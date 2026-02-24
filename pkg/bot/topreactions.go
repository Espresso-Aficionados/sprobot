package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
)

// topReactionsStaticConfig identifies which guilds have the feature enabled.
type topReactionsStaticConfig struct{}

func getTopReactionsConfig(env string) map[snowflake.ID]topReactionsStaticConfig {
	switch env {
	case "prod":
		return map[snowflake.ID]topReactionsStaticConfig{
			726985544038612993: {},
		}
	case "dev":
		return map[snowflake.ID]topReactionsStaticConfig{
			1013566342345019512: {},
		}
	default:
		return nil
	}
}

type topReactionsSettings struct {
	OutputChannelID  snowflake.ID   `json:"output_channel_id"`
	WindowMinutes    int            `json:"window_minutes"`
	FrequencyMinutes int            `json:"frequency_minutes"`
	Count            int            `json:"count"`
	Blacklist        []snowflake.ID `json:"blacklist"`
}

type trackedMessage struct {
	ChannelID snowflake.ID `json:"channel_id"`
	AuthorID  snowflake.ID `json:"author_id"`
	Count     int          `json:"count"`
}

type topReactionsState struct {
	mu       sync.Mutex
	Settings topReactionsSettings            `json:"settings"`
	Messages map[snowflake.ID]trackedMessage `json:"messages"`
	LastPost time.Time                       `json:"last_post"`
}

const (
	defaultWindowMinutes    = 1440
	defaultFrequencyMinutes = 1440
	defaultTopCount         = 10
)

func intPtr(v int) *int { return &v }

// effectiveWindow returns the configured window or the default.
func (s *topReactionsSettings) effectiveWindow() int {
	if s.WindowMinutes > 0 {
		return s.WindowMinutes
	}
	return defaultWindowMinutes
}

// effectiveFrequency returns the configured frequency or the default.
func (s *topReactionsSettings) effectiveFrequency() int {
	if s.FrequencyMinutes > 0 {
		return s.FrequencyMinutes
	}
	return defaultFrequencyMinutes
}

// effectiveCount returns the configured count or the default.
func (s *topReactionsSettings) effectiveCount() int {
	if s.Count > 0 {
		return s.Count
	}
	return defaultTopCount
}

// isBlacklisted returns true if the channel is in the blacklist.
func (s *topReactionsSettings) isBlacklisted(channelID snowflake.ID) bool {
	for _, id := range s.Blacklist {
		if id == channelID {
			return true
		}
	}
	return false
}

// --- Load / Save / Persist ---

func (b *Bot) loadTopReactions() {
	if b.topReactionsConfig == nil {
		return
	}

	ctx := context.Background()
	for guildID := range b.topReactionsConfig {
		st := &topReactionsState{
			Messages: make(map[snowflake.ID]trackedMessage),
		}

		data, err := b.S3.FetchGuildJSON(ctx, "topreactions", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing top reactions data, starting fresh", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load top reactions data", "guild_id", guildID, "error", err)
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode top reactions data", "guild_id", guildID, "error", err)
			}
			if st.Messages == nil {
				st.Messages = make(map[snowflake.ID]trackedMessage)
			}
		}

		b.topReactions[guildID] = st
		b.Log.Info("Loaded top reactions state", "guild_id", guildID, "tracked", len(st.Messages))
	}
}

func (b *Bot) persistTopReactions(guildID snowflake.ID, st *topReactionsState) error {
	st.mu.Lock()
	data, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return b.S3.SaveGuildJSON(context.Background(), "topreactions", fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) saveTopReactions() {
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in top reactions save", "error", r)
		}
	}()

	for guildID, st := range b.topReactions {
		st.mu.Lock()
		pruneOldMessages(st, time.Now())
		st.mu.Unlock()

		if err := b.persistTopReactions(guildID, st); err != nil {
			b.Log.Error("Failed to save top reactions data", "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Saved top reactions state", "guild_id", guildID)
		}
	}
}

// pruneOldMessages removes messages older than the window. Must be called with mu held.
func pruneOldMessages(st *topReactionsState, now time.Time) {
	window := time.Duration(st.Settings.effectiveWindow()) * time.Minute
	cutoff := now.Add(-window)
	for msgID := range st.Messages {
		if msgID.Time().Before(cutoff) {
			delete(st.Messages, msgID)
		}
	}
}

// --- Reaction Event Handlers ---

func (b *Bot) onReactionAdd(e *events.GuildMessageReactionAdd) {
	if e.Member.User.Bot {
		return
	}

	st := b.topReactions[e.GuildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	if st.Settings.isBlacklisted(e.ChannelID) {
		return
	}

	tm, ok := st.Messages[e.MessageID]
	if !ok {
		var authorID snowflake.ID
		if e.MessageAuthorID != nil {
			authorID = *e.MessageAuthorID
		}
		tm = trackedMessage{
			ChannelID: e.ChannelID,
			AuthorID:  authorID,
		}
	}
	tm.Count++
	st.Messages[e.MessageID] = tm
}

func (b *Bot) onReactionRemove(e *events.GuildMessageReactionRemove) {
	st := b.topReactions[e.GuildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	tm, ok := st.Messages[e.MessageID]
	if !ok {
		return
	}
	tm.Count--
	if tm.Count <= 0 {
		delete(st.Messages, e.MessageID)
	} else {
		st.Messages[e.MessageID] = tm
	}
}

func (b *Bot) onReactionRemoveAll(e *events.GuildMessageReactionRemoveAll) {
	st := b.topReactions[e.GuildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	delete(st.Messages, e.MessageID)
	st.mu.Unlock()
}

func (b *Bot) onReactionRemoveEmoji(e *events.GuildMessageReactionRemoveEmoji) {
	// We track total count, not per-emoji. Time-window prune handles cleanup.
}

// --- Periodic Posting Loop ---

func (b *Bot) topReactionsLoop() {
	// Wait for ready
	readyTicker := time.NewTicker(1 * time.Second)
	defer readyTicker.Stop()
	for !b.Ready.Load() {
		select {
		case <-b.stop:
			return
		case <-readyTicker.C:
		}
	}

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-b.stop:
			return
		case <-ticker.C:
			b.checkTopReactionsPosts()
		}
	}
}

func (b *Bot) checkTopReactionsPosts() {
	now := time.Now()
	for guildID, st := range b.topReactions {
		st.mu.Lock()
		if st.Settings.OutputChannelID == 0 {
			st.mu.Unlock()
			continue
		}
		freq := time.Duration(st.Settings.effectiveFrequency()) * time.Minute
		if now.Sub(st.LastPost) < freq {
			st.mu.Unlock()
			continue
		}
		st.mu.Unlock()

		b.postTopReactions(guildID, st, now)
	}
}

type rankedMessage struct {
	MessageID snowflake.ID
	ChannelID snowflake.ID
	AuthorID  snowflake.ID
	Count     int
}

func (b *Bot) postTopReactions(guildID snowflake.ID, st *topReactionsState, now time.Time) {
	st.mu.Lock()
	pruneOldMessages(st, now)

	// Collect candidates
	candidates := make([]rankedMessage, 0, len(st.Messages))
	for msgID, tm := range st.Messages {
		candidates = append(candidates, rankedMessage{
			MessageID: msgID,
			ChannelID: tm.ChannelID,
			AuthorID:  tm.AuthorID,
			Count:     tm.Count,
		})
	}
	outputChannel := st.Settings.OutputChannelID
	count := st.Settings.effectiveCount()
	st.LastPost = now
	st.mu.Unlock()

	if len(candidates) == 0 {
		if err := b.persistTopReactions(guildID, st); err != nil {
			b.Log.Error("Failed to persist top reactions after empty post", "guild_id", guildID, "error", err)
		}
		return
	}

	// Sort by count descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Count > candidates[j].Count
	})

	if len(candidates) > count {
		candidates = candidates[:count]
	}

	guildStr := fmt.Sprintf("%d", guildID)
	var lines []string
	for rank, c := range candidates {
		link := messageLink(guildStr, fmt.Sprintf("%d", c.ChannelID), fmt.Sprintf("%d", c.MessageID))
		lines = append(lines, fmt.Sprintf("%d. [Jump to message](%s) by <@%d> in <#%d> — %d reactions",
			rank+1, link, c.AuthorID, c.ChannelID, c.Count))
	}

	embed := discord.Embed{
		Title:       "Top Reactions",
		Description: strings.Join(lines, "\n"),
		Color:       colorTeal,
		Timestamp:   &now,
	}

	_, err := b.Client.Rest.CreateMessage(outputChannel, discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	})
	if err != nil {
		b.Log.Error("Failed to post top reactions", "guild_id", guildID, "error", err)
	}

	if err := b.persistTopReactions(guildID, st); err != nil {
		b.Log.Error("Failed to persist top reactions after post", "guild_id", guildID, "error", err)
	}
}

// --- Command Handlers ---

func (b *Bot) handleTopReactionsConfig(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.topReactions[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Top reactions is not configured for this server.")
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "set":
		b.handleTRConfigSet(e, st)
	case "show":
		b.handleTRConfigShow(e, st)
	case "disable":
		b.handleTRConfigDisable(e, *guildID, st)
	}
}

func (b *Bot) handleTRConfigSet(e *events.ApplicationCommandInteractionCreate, st *topReactionsState) {
	data := e.Data.(discord.SlashCommandInteractionData)
	guildID := *e.GuildID()

	st.mu.Lock()
	if ch, ok := data.OptChannel("channel"); ok {
		st.Settings.OutputChannelID = ch.ID
	}
	if v, ok := data.OptInt("window"); ok {
		st.Settings.WindowMinutes = v * 60
	}
	if v, ok := data.OptInt("frequency"); ok {
		st.Settings.FrequencyMinutes = v * 60
	}
	if v, ok := data.OptInt("count"); ok {
		st.Settings.Count = v
	}
	st.mu.Unlock()

	if err := b.persistTopReactions(guildID, st); err != nil {
		b.Log.Error("Failed to persist top reactions config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Top reactions configuration updated.")
}

func (b *Bot) handleTRConfigShow(e *events.ApplicationCommandInteractionCreate, st *topReactionsState) {
	st.mu.Lock()
	s := st.Settings
	tracked := len(st.Messages)
	lastPost := st.LastPost
	st.mu.Unlock()

	var channelStr string
	if s.OutputChannelID == 0 {
		channelStr = "Not set (disabled)"
	} else {
		channelStr = fmt.Sprintf("<#%d>", s.OutputChannelID)
	}

	var lastPostStr string
	if lastPost.IsZero() {
		lastPostStr = "Never"
	} else {
		lastPostStr = fmt.Sprintf("<t:%d:R>", lastPost.Unix())
	}

	lines := []string{
		fmt.Sprintf("**Output Channel:** %s", channelStr),
		fmt.Sprintf("**Window:** %d hours", s.effectiveWindow()/60),
		fmt.Sprintf("**Frequency:** %d hours", s.effectiveFrequency()/60),
		fmt.Sprintf("**Count:** %d", s.effectiveCount()),
		fmt.Sprintf("**Tracking:** %d messages", tracked),
		fmt.Sprintf("**Last Post:** %s", lastPostStr),
	}

	botutil.RespondEphemeral(e, strings.Join(lines, "\n"))
}

func (b *Bot) handleTRConfigDisable(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *topReactionsState) {
	st.mu.Lock()
	st.Settings.OutputChannelID = 0
	st.mu.Unlock()

	if err := b.persistTopReactions(guildID, st); err != nil {
		b.Log.Error("Failed to persist top reactions config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Top reactions posting disabled. Tracking continues; settings preserved.")
}

func (b *Bot) handleTopReactionsBlacklist(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.topReactions[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Top reactions is not configured for this server.")
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "add":
		b.handleTRBlacklistAdd(e, *guildID, st)
	case "remove":
		b.handleTRBlacklistRemove(e, *guildID, st)
	case "list":
		b.handleTRBlacklistList(e, st)
	}
}

func (b *Bot) handleTRBlacklistAdd(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *topReactionsState) {
	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a channel.")
		return
	}

	st.mu.Lock()
	if st.Settings.isBlacklisted(ch.ID) {
		st.mu.Unlock()
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> is already blacklisted.", ch.ID))
		return
	}
	st.Settings.Blacklist = append(st.Settings.Blacklist, ch.ID)
	st.mu.Unlock()

	if err := b.persistTopReactions(guildID, st); err != nil {
		b.Log.Error("Failed to persist top reactions blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> added to blacklist.", ch.ID))
}

func (b *Bot) handleTRBlacklistRemove(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *topReactionsState) {
	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a channel.")
		return
	}

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

	if err := b.persistTopReactions(guildID, st); err != nil {
		b.Log.Error("Failed to persist top reactions blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> removed from blacklist.", ch.ID))
}

func (b *Bot) handleTRBlacklistList(e *events.ApplicationCommandInteractionCreate, st *topReactionsState) {
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
