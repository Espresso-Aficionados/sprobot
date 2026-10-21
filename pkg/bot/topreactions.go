package bot

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

type starboardSettings struct {
	OutputChannelID snowflake.ID   `json:"output_channel_id"`
	Emoji           string         `json:"emoji"`
	Threshold       int            `json:"threshold"`
	Blacklist       []snowflake.ID `json:"blacklist"`
}

const (
	defaultThreshold = 5
	// unpostedEntryMaxAge is how long reactions on a message that has not
	// reached the threshold are remembered.
	unpostedEntryMaxAge = 30 * 24 * time.Hour
	// starboardMaxAge is the message age past which reactions are ignored: an
	// older message is not posted, and a post's count stops changing. Posted
	// entries are still kept, so deleting the message deletes its post.
	starboardMaxAge = 90 * 24 * time.Hour
)

// starboardUpdateDelay is how long an edit to a post waits, so a burst of
// reactions becomes one edit. A variable so tests can shorten it.
var starboardUpdateDelay = 3 * time.Second

func (s *starboardSettings) effectiveThreshold() int {
	if s.Threshold > 0 {
		return s.Threshold
	}
	return defaultThreshold
}

func (s *starboardSettings) isBlacklisted(ids ...snowflake.ID) bool {
	return containsAny(s.Blacklist, ids...)
}

// lookupChannel returns a guild channel or thread from disgo's channel cache,
// which the gateway keeps current, falling back to REST for one that isn't
// cached. Nothing is remembered, so a failed lookup is retried next time.
func (b *Bot) lookupChannel(channelID snowflake.ID) (discord.GuildChannel, error) {
	if ch, ok := b.Client.Caches.Channel(channelID); ok {
		return ch, nil
	}
	ch, err := b.Client.Rest.GetChannel(channelID)
	if err != nil {
		return nil, err
	}
	gc, ok := ch.(discord.GuildChannel)
	if !ok {
		return nil, fmt.Errorf("channel %d is not a guild channel", channelID)
	}
	return gc, nil
}

// resolveChannelParents returns the channel ID plus its parent and category
// IDs: for a thread, its channel and that channel's category. A lookup that
// fails is left out of the result. The *sync.Map is unused and kept only so
// existing callers compile; parents come from the channel cache.
func (b *Bot) resolveChannelParents(channelID snowflake.ID, _ *sync.Map) []snowflake.ID {
	ids := []snowflake.ID{channelID}
	ch, err := b.lookupChannel(channelID)
	if err != nil {
		b.Log.Warn("Failed to look up channel parents", "channel_id", channelID, "error", err)
		return ids
	}
	parentID := ch.ParentID()
	if parentID == nil {
		return ids
	}
	ids = append(ids, *parentID)
	if _, isThread := ch.(discord.GuildThread); !isThread {
		return ids
	}
	parent, err := b.lookupChannel(*parentID)
	if err != nil {
		b.Log.Warn("Failed to look up thread parent", "channel_id", *parentID, "error", err)
		return ids
	}
	if categoryID := parent.ParentID(); categoryID != nil {
		ids = append(ids, *categoryID)
	}
	return ids
}

// rolesCanView reports whether a member holding roles (besides @everyone) can
// view a channel with the given overwrites, computed as Discord does: guild
// permissions, then the @everyone overwrite, then the roles' overwrites
// combined, denies before allows. Member overwrites are ignored, and
// Administrator sees everything.
func rolesCanView(guildID snowflake.ID, everyone discord.Role, roles []discord.Role, overwrites discord.PermissionOverwrites) bool {
	perms := everyone.Permissions
	for _, r := range roles {
		perms = perms.Add(r.Permissions)
	}
	if perms.Has(discord.PermissionAdministrator) {
		return true
	}
	if ow, ok := overwrites.Role(guildID); ok {
		perms = perms.Remove(ow.Deny).Add(ow.Allow)
	}
	var deny, allow discord.Permissions
	for _, r := range roles {
		if ow, ok := overwrites.Role(r.ID); ok && r.ID != guildID {
			deny, allow = deny.Add(ow.Deny), allow.Add(ow.Allow)
		}
	}
	return perms.Remove(deny).Add(allow).Has(discord.PermissionViewChannel)
}

// channelNSFW reports whether a channel is age-restricted. Threads report
// false; their channel decides.
func channelNSFW(ch discord.GuildChannel) bool {
	switch c := ch.(type) {
	case discord.GuildMessageChannel:
		return c.NSFW()
	case discord.GuildForumChannel:
		return c.NSFW
	case discord.GuildMediaChannel:
		return c.NSFW
	}
	return false
}

// starboardAudienceOK reports whether a message in source may be reposted to
// output: every member who can view output, whatever their roles, must also
// be able to view source, so the starboard never shows a message to anyone
// who couldn't already read it (member overwrites aside). A server where one
// broad "access" role gates both channels passes; a staff channel, or one
// that a restriction role is denied, does not. An age-restricted source
// passes only for an age-restricted output. A thread is judged by its channel
// (sourceParent), whose overwrites it inherits, and a private thread never
// passes. An output thread must be passed as its channel. roles must include
// @everyone, whose ID is the guild's. A nil channel or a missing @everyone
// role means the data isn't available, which is an error so the caller can
// fail closed.
func starboardAudienceOK(guildID snowflake.ID, roles []discord.Role, source, sourceParent, output discord.GuildChannel) (bool, error) {
	if source == nil {
		return false, errors.New("source channel not found")
	}
	if output == nil {
		return false, errors.New("output channel not found")
	}
	if _, ok := output.(discord.GuildThread); ok {
		return false, errors.New("output is a thread, not its channel")
	}
	if thread, ok := source.(discord.GuildThread); ok {
		if thread.Type() == discord.ChannelTypeGuildPrivateThread {
			return false, nil
		}
		if sourceParent == nil {
			return false, errors.New("thread's channel not found")
		}
		source = sourceParent
	}
	everyoneIdx := slices.IndexFunc(roles, func(r discord.Role) bool { return r.ID == guildID })
	if everyoneIdx < 0 {
		return false, errors.New("@everyone role not cached")
	}
	if channelNSFW(source) && !channelNSFW(output) {
		return false, nil
	}

	everyone := roles[everyoneIdx]
	sourceOverwrites, outputOverwrites := source.PermissionOverwrites(), output.PermissionOverwrites()
	leaks := func(held ...discord.Role) bool {
		return rolesCanView(guildID, everyone, held, outputOverwrites) &&
			!rolesCanView(guildID, everyone, held, sourceOverwrites)
	}
	// A member who sees output but not source has at most two roles that
	// matter: one granting output, and one whose overwrite denies source. So
	// @everyone alone, each role, and each role paired with each role denied
	// source cover every combination of roles.
	var denied []discord.Role
	for _, r := range roles {
		if ow, ok := sourceOverwrites.Role(r.ID); ok && r.ID != guildID && ow.Deny.Has(discord.PermissionViewChannel) {
			denied = append(denied, r)
		}
	}
	if leaks() {
		return false, nil
	}
	for _, r := range roles {
		if r.ID == guildID {
			continue
		}
		if leaks(r) {
			return false, nil
		}
		for _, d := range denied {
			if d.ID != r.ID && leaks(r, d) {
				return false, nil
			}
		}
	}
	return true, nil
}

// starboardVisible applies starboardAudienceOK to a reaction in channelID. It
// fails closed, logging why, when a channel or the roles can't be found.
func (b *Bot) starboardVisible(guildID, channelID, outputID snowflake.ID) bool {
	source, _ := b.lookupChannel(channelID)
	var parent discord.GuildChannel
	if thread, ok := source.(discord.GuildThread); ok {
		parent, _ = b.lookupChannel(*thread.ParentID())
	}
	output, _ := b.lookupChannel(outputID)
	if thread, ok := output.(discord.GuildThread); ok {
		// Only configs older than the channel type check have a thread as
		// output. A thread's audience is at most its channel's, so judging
		// by the channel is never less strict.
		output, _ = b.lookupChannel(*thread.ParentID())
	}
	roles := slices.Collect(b.Client.Caches.Roles(guildID))
	ok, err := starboardAudienceOK(guildID, roles, source, parent, output)
	if err != nil {
		b.Log.Warn("Starboard skipped a reaction: channel visibility unknown",
			"guild_id", guildID, "channel_id", channelID, "error", err)
	}
	return ok
}

// starboardOutputType reports whether the starboard can post in a channel of
// type t.
func starboardOutputType(t discord.ChannelType) bool {
	return t == discord.ChannelTypeGuildText || t == discord.ChannelTypeGuildNews
}

type starboardEntry struct {
	ChannelID      snowflake.ID `json:"channel_id"`
	AuthorID       snowflake.ID `json:"author_id"`
	Count          int          `json:"count"`
	StarboardMsgID snowflake.ID `json:"starboard_msg_id"`
	// PostChannelID is the channel the post went to. Posts made before it was
	// recorded get it at load (backfillPostChannels), unless the starboard
	// was disabled then; those are taken to be in the current output channel.
	PostChannelID snowflake.ID `json:"post_channel_id,omitempty"`
	ChannelName   string       `json:"channel_name,omitempty"`
	// SourceDeleted marks an entry whose message is gone. It is kept only
	// until its post is deleted, and reactions no longer change it.
	SourceDeleted bool `json:"source_deleted,omitempty"`
}

// postChannel returns the channel holding the entry's post.
func (e starboardEntry) postChannel(outputID snowflake.ID) snowflake.ID {
	if e.PostChannelID != 0 {
		return e.PostChannelID
	}
	return outputID
}

// backfillPostChannels records the output channel on posts made before
// PostChannelID was stored, taking them to be in the output channel at load as
// edits always have, so later changing or disabling the output doesn't lose
// where they are.
func backfillPostChannels(st *starboardState) {
	if st.Settings.OutputChannelID == 0 {
		return
	}
	for id, e := range st.Entries {
		if e.StarboardMsgID != 0 && e.PostChannelID == 0 {
			e.PostChannelID = st.Settings.OutputChannelID
			st.Entries[id] = e
		}
	}
}

type starboardState struct {
	mu       sync.Mutex
	Settings starboardSettings               `json:"settings"`
	Entries  map[snowflake.ID]starboardEntry `json:"entries"` // keyed by source message ID

	// work holds the post or update in flight per source message. It is not
	// persisted: nothing is in flight after a restart.
	work map[snowflake.ID]*starboardWork
}

// starboardWork serializes the Discord calls for one source message.
type starboardWork struct {
	posting  bool // the post is being created; no other reaction may post
	updating bool // an edit is waiting or running; no other may start
	dirty    bool // the count changed since the post or edit read it
}

func (st *starboardState) lock()                          { st.mu.Lock() }
func (st *starboardState) unlock()                        { st.mu.Unlock() }
func (st *starboardState) getBlacklist() []snowflake.ID   { return st.Settings.Blacklist }
func (st *starboardState) setBlacklist(bl []snowflake.ID) { st.Settings.Blacklist = bl }

// startWork returns msgID's work record, creating it. Must be called with mu held.
func (st *starboardState) startWork(msgID snowflake.ID) *starboardWork {
	if st.work == nil {
		st.work = make(map[snowflake.ID]*starboardWork)
	}
	w := st.work[msgID]
	if w == nil {
		w = &starboardWork{}
		st.work[msgID] = w
	}
	return w
}

// endWork drops msgID's work record once nothing is in flight. Must be called
// with mu held.
func (st *starboardState) endWork(msgID snowflake.ID) {
	if w := st.work[msgID]; w != nil && !w.posting && !w.updating {
		delete(st.work, msgID)
	}
}

type reactionChange int

const (
	reactionAdded reactionChange = iota
	reactionRemoved
	reactionsCleared // all reactions, or all of the starboard emoji, removed
)

type starboardAction int

const (
	starboardNone starboardAction = iota
	starboardPost
	starboardUpdate
)

// applyReaction records a reaction change on msgID's entry and returns what
// the caller must do next; the work record it sets makes that caller the only
// one posting or editing for the message. seed is the entry created by the
// first add. Must be called with mu held; it makes no Discord calls.
func (st *starboardState) applyReaction(msgID snowflake.ID, change reactionChange, seed starboardEntry) starboardAction {
	entry, ok := st.Entries[msgID]
	if entry.SourceDeleted {
		// A reaction handled after the delete; the post is being deleted.
		return starboardNone
	}
	if !ok {
		if change != reactionAdded {
			return starboardNone
		}
		entry = seed
	}
	switch change {
	case reactionAdded:
		entry.Count++
	case reactionRemoved:
		entry.Count = max(entry.Count-1, 0)
	case reactionsCleared:
		entry.Count = 0
	}
	st.Entries[msgID] = entry

	if w := st.work[msgID]; w != nil {
		// The post or edit in flight re-reads the count when it is done.
		w.dirty = true
		return starboardNone
	}
	switch {
	case entry.StarboardMsgID != 0:
		st.startWork(msgID).updating = true
		return starboardUpdate
	case change == reactionAdded && entry.Count >= st.Settings.effectiveThreshold():
		st.startWork(msgID).posting = true
		return starboardPost
	}
	return starboardNone
}

func intPtr(v int) *int { return &v }

// emojiDisplay returns the display form of an emoji string.
// Custom emoji in "name:id" format becomes "<:name:id>"; unicode emoji is returned as-is.
func emojiDisplay(emoji string) string {
	if parts := strings.SplitN(emoji, ":", 2); len(parts) == 2 && parts[1] != "" {
		return "<:" + emoji + ">"
	}
	return emoji
}

// customEmojiRegexp matches Discord custom emoji like <:name:123> or <a:name:123>.
var customEmojiRegexp = regexp.MustCompile(`^<a?:(\w+):(\d+)>$`)

// parseEmojiInput converts user input (unicode or <:name:id>) to storage form (unicode or name:id).
func parseEmojiInput(input string) string {
	if m := customEmojiRegexp.FindStringSubmatch(input); m != nil {
		return m[1] + ":" + m[2]
	}
	return input
}

// reactionCount returns the count of the reaction matching emoji (stored as
// unicode or name:id, like PartialEmoji.Reaction).
func reactionCount(reactions []discord.MessageReaction, emoji string) int {
	for _, r := range reactions {
		key := r.Emoji.Name
		if r.Emoji.ID != 0 {
			key += ":" + r.Emoji.ID.String()
		}
		if key == emoji {
			return r.Count
		}
	}
	return 0
}

// --- Load / Save / Persist ---

func (b *Bot) saveStarboard() {
	b.starboard.each(func(_ snowflake.ID, st *starboardState) {
		st.mu.Lock()
		pruneStarboardEntries(st, time.Now())
		st.mu.Unlock()
	})
	b.starboard.save()
}

// pruneStarboardEntries removes unposted entries older than
// unpostedEntryMaxAge, unless a post is in flight. Posted entries are kept for
// as long as their post: an entry is the only link from a message to its
// post, which is deleted when the message is. Must be called with mu held.
func pruneStarboardEntries(st *starboardState, now time.Time) {
	cutoff := now.Add(-unpostedEntryMaxAge)
	for msgID, entry := range st.Entries {
		if entry.StarboardMsgID == 0 && st.work[msgID] == nil && msgID.Time().Before(cutoff) {
			delete(st.Entries, msgID)
		}
	}
}

// --- Reaction Event Handlers ---

func (b *Bot) onReactionAdd(e *events.GuildMessageReactionAdd) {
	if e.Member.User.Bot {
		return
	}

	st := b.starboard.get(e.GuildID)
	if st == nil {
		return
	}

	st.mu.Lock()
	settings := st.Settings
	settings.Blacklist = slices.Clone(st.Settings.Blacklist)
	st.mu.Unlock()

	if settings.OutputChannelID == 0 || settings.Emoji == "" ||
		e.Emoji.Reaction() != settings.Emoji || e.ChannelID == settings.OutputChannelID {
		return
	}
	if time.Since(e.MessageID.Time()) > starboardMaxAge {
		return
	}
	if settings.isBlacklisted(b.resolveChannelParents(e.ChannelID, nil)...) {
		return
	}
	if !b.starboardVisible(e.GuildID, e.ChannelID, settings.OutputChannelID) {
		return
	}

	seed := starboardEntry{ChannelID: e.ChannelID}
	if e.MessageAuthorID != nil {
		seed.AuthorID = *e.MessageAuthorID
	}
	st.mu.Lock()
	action := st.applyReaction(e.MessageID, reactionAdded, seed)
	st.mu.Unlock()

	b.runStarboardAction(e.GuildID, e.MessageID, st, action)
}

func (b *Bot) onReactionRemove(e *events.GuildMessageReactionRemove) {
	b.recordReactionRemoval(e.GuildID, e.MessageID, e.Emoji.Reaction(), reactionRemoved)
}

func (b *Bot) onReactionRemoveAll(e *events.GuildMessageReactionRemoveAll) {
	b.recordReactionRemoval(e.GuildID, e.MessageID, "", reactionsCleared)
}

func (b *Bot) onReactionRemoveEmoji(e *events.GuildMessageReactionRemoveEmoji) {
	b.recordReactionRemoval(e.GuildID, e.MessageID, e.Emoji.Reaction(), reactionsCleared)
}

// recordReactionRemoval lowers msgID's count and edits its post if it has
// one. emoji is the removed reaction, or "" when every reaction was removed.
func (b *Bot) recordReactionRemoval(guildID, msgID snowflake.ID, emoji string, change reactionChange) {
	st := b.starboard.get(guildID)
	if st == nil || time.Since(msgID.Time()) > starboardMaxAge {
		return
	}

	st.mu.Lock()
	if st.Settings.Emoji == "" || (emoji != "" && emoji != st.Settings.Emoji) {
		st.mu.Unlock()
		return
	}
	action := st.applyReaction(msgID, change, starboardEntry{})
	st.mu.Unlock()

	b.runStarboardAction(guildID, msgID, st, action)
}

func (b *Bot) runStarboardAction(guildID, msgID snowflake.ID, st *starboardState, action starboardAction) {
	switch action {
	case starboardPost:
		b.postStarboardEntry(guildID, msgID, st)
	case starboardUpdate:
		b.runStarboardUpdates(guildID, msgID, st)
	}
}

// onStarboardSourceDelete deletes the entry and starboard post of a deleted
// message, so its reposted text does not outlive it. A repeated delete finds
// no entry, or retries a post delete that failed.
func (b *Bot) onStarboardSourceDelete(e *events.GuildMessageDelete) {
	if st := b.starboard.get(e.GuildID); st != nil {
		b.markSourcesDeleted(e.GuildID, st, []snowflake.ID{e.MessageID})
	}
}

// onStarboardChannelDelete and onStarboardThreadDelete delete the posts of
// messages in a deleted channel or thread, for which Discord sends no message
// deletes.
func (b *Bot) onStarboardChannelDelete(e *events.GuildChannelDelete) {
	b.deleteChannelSources(e.GuildID, e.ChannelID)
}

func (b *Bot) onStarboardThreadDelete(e *events.ThreadDelete) {
	b.deleteChannelSources(e.GuildID, e.ThreadID)
}

// deleteChannelSources deletes the posts and entries of messages in
// channelID, including its threads that the channel cache knows of.
func (b *Bot) deleteChannelSources(guildID, channelID snowflake.ID) {
	st := b.starboard.get(guildID)
	if st == nil {
		return
	}
	st.mu.Lock()
	byChannel := make(map[snowflake.ID][]snowflake.ID)
	for msgID, entry := range st.Entries {
		byChannel[entry.ChannelID] = append(byChannel[entry.ChannelID], msgID)
	}
	st.mu.Unlock()

	var deleted []snowflake.ID
	for ch, msgIDs := range byChannel {
		thread, isThread := b.Client.Caches.GuildThread(ch)
		if ch == channelID || (isThread && thread.ParentID() != nil && *thread.ParentID() == channelID) {
			deleted = append(deleted, msgIDs...)
		}
	}
	b.markSourcesDeleted(guildID, st, deleted)
}

// markSourcesDeleted flags the entries of deleted messages and deletes their
// posts and entries. One with a post or edit in flight is left to that work,
// which finishes it when done; reactions no longer start any.
func (b *Bot) markSourcesDeleted(guildID snowflake.ID, st *starboardState, msgIDs []snowflake.ID) {
	var idle []snowflake.ID
	st.mu.Lock()
	for _, msgID := range msgIDs {
		entry, ok := st.Entries[msgID]
		if !ok {
			continue
		}
		entry.SourceDeleted = true
		st.Entries[msgID] = entry
		if st.work[msgID] == nil {
			idle = append(idle, msgID)
		}
	}
	st.mu.Unlock()

	for _, msgID := range idle {
		b.removeDeletedEntry(guildID, msgID, st)
	}
	if len(idle) > 0 {
		if err := b.starboard.persist(guildID); err != nil {
			b.Log.Error("Failed to persist starboard after delete", "guild_id", guildID, "error", err)
		}
	}
}

// removeDeletedEntry deletes the post of an entry flagged SourceDeleted, then
// the entry, and reports whether it did. If the post can't be deleted, or its
// channel isn't known, the entry stays flagged for retryStarboardDeletes. The
// caller persists. Must be called with nothing in flight for msgID.
func (b *Bot) removeDeletedEntry(guildID, msgID snowflake.ID, st *starboardState) bool {
	st.mu.Lock()
	entry, ok := st.Entries[msgID]
	outputID := st.Settings.OutputChannelID
	st.mu.Unlock()
	if !ok || !entry.SourceDeleted {
		return false
	}
	if entry.StarboardMsgID != 0 {
		channelID := entry.postChannel(outputID)
		if channelID == 0 || !b.deleteStarboardPost(guildID, msgID, channelID, entry.StarboardMsgID) {
			return false
		}
	}
	st.mu.Lock()
	delete(st.Entries, msgID)
	st.mu.Unlock()
	return true
}

// retryStarboardDeletes retries deleting the posts of deleted messages whose
// first delete failed.
func (b *Bot) retryStarboardDeletes() {
	b.starboard.each(func(guildID snowflake.ID, st *starboardState) {
		var pending []snowflake.ID
		st.mu.Lock()
		for msgID, entry := range st.Entries {
			if entry.SourceDeleted && st.work[msgID] == nil {
				pending = append(pending, msgID)
			}
		}
		st.mu.Unlock()
		removed := false
		for _, msgID := range pending {
			removed = b.removeDeletedEntry(guildID, msgID, st) || removed
		}
		if !removed {
			return
		}
		if err := b.starboard.persist(guildID); err != nil {
			b.Log.Error("Failed to persist starboard after delete", "guild_id", guildID, "error", err)
		}
	})
}

// deleteStarboardPost deletes a post and reports whether it is gone, counting
// one that was already deleted.
func (b *Bot) deleteStarboardPost(guildID, sourceID, channelID, postID snowflake.ID) bool {
	err := b.Client.Rest.DeleteMessage(channelID, postID)
	if err == nil || rest.IsJSONErrorCode(err, rest.JSONErrorCodeUnknownMessage, rest.JSONErrorCodeUnknownChannel) {
		return true
	}
	b.Log.Error("Failed to delete starboard post of a deleted message", "guild_id", guildID,
		"message_id", sourceID, "channel_id", channelID, "post_id", postID, "error", err)
	return false
}

// emojiCDNURL returns a CDN URL for a custom emoji stored as "name:id",
// or empty string for unicode emoji.
func emojiCDNURL(emoji string) string {
	if parts := strings.SplitN(emoji, ":", 2); len(parts) == 2 && parts[1] != "" {
		return "https://cdn.discordapp.com/emojis/" + parts[1] + ".png"
	}
	return ""
}

// starboardFooter shows the count and source channel; channelName is "" when
// the channel couldn't be looked up.
func starboardFooter(emoji string, count int, channelName string) discord.EmbedFooter {
	if channelName == "" {
		channelName = "unknown"
	}
	if url := emojiCDNURL(emoji); url != "" {
		return discord.EmbedFooter{
			Text:    fmt.Sprintf("%d | #%s", count, channelName),
			IconURL: url,
		}
	}
	return discord.EmbedFooter{
		Text: fmt.Sprintf("%s %d | #%s", emoji, count, channelName),
	}
}

// resolveChannelName returns the channel's name, or "" if it can't be looked
// up. Callers must not store "".
func (b *Bot) resolveChannelName(channelID snowflake.ID) string {
	ch, err := b.lookupChannel(channelID)
	if err != nil {
		return ""
	}
	return ch.Name()
}

// starboardNonce identifies the post for a source message. With
// enforce_nonce, Discord returns the existing post instead of creating a
// second one if the same nonce is sent again within a few minutes.
func starboardNonce(msgID snowflake.ID) string {
	return fmt.Sprintf("sb%d", msgID) // at most 22 of the allowed 25 characters
}

func starboardEmbed(guildID snowflake.ID, msg discord.Message, emoji string, count int, channelName string) discord.Embed {
	description := msg.Content
	if len(description) > 2000 {
		description = description[:2000] + "..."
	}
	description += fmt.Sprintf("\n\n[Jump to message](%s)", messageLink(guildID, msg.ChannelID, msg.ID))

	footer := starboardFooter(emoji, count, channelName)
	embed := discord.Embed{
		Author: &discord.EmbedAuthor{
			Name:    msg.Author.EffectiveName(),
			IconURL: msg.Author.EffectiveAvatarURL(),
		},
		Description: description,
		Color:       colorTeal,
		Timestamp:   &msg.CreatedAt,
		Footer:      &footer,
	}

	// Attach the first image if present
	for _, att := range msg.Attachments {
		if att.ContentType != nil && strings.HasPrefix(*att.ContentType, "image/") {
			embed.Image = &discord.EmbedResource{URL: att.URL}
			break
		}
	}
	return embed
}

// --- Starboard Posting ---

// postResult is the outcome of one attempt to post a message.
type postResult int

const (
	postFailed         postResult = iota // a Discord call failed, or the message is gone
	postBelowThreshold                   // the message's own count is too low
	postDone
)

// postStarboardEntry posts msgID. The caller holds the message's posting flag
// (see applyReaction), so no second post can start. A reaction that arrives
// meanwhile marks it dirty: once the post exists it gets one edit, and if the
// count read was too low it is read again.
func (b *Bot) postStarboardEntry(guildID, msgID snowflake.ID, st *starboardState) {
	result := b.createStarboardPost(guildID, msgID, st)

	st.mu.Lock()
	w := st.work[msgID]
	for result == postBelowThreshold && w.dirty {
		st.mu.Unlock()
		result = b.createStarboardPost(guildID, msgID, st)
		st.mu.Lock()
	}
	sourceDeleted := st.Entries[msgID].SourceDeleted
	w.posting = false
	followUp := result == postDone && w.dirty && !sourceDeleted
	w.updating = followUp
	st.endWork(msgID)
	st.mu.Unlock()

	switch {
	case followUp:
		b.runStarboardUpdates(guildID, msgID, st)
	case sourceDeleted:
		b.finishSourceDelete(guildID, msgID, st)
	}
}

// finishSourceDelete removes msgID's flagged entry and its post, if any, once
// the work in flight when its message was deleted has ended.
func (b *Bot) finishSourceDelete(guildID, msgID snowflake.ID, st *starboardState) {
	b.removeDeletedEntry(guildID, msgID, st)
	if err := b.starboard.persist(guildID); err != nil {
		b.Log.Error("Failed to persist starboard after delete", "guild_id", guildID, "error", err)
	}
}

// createStarboardPost posts msgID if its actual reaction count meets the
// threshold.
func (b *Bot) createStarboardPost(guildID, msgID snowflake.ID, st *starboardState) postResult {
	st.mu.Lock()
	st.work[msgID].dirty = false
	entry := st.Entries[msgID]
	settings := st.Settings
	st.mu.Unlock()

	msg, err := b.Client.Rest.GetMessage(entry.ChannelID, msgID)
	if err != nil {
		b.Log.Error("Failed to fetch message for starboard", "guild_id", guildID, "message_id", msgID, "error", err)
		return postFailed
	}

	// Reaction events are handled concurrently, so the event counter can
	// drift; the message's own reactions decide. A reaction that arrived
	// during the read marks the message dirty, and the caller reads again.
	count := reactionCount(msg.Reactions, settings.Emoji)
	st.mu.Lock()
	entry, ok := st.Entries[msgID]
	if ok {
		entry.Count = count
		st.Entries[msgID] = entry
	}
	st.mu.Unlock()
	if !ok || entry.SourceDeleted {
		return postFailed
	}
	if count < settings.effectiveThreshold() {
		return postBelowThreshold
	}

	channelName := b.resolveChannelName(entry.ChannelID)
	embed := starboardEmbed(guildID, *msg, settings.Emoji, count, channelName)
	create := discord.MessageCreate{Embeds: []discord.Embed{embed}}.
		WithNonce(starboardNonce(msgID)).WithEnforceNonce(true)
	sent, err := b.Client.Rest.CreateMessage(settings.OutputChannelID, create)
	if err != nil {
		b.Log.Error("Failed to post starboard entry", "guild_id", guildID, "message_id", msgID, "error", err)
		return postFailed
	}

	// The post is recorded even if the message was deleted meanwhile, so the
	// caller's finishSourceDelete can delete it.
	st.mu.Lock()
	entry, ok = st.Entries[msgID]
	if ok {
		entry.StarboardMsgID = sent.ID
		entry.PostChannelID = settings.OutputChannelID
		if channelName != "" {
			entry.ChannelName = channelName
		}
		st.Entries[msgID] = entry
	}
	st.mu.Unlock()

	if !ok {
		// Nothing removes an entry with a post in flight, but a post nothing
		// tracks could never be deleted.
		b.deleteStarboardPost(guildID, msgID, settings.OutputChannelID, sent.ID)
		return postFailed
	}
	if err := b.starboard.persist(guildID); err != nil {
		b.Log.Error("Failed to persist starboard after post", "guild_id", guildID, "error", err)
	}
	return postDone
}

// runStarboardUpdates edits msgID's post, again while reactions keep arriving
// during an edit. The caller holds the message's updating flag, so edits for
// a message run one at a time and can't land out of order, and the wait
// before each folds a burst of reactions into one edit.
func (b *Bot) runStarboardUpdates(guildID, msgID snowflake.ID, st *starboardState) {
	for {
		time.Sleep(starboardUpdateDelay)

		st.mu.Lock()
		st.work[msgID].dirty = false
		st.mu.Unlock()

		b.updateStarboardPost(guildID, msgID, st)

		st.mu.Lock()
		w := st.work[msgID]
		if !w.dirty {
			w.updating = false
			st.endWork(msgID)
			sourceDeleted := st.Entries[msgID].SourceDeleted
			st.mu.Unlock()
			if sourceDeleted {
				b.finishSourceDelete(guildID, msgID, st)
			}
			return
		}
		st.mu.Unlock()
	}
}

// updateStarboardPost sets the post's footer to the source message's reaction
// count, read just before the edit.
func (b *Bot) updateStarboardPost(guildID, msgID snowflake.ID, st *starboardState) {
	st.mu.Lock()
	entry, ok := st.Entries[msgID]
	settings := st.Settings
	st.mu.Unlock()

	if !ok || entry.StarboardMsgID == 0 || entry.SourceDeleted || settings.OutputChannelID == 0 {
		return
	}
	postChannel := entry.postChannel(settings.OutputChannelID)

	existing, err := b.Client.Rest.GetMessage(postChannel, entry.StarboardMsgID)
	if err != nil {
		b.Log.Error("Failed to fetch starboard message for update", "guild_id", guildID, "message_id", msgID, "error", err)
		return
	}
	if len(existing.Embeds) == 0 {
		return
	}

	// Fill in a channel name an older post stored as "unknown" or not at all.
	channelName := entry.ChannelName
	if channelName == "" || channelName == "unknown" {
		channelName = b.resolveChannelName(entry.ChannelID)
	}

	source, err := b.Client.Rest.GetMessage(entry.ChannelID, msgID)
	if err != nil {
		b.Log.Error("Failed to fetch message for starboard update", "guild_id", guildID, "message_id", msgID, "error", err)
		return
	}
	count := reactionCount(source.Reactions, settings.Emoji)

	st.mu.Lock()
	entry, ok = st.Entries[msgID]
	if ok {
		entry.Count = count
		if channelName != "" {
			entry.ChannelName = channelName
		}
		st.Entries[msgID] = entry
	}
	st.mu.Unlock()
	if !ok {
		return
	}

	embed := existing.Embeds[0]
	footer := starboardFooter(settings.Emoji, count, channelName)
	embed.Footer = &footer

	emptyContent := ""
	embeds := []discord.Embed{embed}
	_, err = b.Client.Rest.UpdateMessage(postChannel, entry.StarboardMsgID, discord.MessageUpdate{
		Content: &emptyContent,
		Embeds:  &embeds,
	})
	if err != nil {
		b.Log.Error("Failed to update starboard entry", "guild_id", guildID, "message_id", msgID, "error", err)
	}
}

// --- Command Handlers ---

func (b *Bot) handleStarboardConfig(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.starboard.get(*guildID)
	if st == nil {
		botutil.RespondEphemeral(e, "Starboard is not configured for this server.")
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	persist := func() error { return b.starboard.persist(*guildID) }
	switch *subCmd {
	case "config":
		b.handleSBConfigSet(e, *guildID, st)
	case "show-config":
		b.handleSBConfigShow(e, st)
	case "disable":
		b.handleSBConfigDisable(e, *guildID, st)
	case "filter-add":
		b.handleBlacklistAdd(e, *guildID, st, persist, "Starboard")
	case "filter-remove":
		b.handleBlacklistRemove(e, *guildID, st, persist, "Starboard")
	case "filter-list":
		b.handleBlacklistList(e, st, "Starboard")
	case "filter-clear":
		b.handleBlacklistClear(e, *guildID, st, persist, "Starboard")
	}
}

func (b *Bot) handleSBConfigSet(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *starboardState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	b.Log.Info("Starboard config set", "user_id", e.User().ID, "guild_id", guildID)

	ch, hasChannel := data.OptChannel("channel")
	if hasChannel && !starboardOutputType(ch.Type) {
		botutil.RespondEphemeral(e, "The starboard channel must be a text or announcement channel.")
		return
	}

	st.mu.Lock()
	if hasChannel {
		st.Settings.OutputChannelID = ch.ID
	}
	if v, ok := data.OptString("emoji"); ok {
		newEmoji := parseEmojiInput(v)
		if newEmoji != st.Settings.Emoji {
			// Emoji changed — clear all unposted entries that aren't mid-post
			for id, entry := range st.Entries {
				if entry.StarboardMsgID == 0 && st.work[id] == nil {
					delete(st.Entries, id)
				}
			}
			st.Settings.Emoji = newEmoji
		}
	}
	if v, ok := data.OptInt("threshold"); ok {
		st.Settings.Threshold = v
	}
	st.mu.Unlock()

	if err := b.starboard.persist(guildID); err != nil {
		b.Log.Error("Failed to persist starboard config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Starboard configuration updated.")
}

func (b *Bot) handleSBConfigShow(e *events.ApplicationCommandInteractionCreate, st *starboardState) {
	b.Log.Info("Starboard config show", "user_id", e.User().ID, "guild_id", *e.GuildID())

	st.mu.Lock()
	s := st.Settings
	entryCount := len(st.Entries)
	st.mu.Unlock()

	var channelStr string
	if s.OutputChannelID == 0 {
		channelStr = "Not set (disabled)"
	} else {
		channelStr = fmt.Sprintf("<#%d>", s.OutputChannelID)
	}

	var emojiStr string
	if s.Emoji == "" {
		emojiStr = "Not set"
	} else {
		emojiStr = emojiDisplay(s.Emoji)
	}

	lines := []string{
		fmt.Sprintf("**Output Channel:** %s", channelStr),
		fmt.Sprintf("**Emoji:** %s", emojiStr),
		fmt.Sprintf("**Threshold:** %d", s.effectiveThreshold()),
		fmt.Sprintf("**Tracked Entries:** %d", entryCount),
	}

	botutil.RespondEphemeral(e, strings.Join(lines, "\n"))
}

func (b *Bot) handleSBConfigDisable(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *starboardState) {
	b.Log.Info("Starboard config disable", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	st.Settings.OutputChannelID = 0
	st.mu.Unlock()

	if err := b.starboard.persist(guildID); err != nil {
		b.Log.Error("Failed to persist starboard config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Starboard disabled. Settings preserved.")
}
