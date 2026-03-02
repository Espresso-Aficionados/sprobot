package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

type posterRoleSettings struct {
	RoleID       snowflake.ID   `json:"role_id"`
	Threshold    int            `json:"threshold"`
	SkipChannels []snowflake.ID `json:"skip_channels"`
}

func (s *posterRoleSettings) isSkipped(ids ...snowflake.ID) bool {
	for _, id := range ids {
		for _, skip := range s.SkipChannels {
			if id == skip {
				return true
			}
		}
	}
	return false
}

type posterRoleState struct {
	mu       sync.Mutex
	Settings posterRoleSettings `json:"settings"`
	Counts   map[string]int     `json:"counts"`
	Fetched  map[string]bool    `json:"fetched"`
}

func (b *Bot) checkPosterRole(guildID snowflake.ID, channelID snowflake.ID, ch discord.GuildMessageChannel, msg discord.Message) {
	st := b.posterRole[guildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	cfg := st.Settings
	st.mu.Unlock()

	if cfg.RoleID == 0 || cfg.Threshold == 0 {
		return
	}

	if cfg.isSkipped(channelID) {
		return
	}
	if thread, ok := ch.(discord.GuildThread); ok {
		if parentID := thread.ParentID(); parentID != nil && cfg.isSkipped(*parentID) {
			return
		}
	}

	if msg.Member == nil {
		return
	}
	for _, roleID := range msg.Member.RoleIDs {
		if roleID == cfg.RoleID {
			return
		}
	}

	userID := msg.Author.ID
	userIDStr := fmt.Sprintf("%d", userID)

	st.mu.Lock()
	st.Counts[userIDStr]++
	fetched := st.Fetched[userIDStr]
	st.mu.Unlock()

	if !fetched {
		b.fetchPosterRoleHistory(guildID, userID, userIDStr)
	}

	st.mu.Lock()
	count := st.Counts[userIDStr]
	fetched = st.Fetched[userIDStr]
	st.mu.Unlock()

	if fetched && count >= cfg.Threshold {
		if err := b.Client.Rest.AddMemberRole(guildID, userID, cfg.RoleID, rest.WithReason("Reached marketplace post threshold")); err != nil {
			b.Log.Error("Failed to grant poster role", "user_id", userID, "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Granted poster role", "user_id", userID, "guild_id", guildID, "total", count)
			b.clearPosterRoleTracking(guildID, userIDStr)
		}
	}
}

type discordSearchResponse struct {
	TotalResults int `json:"total_results"`
}

func (b *Bot) fetchPosterRoleHistory(guildID snowflake.ID, userID snowflake.ID, userIDStr string) {
	url := fmt.Sprintf("https://discord.com/api/v10/guilds/%d/messages/search?author_id=%d", guildID, userID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		b.Log.Error("Failed to create search request", "user_id", userID, "error", err)
		return
	}
	req.Header.Set("Authorization", "Bot "+b.Client.Token)

	resp, err := b.searchClient.Do(req)
	if err != nil {
		b.Log.Error("Failed to execute search request", "user_id", userID, "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		b.Log.Info("Search index not ready, will retry on next message", "user_id", userID)
		return
	}
	if resp.StatusCode != http.StatusOK {
		b.Log.Error("Search API returned non-200", "user_id", userID, "status", resp.StatusCode)
		return
	}

	var result discordSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		b.Log.Error("Failed to decode search response", "user_id", userID, "error", err)
		return
	}

	st := b.posterRole[guildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	st.Counts[userIDStr] += result.TotalResults
	st.Fetched[userIDStr] = true
	st.mu.Unlock()

	b.Log.Info("Cached historical post count", "user_id", userID, "guild_id", guildID, "count", result.TotalResults)
}

func (b *Bot) clearPosterRoleTracking(guildID snowflake.ID, userIDStr string) {
	st := b.posterRole[guildID]
	if st == nil {
		return
	}
	st.mu.Lock()
	delete(st.Counts, userIDStr)
	delete(st.Fetched, userIDStr)
	st.mu.Unlock()
}

func (b *Bot) loadPosterRole() {
	templates := sprobot.AllTemplates(b.Env)
	if templates == nil {
		return
	}

	ctx := context.Background()
	for guildID := range templates {
		st := &posterRoleState{
			Counts:  make(map[string]int),
			Fetched: make(map[string]bool),
		}

		data, err := b.S3.FetchGuildJSON(ctx, "posterroles", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing poster role data, starting fresh", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load poster role data", "guild_id", guildID, "error", err)
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode poster role data", "guild_id", guildID, "error", err)
			}
			if st.Counts == nil {
				st.Counts = make(map[string]int)
			}
			if st.Fetched == nil {
				st.Fetched = make(map[string]bool)
			}
		}

		b.posterRole[guildID] = st
		b.Log.Info("Loaded poster role state", "guild_id", guildID, "users", len(st.Counts))
	}
}

func (b *Bot) persistPosterRole(guildID snowflake.ID, st *posterRoleState) error {
	st.mu.Lock()
	data, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return b.S3.SaveGuildJSON(context.Background(), "posterroles", fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) savePosterRole() {
	for guildID, st := range b.posterRole {
		if err := b.persistPosterRole(guildID, st); err != nil {
			b.Log.Error("Failed to save poster role data", "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Saved poster role state", "guild_id", guildID)
		}
	}
}

func (b *Bot) handleMarketProgress(e *events.ApplicationCommandInteractionCreate) {
	if e.GuildID() == nil {
		return
	}
	guildID := *e.GuildID()

	st := b.posterRole[guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Poster role is not configured for this server.")
		return
	}

	st.mu.Lock()
	cfg := st.Settings
	st.mu.Unlock()

	if cfg.RoleID == 0 || cfg.Threshold == 0 {
		botutil.RespondEphemeral(e, "Poster role is not configured. Use `/marketconfig set` first.")
		return
	}

	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	user, ok := data.OptUser("user")
	if !ok {
		botutil.RespondEphemeral(e, "Please specify a user.")
		return
	}

	public, _ := data.OptBool("public")
	ephemeral := !public

	b.Log.Info("Market progress", "user_id", e.User().ID, "guild_id", guildID, "target_user", user.ID, "public", public)

	// Defer since GetMember (for the specified user) is a network call.
	if err := e.DeferCreateMessage(ephemeral); err != nil {
		b.Log.Error("Failed to defer marketprogress response", "error", err)
		return
	}

	userIDStr := fmt.Sprintf("%d", user.ID)
	mention := fmt.Sprintf("<@%d>", user.ID)

	// Check if user already has the role
	member, err := b.Client.Rest.GetMember(guildID, user.ID)
	if err == nil {
		for _, roleID := range member.RoleIDs {
			if roleID == cfg.RoleID {
				msg := discord.MessageCreate{
					Content: fmt.Sprintf("%s already has access to the marketplace.", mention),
				}
				if ephemeral {
					msg.Flags = discord.MessageFlagEphemeral
				}
				b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), msg)
				return
			}
		}
	}

	st.mu.Lock()
	count := st.Counts[userIDStr]
	st.mu.Unlock()

	pct := 0
	if cfg.Threshold > 0 {
		pct = count * 100 / cfg.Threshold
	}
	remaining := cfg.Threshold - count
	if remaining < 0 {
		remaining = 0
	}

	content := fmt.Sprintf("Poster role progress for %s:\n- Posts: %d / %d (%d%%)\n- %d more posts needed",
		mention, count, cfg.Threshold, pct, remaining)

	msg := discord.MessageCreate{
		Content: content,
	}
	if ephemeral {
		msg.Flags = discord.MessageFlagEphemeral
	}
	b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), msg)
}

type leaderboardEntry struct {
	UserID string
	Total  int
}

func (b *Bot) handleMarketLeaderboard(e *events.ApplicationCommandInteractionCreate) {
	if e.GuildID() == nil {
		return
	}
	guildID := *e.GuildID()

	st := b.posterRole[guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Poster role is not configured for this server.")
		return
	}

	st.mu.Lock()
	cfg := st.Settings
	st.mu.Unlock()

	if cfg.RoleID == 0 || cfg.Threshold == 0 {
		botutil.RespondEphemeral(e, "Poster role is not configured. Use `/marketconfig set` first.")
		return
	}

	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	public, _ := data.OptBool("public")
	ephemeral := !public

	b.Log.Info("Market leaderboard", "user_id", e.User().ID, "guild_id", guildID, "public", public)

	if err := e.DeferCreateMessage(ephemeral); err != nil {
		b.Log.Error("Failed to defer marketleaderboard response", "error", err)
		return
	}

	// Snapshot counts under lock
	st.mu.Lock()
	entries := make([]leaderboardEntry, 0, len(st.Counts))
	for uid, total := range st.Counts {
		entries = append(entries, leaderboardEntry{UserID: uid, Total: total})
	}
	st.mu.Unlock()

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Total > entries[j].Total
	})

	// Users who have been granted the role are removed from tracking data,
	// so the leaderboard only contains users still working toward the threshold.
	if len(entries) > 20 {
		entries = entries[:20]
	}

	// Build embed
	var lines []string
	for rank, entry := range entries {
		pct := 0
		if cfg.Threshold > 0 {
			pct = entry.Total * 100 / cfg.Threshold
		}
		remaining := cfg.Threshold - entry.Total
		if remaining < 0 {
			remaining = 0
		}
		lines = append(lines, fmt.Sprintf("%d. <@%s> — %d/%d (%d%%) — %d remaining",
			rank+1, entry.UserID, entry.Total, cfg.Threshold, pct, remaining))
	}

	description := "No users are being tracked yet."
	if len(lines) > 0 {
		description = strings.Join(lines, "\n")
	}

	embed := discord.Embed{
		Title:       "Marketplace Progress Leaderboard",
		Description: description,
		Footer: &discord.EmbedFooter{
			Text: "Only includes users who have sent a message since tracking began. Counts reflect their full post history.",
		},
	}

	msg := discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	}
	if ephemeral {
		msg.Flags = discord.MessageFlagEphemeral
	}
	b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), msg)
}

// --- /marketconfig handlers ---

func (b *Bot) handleMarketConfig(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.posterRole[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Marketplace feature is not available for this server.")
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "set":
		b.handleMarketConfigSet(e, *guildID, st)
	case "show":
		b.handleMarketConfigShow(e, st)
	}
}

func (b *Bot) handleMarketConfigSet(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *posterRoleState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	b.Log.Info("Market config set", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	if role, ok := data.OptRole("role"); ok {
		st.Settings.RoleID = role.ID
	}
	if v, ok := data.OptInt("threshold"); ok {
		st.Settings.Threshold = v
	}
	st.mu.Unlock()

	if err := b.persistPosterRole(guildID, st); err != nil {
		b.Log.Error("Failed to persist market config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Marketplace configuration updated.")
}

func (b *Bot) handleMarketConfigShow(e *events.ApplicationCommandInteractionCreate, st *posterRoleState) {
	b.Log.Info("Market config show", "user_id", e.User().ID, "guild_id", *e.GuildID())

	st.mu.Lock()
	s := st.Settings
	st.mu.Unlock()

	var roleStr string
	if s.RoleID == 0 {
		roleStr = "Not set"
	} else {
		roleStr = fmt.Sprintf("<@&%d>", s.RoleID)
	}

	var thresholdStr string
	if s.Threshold == 0 {
		thresholdStr = "Not set"
	} else {
		thresholdStr = fmt.Sprintf("%d", s.Threshold)
	}

	lines := []string{
		fmt.Sprintf("**Role:** %s", roleStr),
		fmt.Sprintf("**Threshold:** %s", thresholdStr),
		fmt.Sprintf("**Blacklisted Channels:** %d", len(s.SkipChannels)),
	}

	botutil.RespondEphemeral(e, strings.Join(lines, "\n"))
}

// --- /marketblacklist handlers ---

func (b *Bot) handleMarketBlacklist(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.posterRole[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Marketplace feature is not available for this server.")
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "add":
		b.handleMarketBlacklistAdd(e, *guildID, st)
	case "remove":
		b.handleMarketBlacklistRemove(e, *guildID, st)
	case "list":
		b.handleMarketBlacklistList(e, st)
	case "clear":
		b.handleMarketBlacklistClear(e, *guildID, st)
	}
}

func (b *Bot) handleMarketBlacklistAdd(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *posterRoleState) {
	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a channel.")
		return
	}

	b.Log.Info("Market blacklist add", "user_id", e.User().ID, "guild_id", guildID, "channel_id", ch.ID)

	st.mu.Lock()
	if st.Settings.isSkipped(ch.ID) {
		st.mu.Unlock()
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> is already blacklisted.", ch.ID))
		return
	}
	st.Settings.SkipChannels = append(st.Settings.SkipChannels, ch.ID)
	st.mu.Unlock()

	if err := b.persistPosterRole(guildID, st); err != nil {
		b.Log.Error("Failed to persist market blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> added to blacklist.", ch.ID))
}

func (b *Bot) handleMarketBlacklistRemove(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *posterRoleState) {
	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a channel.")
		return
	}

	b.Log.Info("Market blacklist remove", "user_id", e.User().ID, "guild_id", guildID, "channel_id", ch.ID)

	st.mu.Lock()
	found := false
	for i, id := range st.Settings.SkipChannels {
		if id == ch.ID {
			st.Settings.SkipChannels = append(st.Settings.SkipChannels[:i], st.Settings.SkipChannels[i+1:]...)
			found = true
			break
		}
	}
	st.mu.Unlock()

	if !found {
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> is not blacklisted.", ch.ID))
		return
	}

	if err := b.persistPosterRole(guildID, st); err != nil {
		b.Log.Error("Failed to persist market blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> removed from blacklist.", ch.ID))
}

func (b *Bot) handleMarketBlacklistList(e *events.ApplicationCommandInteractionCreate, st *posterRoleState) {
	b.Log.Info("Market blacklist list", "user_id", e.User().ID, "guild_id", *e.GuildID())

	st.mu.Lock()
	bl := make([]snowflake.ID, len(st.Settings.SkipChannels))
	copy(bl, st.Settings.SkipChannels)
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

func (b *Bot) handleMarketBlacklistClear(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *posterRoleState) {
	b.Log.Info("Market blacklist clear", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	count := len(st.Settings.SkipChannels)
	st.Settings.SkipChannels = nil
	st.mu.Unlock()

	if count == 0 {
		botutil.RespondEphemeral(e, "Blacklist is already empty.")
		return
	}

	if err := b.persistPosterRole(guildID, st); err != nil {
		b.Log.Error("Failed to persist market blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Cleared %d entries from the blacklist.", count))
}
