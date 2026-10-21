package bot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

const embedSplitSize = 1024

// embedTotalLimit is Discord's limit on the text in an embed: title,
// description, field names and values, footer text and author name combined.
const embedTotalLimit = 6000

// messageContentLimit is Discord's limit on a message's content.
const messageContentLimit = 2000

// truncatedMarker ends text that was cut to fit a Discord limit.
const truncatedMarker = "\n… (truncated)"

// modFileSaveConcurrency bounds how many attachments of one message are saved
// at once. Each save spools up to 500MB to temp disk, so keep it low.
const modFileSaveConcurrency = 2

var errNotForum = errors.New("mod log channel is not a forum channel")

type modLogState struct {
	mu        sync.Mutex
	ChannelID snowflake.ID `json:"channel_id"`
}

func defaultModLogConfig() map[snowflake.ID]snowflake.ID {
	return map[snowflake.ID]snowflake.ID{
		726985544038612993:  1141477354129080361,
		1013566342345019512: 1142519200682876938,
	}
}

func (b *Bot) handleModLogMenu(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.MessageCommandInteractionData)
	if !ok {
		botutil.RespondEphemeral(e, "Oops! Something went wrong.")
		return
	}
	msg := data.TargetMessage()
	b.modLogPending.Put(msg)

	b.Log.Info("Processing save message to mod log",
		"user_id", userIDStr(e),
		"guild_id", guildIDStr(e),
	)

	// Send a modal asking for a mod note
	err := e.Modal(discord.ModalCreate{
		CustomID: fmt.Sprintf("modlog_%d_%d", msg.ChannelID, msg.ID),
		Title:    "Save Message to Mod Logs",
		Components: []discord.LayoutComponent{
			discord.NewLabel(
				"Mod Note about message",
				discord.TextInputComponent{
					CustomID:    "mod_note",
					Placeholder: "Context for why we're saving this",
					Style:       discord.TextInputStyleParagraph,
					MaxLength:   1024,
					Required:    false,
				},
			),
		},
	})
	if err != nil {
		b.Log.Error("Failed to respond with mod log modal", "error", err)
	}
}

func (b *Bot) handleModLogModalSubmit(e *events.ModalSubmitInteractionCreate, channelID, messageID snowflake.ID) {
	// Defer the response since this might take a while
	if err := e.DeferCreateMessage(true); err != nil {
		b.Log.Error("Failed to defer mod log modal response", "error", err)
		return
	}
	followup := func(content string) { botutil.FollowupEphemeral(b.Client, e.Token(), content) }

	// Use the message captured when the menu was opened, so it can still be
	// saved if it is deleted while the mod writes the note. Fall back to REST
	// for modals submitted across a bot restart.
	msg, ok := b.modLogPending.Get(messageID)
	if !ok {
		fetched, err := b.Client.Rest.GetMessage(channelID, messageID)
		if err != nil {
			b.Log.Error("Failed to fetch original message", "error", err)
			followup("Couldn't fetch the message. It may have been deleted while the bot restarted.")
			return
		}
		msg = *fetched
	}

	// Resolve where the entry goes before saving attachments, which can take
	// minutes.
	guildID := *e.GuildID()
	st := b.modLog.get(guildID)
	var modLogChannelID snowflake.ID
	if st != nil {
		st.mu.Lock()
		modLogChannelID = st.ChannelID
		st.mu.Unlock()
	}
	if modLogChannelID == 0 {
		b.Log.Info("Mod log channel not set", "guild_id", guildID)
		followup("No mod log channel is configured; set one with /config modlog set.")
		return
	}
	if _, err := b.findOrCreateModLogThread(modLogChannelID, msg.Author); err != nil {
		b.Log.Error("Failed to find mod log thread", "guild_id", guildID, "user_id", msg.Author.ID, "error", err)
		followup("Couldn't find or create the user's mod log thread, so nothing was saved. Try again in a minute.")
		return
	}

	var saved []savedModFile
	if len(msg.Attachments) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), botutil.DeferredWorkBudget)
		guildStr := guildID.String()
		saved = saveModFiles(ctx, msg.Attachments, modFileSaveConcurrency, func(ctx context.Context, att discord.Attachment) savedModFile {
			return b.saveModAttachment(ctx, guildStr, att)
		})
		cancel()
	}
	var lines, keys []string
	for _, s := range saved {
		lines = append(lines, s.line)
		if s.key != "" {
			keys = append(keys, s.key)
		}
	}

	embed := buildModLogEmbed(modLogEntry{
		msg:         msg,
		channelName: b.channelNameOrID(channelID),
		requester:   e.User(),
		attachments: lines,
		link:        messageLink(guildID, channelID, messageID),
		note:        e.Data.Text("mod_note"),
		archivedAt:  time.Now().UTC(),
	})
	threadID, sentMsg, err := b.postModLogEntry(modLogChannelID, msg.Author, embed, keys)
	if err != nil {
		b.Log.Error("Failed to send mod log message", "guild_id", guildID, "error", err)
		followup("Oops! Something went wrong posting to the mod log.")
		return
	}

	notice := modLogSavedNotice(msg.Author.Username, b.channelNameOrID(modLogChannelID), b.channelNameOrID(threadID),
		messageLink(guildID, threadID, sentMsg.ID), len(msg.Attachments)-len(keys), len(msg.Attachments))
	if _, err := b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
		Embeds: []discord.Embed{notice},
		Flags:  discord.MessageFlagEphemeral,
	}); err != nil {
		b.Log.Error("Failed to send mod log confirmation", "error", err)
	}
}

// modLogEntry is what a saved message's mod log entry shows.
type modLogEntry struct {
	msg         discord.Message
	channelName string
	requester   discord.User
	attachments []string // one line per attachment
	link        string
	note        string
	archivedAt  time.Time
}

// buildModLogEmbed lays out a saved message's mod log entry. The content is
// split across fields without cutting a character in two, and cut short with
// truncatedMarker if the embed would otherwise pass Discord's total limit.
func buildModLogEmbed(in modLogEntry) discord.Embed {
	embed := discord.Embed{
		Title: fmt.Sprintf("Message from @%s to #%s", in.msg.Author.Username, in.channelName),
		Author: &discord.EmbedAuthor{
			Name:    in.msg.Author.Username,
			IconURL: in.msg.Author.EffectiveAvatarURL(),
		},
		Footer: &discord.EmbedFooter{
			Text:    fmt.Sprintf("archived on behalf of @%s", in.requester.Username),
			IconURL: in.requester.EffectiveAvatarURL(),
		},
	}

	fields := attachmentFields(in.attachments)
	fields = append(fields, discord.EmbedField{
		Name:  "Link to Message",
		Value: fmt.Sprintf("[Click here](%s)", in.link),
	})
	timestamps := fmt.Sprintf("Created: %s UTC\n", in.msg.CreatedAt.UTC().Format(time.DateTime))
	if in.msg.EditedTimestamp != nil {
		timestamps += fmt.Sprintf("Edited: %s UTC\n", in.msg.EditedTimestamp.UTC().Format(time.DateTime))
	}
	timestamps += fmt.Sprintf("Archived: %s UTC", in.archivedAt.Format(time.DateTime))
	fields = append(fields, discord.EmbedField{Name: "Timestamps", Value: timestamps})
	if in.note != "" {
		fields = append(fields, discord.EmbedField{Name: "Mod Note", Value: in.note})
	}
	embed.Fields = fields

	content := truncateText(in.msg.Content, embedTotalLimit-embedTextLen(embed))
	var contentFields []discord.EmbedField
	for _, chunk := range splitText(content, embedSplitSize) {
		contentFields = append(contentFields, discord.EmbedField{Value: chunk})
	}
	embed.Fields = append(contentFields, fields...)
	return embed
}

// modLogSavedNotice is the confirmation shown to the mod who saved a message.
func modLogSavedNotice(author, forumName, threadName, link string, failedAttachments, attachments int) discord.Embed {
	notice := discord.Embed{
		Title: fmt.Sprintf("Saved message from %s in #%s/%s", author, forumName, threadName),
		URL:   link,
	}
	if failedAttachments > 0 {
		notice.Description = fmt.Sprintf("⚠️ %d of %d attachments could not be saved; the mod log entry says why.",
			failedAttachments, attachments)
	}
	return notice
}

// textLen measures s against Discord's length limits. Characters outside the
// Basic Multilingual Plane count twice, as in UTF-16, which is never less than
// Discord's own count.
func textLen(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// splitText splits s into chunks of at most size characters (by textLen),
// never cutting a character in two.
func splitText(s string, size int) []string {
	var chunks []string
	start, n := 0, 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if n+w > size {
			chunks = append(chunks, s[start:i])
			start, n = i, 0
		}
		n += w
	}
	if start < len(s) {
		chunks = append(chunks, s[start:])
	}
	return chunks
}

// truncateText cuts s to at most limit characters (by textLen), ending it with
// truncatedMarker if anything was cut. It returns "" if not even the marker
// fits.
func truncateText(s string, limit int) string {
	if textLen(s) <= limit {
		return s
	}
	limit -= textLen(truncatedMarker)
	if limit < 0 {
		return ""
	}
	n := 0
	for i, r := range s {
		n += utf16.RuneLen(r)
		if n > limit {
			return s[:i] + truncatedMarker
		}
	}
	return s
}

// embedTextLen is the length of the embed text that counts toward
// embedTotalLimit.
func embedTextLen(e discord.Embed) int {
	n := textLen(e.Title) + textLen(e.Description)
	for _, f := range e.Fields {
		n += textLen(f.Name) + textLen(f.Value)
	}
	if e.Footer != nil {
		n += textLen(e.Footer.Text)
	}
	if e.Author != nil {
		n += textLen(e.Author.Name)
	}
	return n
}

// savedModFile is one attachment's line in the mod log entry, and the S3 key
// it was saved under ("" if it wasn't saved).
type savedModFile struct {
	line string
	key  string
}

// saveModFiles saves each attachment with save, at most limit at a time, and
// returns the results in attachment order.
func saveModFiles(ctx context.Context, atts []discord.Attachment, limit int, save func(context.Context, discord.Attachment) savedModFile) []savedModFile {
	out := make([]savedModFile, len(atts))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, att := range atts {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			out[i] = save(ctx, att)
		})
	}
	wg.Wait()
	return out
}

// saveModAttachment saves one attachment and returns the line to show for it
// in the mod log entry: its permanent link, or why it couldn't be saved.
func (b *Bot) saveModAttachment(ctx context.Context, guildID string, att discord.Attachment) savedModFile {
	failed := func(reason string) savedModFile {
		// Discord limits filenames to safe characters, but drop backticks
		// anyway so the name can't break out of its code span.
		name := strings.ReplaceAll(att.Filename, "`", "")
		return savedModFile{line: fmt.Sprintf("⚠️ `%s` (%s) not saved: %s", name, sprobot.FormatBytes(int64(att.Size)), reason)}
	}

	if int64(att.Size) > s3client.MaxFileSize {
		return failed("larger than " + sprobot.FormatBytes(s3client.MaxFileSize))
	}
	key, err := b.S3.SaveModFile(ctx, guildID, att.ProxyURL)
	if err != nil {
		b.Log.Error("Failed to save mod file", "guild_id", guildID, "size", att.Size, "error", err)
		switch {
		case errors.Is(err, s3client.ErrFileTooLarge):
			return failed("larger than " + sprobot.FormatBytes(s3client.MaxFileSize))
		case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
			return failed("timed out")
		default:
			return failed("upload failed")
		}
	}
	return savedModFile{line: b.S3.FileURL(key), key: key}
}

// attachmentFields packs attachment lines into as few embed fields as fit
// Discord's per-field limit.
func attachmentFields(lines []string) []discord.EmbedField {
	var fields []discord.EmbedField
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		name := "Attachments"
		if len(fields) > 0 {
			name = "Attachments (cont.)"
		}
		fields = append(fields, discord.EmbedField{Name: name, Value: cur.String()})
		cur.Reset()
	}
	for _, line := range lines {
		if cur.Len() > 0 && cur.Len()+1+len(line) > embedSplitSize {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(line)
	}
	flush()
	return fields
}

// postModLogEntry posts a saved message's entry in its author's thread. If
// Discord rejects the post, the files saved for it are deleted, since nothing
// links to them. Other failures may have posted it anyway, so the files are
// kept.
func (b *Bot) postModLogEntry(forumID snowflake.ID, author discord.User, embed discord.Embed, keys []string) (snowflake.ID, *discord.Message, error) {
	threadID, sent, err := b.postToModLogThread(forumID, author, discord.MessageCreate{Embeds: []discord.Embed{embed}})
	if err != nil && len(keys) > 0 && !mayHavePosted(err) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if delErr := b.S3.DeleteKeys(ctx, keys...); delErr != nil {
			b.Log.Error("Failed to delete files of an unposted mod log entry", "keys", keys, "error", delErr)
		}
	}
	return threadID, sent, err
}

// mayHavePosted reports whether a failed post might still have gone through.
// Only a 4xx answer from Discord rules that out.
func mayHavePosted(err error) bool {
	var restErr *rest.Error
	if errors.As(err, &restErr) && restErr.Response != nil {
		return restErr.Response.StatusCode >= 500
	}
	return true
}

// postToModLogThread posts msg in author's thread in the mod log forum,
// creating the thread if needed. If the thread turns out to be gone, the forum
// is rescanned and the post retried once.
func (b *Bot) postToModLogThread(forumID snowflake.ID, author discord.User, msg discord.MessageCreate) (snowflake.ID, *discord.Message, error) {
	for attempt := 0; ; attempt++ {
		threadID, err := b.findOrCreateModLogThread(forumID, author)
		if err != nil {
			return 0, nil, err
		}
		sent, err := b.Client.Rest.CreateMessage(threadID, msg)
		if err == nil {
			return threadID, sent, nil
		}
		if attempt > 0 || !rest.IsJSONErrorCode(err, rest.JSONErrorCodeUnknownChannel) {
			return threadID, nil, err
		}
		b.Log.Warn("Mod log thread is gone; rescanning the forum", "forum_id", forumID, "thread_id", threadID)
		b.modLogThreads.reset(forumID)
	}
}

// findOrCreateModLogThread returns the ID of author's thread in the mod log
// forum, creating it if there is none. Calls for the same user are serialized
// so that concurrent posts about them can't both create a thread. A failure to
// list the forum's threads is returned rather than risking a duplicate.
func (b *Bot) findOrCreateModLogThread(forumID snowflake.ID, author discord.User) (snowflake.ID, error) {
	unlock := b.modLogThreads.lock(modLogLockKey{forumID: forumID, userID: author.ID})
	defer unlock()

	threadID, found, err := b.findModLogThread(forumID, author)
	if err != nil || found {
		return threadID, err
	}

	// Hold off scans of the forum while creating, so none can miss the thread.
	unlockScan := b.modLogThreads.lock(modLogLockKey{forumID: forumID, scan: true})
	defer unlockScan()
	post, err := b.Client.Rest.CreatePostInThreadChannel(forumID, discord.ThreadChannelPostCreate{
		Name:                fmt.Sprintf("%s - %d", author.Username, author.ID),
		AutoArchiveDuration: discord.AutoArchiveDuration1w,
		Message: discord.MessageCreate{
			Content: fmt.Sprintf("Topic for thread about @%s.", author.Username),
		},
	})
	if err != nil {
		return 0, fmt.Errorf("creating mod log thread: %w", err)
	}
	b.modLogThreads.update(forumID, post.ID(), post.Name())
	return post.ID(), nil
}

// findModLogThread looks author up in the forum's thread index, scanning the
// forum first if it isn't indexed yet.
func (b *Bot) findModLogThread(forumID snowflake.ID, author discord.User) (snowflake.ID, bool, error) {
	if threadID, found, indexed := b.modLogThreads.find(forumID, author); indexed {
		return threadID, found, nil
	}

	unlock := b.modLogThreads.lock(modLogLockKey{forumID: forumID, scan: true})
	defer unlock()
	if threadID, found, indexed := b.modLogThreads.find(forumID, author); indexed {
		return threadID, found, nil
	}
	forum, err := b.modLogForumChannel(forumID)
	if err != nil {
		return 0, false, err
	}
	threads, err := b.listModLogThreads(forum)
	if err != nil {
		return 0, false, err
	}
	idx := newModLogForum(threads)
	threadID, found := idx.find(author)
	b.modLogThreads.store(forumID, idx)
	return threadID, found, nil
}

// modLogForumChannel returns the mod log forum from the channel cache, or
// from REST if it isn't cached.
func (b *Bot) modLogForumChannel(forumID snowflake.ID) (discord.GuildForumChannel, error) {
	ch, err := b.cachedChannel(forumID)
	if err != nil {
		return discord.GuildForumChannel{}, fmt.Errorf("fetching mod log channel: %w", err)
	}
	forum, ok := ch.(discord.GuildForumChannel)
	if !ok {
		return discord.GuildForumChannel{}, errNotForum
	}
	return forum, nil
}

// listModLogThreads lists every thread in the mod log forum: active ones
// first, then archived ones, most recently archived first. On error it also
// returns the threads listed before it.
func (b *Bot) listModLogThreads(forum discord.GuildForumChannel) ([]discord.GuildThread, error) {
	var threads []discord.GuildThread
	seen := map[snowflake.ID]bool{}
	add := func(t discord.GuildThread) {
		if !seen[t.ID()] {
			seen[t.ID()] = true
			threads = append(threads, t)
		}
	}

	active, err := b.Client.Rest.GetActiveGuildThreads(forum.GuildID())
	if err != nil {
		return nil, fmt.Errorf("listing active threads: %w", err)
	}
	for _, t := range active.Threads {
		if p := t.ParentID(); p != nil && *p == forum.ID() {
			add(t)
		}
	}

	before := time.Time{}
	for {
		page, err := b.Client.Rest.GetPublicArchivedThreads(forum.ID(), before, 100)
		if err != nil {
			return threads, fmt.Errorf("listing archived threads: %w", err)
		}
		for _, t := range page.Threads {
			add(t)
		}
		if !page.HasMore || len(page.Threads) == 0 {
			return threads, nil
		}
		before = page.Threads[len(page.Threads)-1].ThreadMetadata.ArchiveTimestamp
	}
}

// cachedChannel returns a channel from the cache, or from REST if it isn't
// cached.
func (b *Bot) cachedChannel(channelID snowflake.ID) (discord.Channel, error) {
	if ch, ok := b.Client.Caches.Channel(channelID); ok {
		return ch, nil
	}
	return b.Client.Rest.GetChannel(channelID)
}

// channelNameOrID returns a channel's name, or its ID if it can't be found.
func (b *Bot) channelNameOrID(channelID snowflake.ID) string {
	ch, err := b.cachedChannel(channelID)
	if err != nil {
		b.Log.Warn("Failed to look up channel name", "channel_id", channelID, "error", err)
		return channelID.String()
	}
	return ch.Name()
}

// modLogThreadIndex remembers which thread in each mod log forum belongs to
// which user, so that posting to the mod log doesn't list the whole forum.
// Each forum is indexed by one full scan, then kept current from thread events
// and the threads the bot creates. It holds only user and thread IDs, plus the
// names of legacy threads whose name has no user ID, and is never persisted.
// The zero value is ready to use.
type modLogThreadIndex struct {
	mu     sync.Mutex
	forums map[snowflake.ID]*modLogForum // forum ID -> index; absent until scanned
	busy   map[modLogLockKey]chan struct{}
}

// modLogLockKey names what lock serializes: find-or-create for one user in a
// forum, or (with scan set) the scan of a forum.
type modLogLockKey struct {
	forumID snowflake.ID
	userID  snowflake.ID
	scan    bool
}

// lock waits until no one else holds key, then holds it until the returned
// function is called.
func (x *modLogThreadIndex) lock(key modLogLockKey) (unlock func()) {
	for {
		x.mu.Lock()
		held, ok := x.busy[key]
		if !ok {
			if x.busy == nil {
				x.busy = make(map[modLogLockKey]chan struct{})
			}
			done := make(chan struct{})
			x.busy[key] = done
			x.mu.Unlock()
			return func() {
				x.mu.Lock()
				delete(x.busy, key)
				x.mu.Unlock()
				close(done)
			}
		}
		x.mu.Unlock()
		<-held
	}
}

// find looks up user's thread. indexed is false if the forum hasn't been
// scanned.
func (x *modLogThreadIndex) find(forumID snowflake.ID, user discord.User) (threadID snowflake.ID, found, indexed bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	f := x.forums[forumID]
	if f == nil {
		return 0, false, false
	}
	threadID, found = f.find(user)
	return threadID, found, true
}

func (x *modLogThreadIndex) store(forumID snowflake.ID, f *modLogForum) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.forums == nil {
		x.forums = make(map[snowflake.ID]*modLogForum)
	}
	x.forums[forumID] = f
}

// update records a thread created or renamed in an indexed forum.
func (x *modLogThreadIndex) update(forumID, threadID snowflake.ID, name string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if f := x.forums[forumID]; f != nil {
		f.remove(threadID)
		f.add(threadID, name)
	}
}

// remove handles a deleted thread. If it was someone's thread, the forum is
// scanned again on next use, in case they have another one.
func (x *modLogThreadIndex) remove(forumID, threadID snowflake.ID) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if f := x.forums[forumID]; f != nil && f.remove(threadID) {
		delete(x.forums, forumID)
	}
}

// reset forgets a forum's index, so it is scanned again on next use.
func (x *modLogThreadIndex) reset(forumID snowflake.ID) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.forums, forumID)
}

func (b *Bot) onModLogThreadCreate(e *events.ThreadCreate) {
	if parentID := e.Thread.ParentID(); parentID != nil {
		b.modLogThreads.update(*parentID, e.ThreadID, e.Thread.Name())
	}
}

func (b *Bot) onModLogThreadUpdate(e *events.ThreadUpdate) {
	b.modLogThreads.update(e.ParentID, e.ThreadID, e.Thread.Name())
}

func (b *Bot) onModLogThreadDelete(e *events.ThreadDelete) {
	b.modLogThreads.remove(e.ParentID, e.ThreadID)
}

// modLogForum indexes one mod log forum's threads. When a user has more than
// one thread, the first one added wins.
type modLogForum struct {
	byUser map[snowflake.ID]snowflake.ID // user ID in the thread name -> thread ID
	legacy map[string]snowflake.ID       // exact name -> thread ID, for names with no user ID
}

func newModLogForum(threads []discord.GuildThread) *modLogForum {
	f := &modLogForum{byUser: map[snowflake.ID]snowflake.ID{}, legacy: map[string]snowflake.ID{}}
	for _, t := range threads {
		f.add(t.ID(), t.Name())
	}
	return f
}

func (f *modLogForum) add(threadID snowflake.ID, name string) {
	if userID := parseThreadUserID(name); userID != 0 {
		if _, ok := f.byUser[userID]; !ok {
			f.byUser[userID] = threadID
		}
		return
	}
	if _, ok := f.legacy[name]; !ok {
		f.legacy[name] = threadID
	}
}

// remove drops threadID, reporting whether it was indexed.
func (f *modLogForum) remove(threadID snowflake.ID) bool {
	for userID, id := range f.byUser {
		if id == threadID {
			delete(f.byUser, userID)
			return true
		}
	}
	for name, id := range f.legacy {
		if id == threadID {
			delete(f.legacy, name)
			return true
		}
	}
	return false
}

// find returns the thread whose name carries user's ID. Failing that, a legacy
// thread named exactly after their username will do.
func (f *modLogForum) find(user discord.User) (snowflake.ID, bool) {
	if threadID, ok := f.byUser[user.ID]; ok {
		return threadID, true
	}
	threadID, ok := f.legacy[user.Username]
	return threadID, ok
}

func messageLink(guildID, channelID, messageID snowflake.ID) string {
	return fmt.Sprintf("https://discord.com/channels/%d/%d/%d", guildID, channelID, messageID)
}

// --- /config modlog handlers ---

func (b *Bot) handleModLogConfig(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.modLog.get(*guildID)
	if st == nil {
		st = &modLogState{}
		b.modLog.set(*guildID, st)
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "set":
		b.handleModLogConfigSet(e, *guildID, st)
	case "show":
		b.handleModLogConfigShow(e, st)
	case "clear":
		b.handleModLogConfigClear(e, *guildID, st)
	case "audit":
		b.handleModLogAudit(e, *guildID, st)
	}
}

func (b *Bot) handleModLogConfigSet(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *modLogState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a channel.")
		return
	}
	if ch.Type != discord.ChannelTypeGuildForum {
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> isn't a forum channel. The mod log needs a forum, where each user gets their own post.", ch.ID))
		return
	}

	b.Log.Info("Modlog config set", "user_id", e.User().ID, "guild_id", guildID, "channel_id", ch.ID)

	st.mu.Lock()
	st.ChannelID = ch.ID
	st.mu.Unlock()

	if err := b.modLog.persist(guildID); err != nil {
		b.Log.Error("Failed to persist modlog config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Mod log forum channel set to <#%d>.", ch.ID))
}

func (b *Bot) handleModLogConfigShow(e *events.ApplicationCommandInteractionCreate, st *modLogState) {
	b.Log.Info("Modlog config show", "user_id", e.User().ID, "guild_id", *e.GuildID())

	st.mu.Lock()
	channelID := st.ChannelID
	st.mu.Unlock()

	if channelID == 0 {
		botutil.RespondEphemeral(e, "**Mod log channel:** Not set")
		return
	}
	botutil.RespondEphemeral(e, fmt.Sprintf("**Mod log channel:** <#%d>", channelID))
}

func (b *Bot) handleModLogConfigClear(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *modLogState) {
	b.Log.Info("Modlog config clear", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	st.ChannelID = 0
	st.mu.Unlock()

	if err := b.modLog.persist(guildID); err != nil {
		b.Log.Error("Failed to persist modlog config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Mod log disabled.")
}

var threadUserIDRegex = regexp.MustCompile(`- (\d+)$`)

// parseThreadUserID returns the user ID at the end of a mod log thread name
// ("username - 123"), or 0 if there is none.
func parseThreadUserID(name string) snowflake.ID {
	m := threadUserIDRegex.FindStringSubmatch(name)
	if len(m) < 2 {
		return 0
	}
	id, err := snowflake.Parse(m[1])
	if err != nil {
		return 0
	}
	return id
}

func (b *Bot) handleModLogAudit(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *modLogState) {
	b.Log.Info("Modlog audit", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	channelID := st.ChannelID
	st.mu.Unlock()

	if channelID == 0 {
		botutil.RespondEphemeral(e, "Mod log channel is not configured.")
		return
	}

	if err := e.DeferCreateMessage(true); err != nil {
		b.Log.Error("Failed to defer modlog audit response", "error", err)
		return
	}

	forum, err := b.modLogForumChannel(channelID)
	if errors.Is(err, errNotForum) {
		botutil.FollowupEphemeral(b.Client, e.Token(), "Mod log channel is not a forum channel.")
		return
	}
	if err != nil {
		b.Log.Error("Failed to get mod log channel for audit", "error", err)
		botutil.FollowupEphemeral(b.Client, e.Token(), "Failed to access the mod log channel.")
		return
	}

	threads, err := b.listModLogThreads(forum)
	if err != nil {
		b.Log.Error("Failed to list mod log threads for audit", "guild_id", guildID, "listed", len(threads), "error", err)
	}
	botutil.FollowupEphemeral(b.Client, e.Token(), modLogAuditReport(threads, err != nil))
}

// modLogAuditReport lists users with more than one mod log thread, and threads
// whose name has no user ID. incomplete means listing the threads failed
// partway, so the report covers only those listed before the failure.
func modLogAuditReport(threads []discord.GuildThread, incomplete bool) string {
	byUser := map[snowflake.ID][]discord.GuildThread{}
	var unrecognized []discord.GuildThread
	for _, t := range threads {
		if userID := parseThreadUserID(t.Name()); userID != 0 {
			byUser[userID] = append(byUser[userID], t)
		} else {
			unrecognized = append(unrecognized, t)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "**Mod log audit** — %d threads scanned\n", len(threads))
	if incomplete {
		sb.WriteString("\n⚠️ Listing the forum's threads failed partway, so this report may be incomplete. Try again later.\n")
	}

	hasDuplicates := false
	userIDs := make([]snowflake.ID, 0, len(byUser))
	for userID := range byUser {
		userIDs = append(userIDs, userID)
	}
	slices.Sort(userIDs)
	for _, userID := range userIDs {
		userThreads := byUser[userID]
		if len(userThreads) <= 1 {
			continue
		}
		hasDuplicates = true
		fmt.Fprintf(&sb, "\n**User ID %d** — %d threads:\n", userID, len(userThreads))
		for _, t := range userThreads {
			fmt.Fprintf(&sb, "- `%s` (<#%d>)\n", t.Name(), t.ID())
		}
	}

	if len(unrecognized) > 0 {
		sb.WriteString("\n**Unrecognized thread names** (no parseable user ID):\n")
		for _, t := range unrecognized {
			fmt.Fprintf(&sb, "- `%s` (<#%d>)\n", t.Name(), t.ID())
		}
	}

	if !hasDuplicates && len(unrecognized) == 0 && !incomplete {
		sb.WriteString("\nNo issues found.")
	}
	return truncateText(sb.String(), messageContentLimit)
}
