package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
)

// starboardStaticConfig identifies which guilds have the feature enabled.
type starboardStaticConfig struct{}

func getStarboardConfig(env string) map[snowflake.ID]starboardStaticConfig {
	switch env {
	case "prod":
		return map[snowflake.ID]starboardStaticConfig{
			726985544038612993: {},
		}
	case "dev":
		return map[snowflake.ID]starboardStaticConfig{
			1013566342345019512: {},
		}
	default:
		return nil
	}
}

type starboardSettings struct {
	OutputChannelID snowflake.ID   `json:"output_channel_id"`
	Emoji           string         `json:"emoji"`
	Threshold       int            `json:"threshold"`
	Blacklist       []snowflake.ID `json:"blacklist"`
}

const defaultThreshold = 5

func (s *starboardSettings) effectiveThreshold() int {
	if s.Threshold > 0 {
		return s.Threshold
	}
	return defaultThreshold
}

func (s *starboardSettings) isBlacklisted(ids ...snowflake.ID) bool {
	for _, id := range ids {
		for _, blID := range s.Blacklist {
			if id == blID {
				return true
			}
		}
	}
	return false
}

type starboardEntry struct {
	ChannelID      snowflake.ID `json:"channel_id"`
	AuthorID       snowflake.ID `json:"author_id"`
	Count          int          `json:"count"`
	StarboardMsgID snowflake.ID `json:"starboard_msg_id"`
}

type starboardState struct {
	mu       sync.Mutex
	Settings starboardSettings               `json:"settings"`
	Entries  map[snowflake.ID]starboardEntry `json:"entries"` // keyed by source message ID
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

// --- Load / Save / Persist ---

func (b *Bot) loadStarboard() {
	if b.starboardConfig == nil {
		return
	}

	ctx := context.Background()
	for guildID := range b.starboardConfig {
		st := &starboardState{
			Entries: make(map[snowflake.ID]starboardEntry),
		}

		data, err := b.S3.FetchGuildJSON(ctx, "starboard", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing starboard data, starting fresh", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load starboard data", "guild_id", guildID, "error", err)
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode starboard data", "guild_id", guildID, "error", err)
			}
			if st.Entries == nil {
				st.Entries = make(map[snowflake.ID]starboardEntry)
			}
		}

		b.starboard[guildID] = st
		b.Log.Info("Loaded starboard state", "guild_id", guildID, "entries", len(st.Entries))
	}
}

func (b *Bot) persistStarboard(guildID snowflake.ID, st *starboardState) error {
	st.mu.Lock()
	data, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return b.S3.SaveGuildJSON(context.Background(), "starboard", fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) saveStarboard() {
	for guildID, st := range b.starboard {
		st.mu.Lock()
		pruneUnpostedEntries(st, time.Now())
		st.mu.Unlock()

		if err := b.persistStarboard(guildID, st); err != nil {
			b.Log.Error("Failed to save starboard data", "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Saved starboard state", "guild_id", guildID)
		}
	}
}

// pruneUnpostedEntries removes unposted entries older than 30 days. Must be called with mu held.
func pruneUnpostedEntries(st *starboardState, now time.Time) {
	cutoff := now.Add(-30 * 24 * time.Hour)
	for msgID, entry := range st.Entries {
		if entry.StarboardMsgID == 0 && msgID.Time().Before(cutoff) {
			delete(st.Entries, msgID)
		}
	}
}

// --- Reaction Event Handlers ---

func (b *Bot) onReactionAdd(e *events.GuildMessageReactionAdd) {
	if e.Member.User.Bot {
		return
	}

	st := b.starboard[e.GuildID]
	if st == nil {
		return
	}

	// Resolve the channel's parent chain for blacklist checks.
	// For threads: channel → parent channel → category.
	// For regular channels: channel → category.
	blacklistIDs := []snowflake.ID{e.ChannelID}
	if ch, err := b.Client.Rest.GetChannel(e.ChannelID); err == nil {
		if gc, ok := ch.(discord.GuildChannel); ok {
			if parentID := gc.ParentID(); parentID != nil {
				blacklistIDs = append(blacklistIDs, *parentID)
				// If this was a thread, the parent is a channel — resolve its category too.
				if _, ok := gc.(discord.GuildThread); ok {
					if parent, err := b.Client.Rest.GetChannel(*parentID); err == nil {
						if pgc, ok := parent.(discord.GuildChannel); ok {
							if catID := pgc.ParentID(); catID != nil {
								blacklistIDs = append(blacklistIDs, *catID)
							}
						}
					}
				}
			}
		}
	}

	st.mu.Lock()

	if st.Settings.OutputChannelID == 0 || st.Settings.Emoji == "" {
		st.mu.Unlock()
		return
	}

	if e.Emoji.Reaction() != st.Settings.Emoji {
		st.mu.Unlock()
		return
	}

	if e.ChannelID == st.Settings.OutputChannelID {
		st.mu.Unlock()
		return
	}

	if st.Settings.isBlacklisted(blacklistIDs...) {
		st.mu.Unlock()
		return
	}

	entry, ok := st.Entries[e.MessageID]
	if !ok {
		var authorID snowflake.ID
		if e.MessageAuthorID != nil {
			authorID = *e.MessageAuthorID
		}
		entry = starboardEntry{
			ChannelID: e.ChannelID,
			AuthorID:  authorID,
		}
	}
	entry.Count++
	st.Entries[e.MessageID] = entry

	threshold := st.Settings.effectiveThreshold()
	shouldPost := entry.Count >= threshold && entry.StarboardMsgID == 0
	shouldUpdate := entry.StarboardMsgID != 0

	st.mu.Unlock()

	if shouldPost {
		b.postStarboardEntry(e.GuildID, e.MessageID, st)
	} else if shouldUpdate {
		b.updateStarboardEntry(e.GuildID, e.MessageID, st)
	}
}

func (b *Bot) onReactionRemove(e *events.GuildMessageReactionRemove) {
	st := b.starboard[e.GuildID]
	if st == nil {
		return
	}

	st.mu.Lock()

	if st.Settings.Emoji == "" || e.Emoji.Reaction() != st.Settings.Emoji {
		st.mu.Unlock()
		return
	}

	entry, ok := st.Entries[e.MessageID]
	if !ok {
		st.mu.Unlock()
		return
	}

	entry.Count--
	if entry.Count < 0 {
		entry.Count = 0
	}
	st.Entries[e.MessageID] = entry

	shouldUpdate := entry.StarboardMsgID != 0

	st.mu.Unlock()

	if shouldUpdate {
		b.updateStarboardEntry(e.GuildID, e.MessageID, st)
	}
}

func (b *Bot) onReactionRemoveAll(e *events.GuildMessageReactionRemoveAll) {
	st := b.starboard[e.GuildID]
	if st == nil {
		return
	}

	st.mu.Lock()

	entry, ok := st.Entries[e.MessageID]
	if !ok {
		st.mu.Unlock()
		return
	}

	entry.Count = 0
	st.Entries[e.MessageID] = entry

	shouldUpdate := entry.StarboardMsgID != 0

	st.mu.Unlock()

	if shouldUpdate {
		b.updateStarboardEntry(e.GuildID, e.MessageID, st)
	}
}

func (b *Bot) onReactionRemoveEmoji(e *events.GuildMessageReactionRemoveEmoji) {
	st := b.starboard[e.GuildID]
	if st == nil {
		return
	}

	st.mu.Lock()

	if st.Settings.Emoji == "" || e.Emoji.Reaction() != st.Settings.Emoji {
		st.mu.Unlock()
		return
	}

	entry, ok := st.Entries[e.MessageID]
	if !ok {
		st.mu.Unlock()
		return
	}

	entry.Count = 0
	st.Entries[e.MessageID] = entry

	shouldUpdate := entry.StarboardMsgID != 0

	st.mu.Unlock()

	if shouldUpdate {
		b.updateStarboardEntry(e.GuildID, e.MessageID, st)
	}
}

// --- Starboard Posting ---

func (b *Bot) postStarboardEntry(guildID, msgID snowflake.ID, st *starboardState) {
	st.mu.Lock()
	entry := st.Entries[msgID]
	settings := st.Settings
	st.mu.Unlock()

	msg, err := b.Client.Rest.GetMessage(entry.ChannelID, msgID)
	if err != nil {
		b.Log.Error("Failed to fetch message for starboard", "guild_id", guildID, "message_id", msgID, "error", err)
		return
	}

	link := messageLink(guildID, entry.ChannelID, msgID)

	content := fmt.Sprintf("%s **%d** | <#%d>", emojiDisplay(settings.Emoji), entry.Count, entry.ChannelID)

	description := msg.Content
	if len(description) > 2000 {
		description = description[:2000] + "..."
	}
	description += fmt.Sprintf("\n\n[Jump to message](%s)", link)

	embed := discord.Embed{
		Author: &discord.EmbedAuthor{
			Name:    msg.Author.EffectiveName(),
			IconURL: msg.Author.EffectiveAvatarURL(),
		},
		Description: description,
		Color:       colorTeal,
		Timestamp:   &msg.CreatedAt,
	}

	// Attach the first image if present
	for _, att := range msg.Attachments {
		if att.ContentType != nil && strings.HasPrefix(*att.ContentType, "image/") {
			embed.Image = &discord.EmbedResource{URL: att.URL}
			break
		}
	}

	sent, err := b.Client.Rest.CreateMessage(settings.OutputChannelID, discord.MessageCreate{
		Content: content,
		Embeds:  []discord.Embed{embed},
	})
	if err != nil {
		b.Log.Error("Failed to post starboard entry", "guild_id", guildID, "message_id", msgID, "error", err)
		return
	}

	st.mu.Lock()
	entry = st.Entries[msgID]
	entry.StarboardMsgID = sent.ID
	st.Entries[msgID] = entry
	st.mu.Unlock()

	if err := b.persistStarboard(guildID, st); err != nil {
		b.Log.Error("Failed to persist starboard after post", "guild_id", guildID, "error", err)
	}
}

func (b *Bot) updateStarboardEntry(guildID, msgID snowflake.ID, st *starboardState) {
	st.mu.Lock()
	entry := st.Entries[msgID]
	settings := st.Settings
	st.mu.Unlock()

	content := fmt.Sprintf("%s **%d** | <#%d>", emojiDisplay(settings.Emoji), entry.Count, entry.ChannelID)

	_, err := b.Client.Rest.UpdateMessage(settings.OutputChannelID, entry.StarboardMsgID, discord.MessageUpdate{
		Content: &content,
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

	st := b.starboard[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Starboard is not configured for this server.")
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "set":
		b.handleSBConfigSet(e, *guildID, st)
	case "show":
		b.handleSBConfigShow(e, st)
	case "disable":
		b.handleSBConfigDisable(e, *guildID, st)
	}
}

func (b *Bot) handleSBConfigSet(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *starboardState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	b.Log.Info("Starboard config set", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	if ch, ok := data.OptChannel("channel"); ok {
		st.Settings.OutputChannelID = ch.ID
	}
	if v, ok := data.OptString("emoji"); ok {
		newEmoji := parseEmojiInput(v)
		if newEmoji != st.Settings.Emoji {
			// Emoji changed — clear all unposted entries
			for id, entry := range st.Entries {
				if entry.StarboardMsgID == 0 {
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

	if err := b.persistStarboard(guildID, st); err != nil {
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

	if err := b.persistStarboard(guildID, st); err != nil {
		b.Log.Error("Failed to persist starboard config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Starboard disabled. Settings preserved.")
}

func (b *Bot) handleStarboardBlacklist(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.starboard[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Starboard is not configured for this server.")
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "add":
		b.handleSBBlacklistAdd(e, *guildID, st)
	case "remove":
		b.handleSBBlacklistRemove(e, *guildID, st)
	case "list":
		b.handleSBBlacklistList(e, st)
	}
}

func (b *Bot) handleSBBlacklistAdd(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *starboardState) {
	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a channel.")
		return
	}

	b.Log.Info("Starboard blacklist add", "user_id", e.User().ID, "guild_id", guildID, "channel_id", ch.ID)

	st.mu.Lock()
	if st.Settings.isBlacklisted(ch.ID) {
		st.mu.Unlock()
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> is already blacklisted.", ch.ID))
		return
	}
	st.Settings.Blacklist = append(st.Settings.Blacklist, ch.ID)
	st.mu.Unlock()

	if err := b.persistStarboard(guildID, st); err != nil {
		b.Log.Error("Failed to persist starboard blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> added to blacklist.", ch.ID))
}

func (b *Bot) handleSBBlacklistRemove(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *starboardState) {
	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a channel.")
		return
	}

	b.Log.Info("Starboard blacklist remove", "user_id", e.User().ID, "guild_id", guildID, "channel_id", ch.ID)

	st.mu.Lock()
	found := false
	for i, id := range st.Settings.Blacklist {
		if id == ch.ID {
			st.Settings.Blacklist = append(st.Settings.Blacklist[:i], st.Settings.Blacklist[i+1:]...)
			found = true
			break
		}
	}
	st.mu.Unlock()

	if !found {
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> is not blacklisted.", ch.ID))
		return
	}

	if err := b.persistStarboard(guildID, st); err != nil {
		b.Log.Error("Failed to persist starboard blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> removed from blacklist.", ch.ID))
}

func (b *Bot) handleSBBlacklistList(e *events.ApplicationCommandInteractionCreate, st *starboardState) {
	b.Log.Info("Starboard blacklist list", "user_id", e.User().ID, "guild_id", *e.GuildID())

	st.mu.Lock()
	bl := make([]snowflake.ID, len(st.Settings.Blacklist))
	copy(bl, st.Settings.Blacklist)
	st.mu.Unlock()

	if len(bl) == 0 {
		botutil.RespondEphemeral(e, "No channels are blacklisted.")
		return
	}

	var lines []string
	for _, id := range bl {
		lines = append(lines, fmt.Sprintf("<#%d>", id))
	}
	botutil.RespondEphemeral(e, "**Blacklisted channels:**\n"+strings.Join(lines, "\n"))
}
