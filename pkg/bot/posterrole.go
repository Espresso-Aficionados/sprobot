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

	parentCache sync.Map // channelID → channelParents
}

func (b *Bot) checkPosterRole(guildID snowflake.ID, channelID snowflake.ID, msg discord.Message) {
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

	if cfg.isSkipped(b.resolveChannelParents(channelID, &st.parentCache)...) {
		return
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

type searchHitMessage struct {
	ChannelID snowflake.ID `json:"channel_id"`
}

type discordSearchResponse struct {
	TotalResults int                  `json:"total_results"`
	Messages     [][]searchHitMessage `json:"messages"`
}

type channelCount struct {
	ChannelID snowflake.ID
	Count     int
}

// topChannels returns the top n channels by message count from a search response.
func topChannels(resp *discordSearchResponse, n int) []channelCount {
	counts := make(map[snowflake.ID]int)
	for _, hits := range resp.Messages {
		if len(hits) > 0 {
			counts[hits[0].ChannelID]++
		}
	}

	result := make([]channelCount, 0, len(counts))
	for id, c := range counts {
		result = append(result, channelCount{ChannelID: id, Count: c})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].ChannelID < result[j].ChannelID
	})

	if len(result) > n {
		result = result[:n]
	}
	return result
}

// filteredSearchCount estimates the total number of search results after
// excluding messages in channels matched by the skip predicate. It filters the
// sampled Messages array and extrapolates to TotalResults.
func filteredSearchCount(resp *discordSearchResponse, skip func(snowflake.ID) bool) int {
	if len(resp.Messages) == 0 {
		return 0
	}
	eligible := 0
	for _, hits := range resp.Messages {
		if len(hits) > 0 && !skip(hits[0].ChannelID) {
			eligible++
		}
	}
	n := len(resp.Messages)
	if resp.TotalResults <= n {
		return eligible
	}
	return eligible * resp.TotalResults / n
}

// filterMessages returns a copy of resp.Messages with blacklisted channels removed.
func filterMessages(resp *discordSearchResponse, skip func(snowflake.ID) bool) [][]searchHitMessage {
	out := make([][]searchHitMessage, 0, len(resp.Messages))
	for _, hits := range resp.Messages {
		if len(hits) > 0 && !skip(hits[0].ChannelID) {
			out = append(out, hits)
		}
	}
	return out
}

var errSearchIndexing = errors.New("search index not ready")

func (b *Bot) searchUserMessages(guildID, userID snowflake.ID) (*discordSearchResponse, error) {
	url := fmt.Sprintf("https://discord.com/api/v10/guilds/%d/messages/search?author_id=%d", guildID, userID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bot "+b.Client.Token)

	resp, err := b.searchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		return nil, errSearchIndexing
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search API returned status %d", resp.StatusCode)
	}

	var result discordSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

func (b *Bot) fetchPosterRoleHistory(guildID snowflake.ID, userID snowflake.ID, userIDStr string) {
	result, err := b.searchUserMessages(guildID, userID)
	if err != nil {
		if errors.Is(err, errSearchIndexing) {
			b.Log.Info("Search index not ready, will retry on next message", "user_id", userID)
		} else {
			b.Log.Error("Failed to fetch poster role history", "user_id", userID, "error", err)
		}
		return
	}

	st := b.posterRole[guildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	cfg := st.Settings
	st.mu.Unlock()

	skip := func(chID snowflake.ID) bool {
		return cfg.isSkipped(b.resolveChannelParents(chID, &st.parentCache)...)
	}
	count := filteredSearchCount(result, skip)

	st.mu.Lock()
	st.Counts[userIDStr] += count
	st.Fetched[userIDStr] = true
	st.mu.Unlock()

	b.Log.Info("Cached historical post count", "user_id", userID, "guild_id", guildID, "count", count)
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
	ctx := context.Background()
	for _, guildID := range b.GuildIDs() {
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

	if err := e.DeferCreateMessage(ephemeral); err != nil {
		b.Log.Error("Failed to defer marketprogress response", "error", err)
		return
	}

	// Fetch member info (for role check + join date)
	var hasRole bool
	var joinedAt *string
	member, memberErr := b.Client.Rest.GetMember(guildID, user.ID)
	if memberErr == nil {
		for _, roleID := range member.RoleIDs {
			if roleID == cfg.RoleID {
				hasRole = true
				break
			}
		}
		if member.JoinedAt != nil {
			ts := fmt.Sprintf("<t:%d:R>", member.JoinedAt.Unix())
			joinedAt = &ts
		}
	}

	// Fetch live search results
	searchResult, searchErr := b.searchUserMessages(guildID, user.ID)
	if searchErr != nil && errors.Is(searchErr, errSearchIndexing) {
		msg := discord.MessageCreate{
			Content: "Search index is building, please try again shortly.",
		}
		if ephemeral {
			msg.Flags = discord.MessageFlagEphemeral
		}
		b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), msg)
		return
	}

	skip := func(chID snowflake.ID) bool {
		return cfg.isSkipped(b.resolveChannelParents(chID, &st.parentCache)...)
	}

	count := 0
	if searchResult != nil {
		count = filteredSearchCount(searchResult, skip)
	}

	// Build embed fields
	var fields []discord.EmbedField
	if memberErr == nil {
		hasRoleStr := "No"
		if hasRole {
			hasRoleStr = "Yes"
		}
		fields = append(fields, discord.EmbedField{
			Name:   "Has Role",
			Value:  hasRoleStr,
			Inline: boolPtr(true),
		})
	}
	if joinedAt != nil {
		fields = append(fields, discord.EmbedField{
			Name:   "Joined",
			Value:  *joinedAt,
			Inline: boolPtr(true),
		})
	}

	fields = append(fields, discord.EmbedField{
		Name:   "Total Posts",
		Value:  fmt.Sprintf("%d", count),
		Inline: boolPtr(true),
	})

	if !hasRole {
		pct := 0
		if cfg.Threshold > 0 {
			pct = count * 100 / cfg.Threshold
		}
		remaining := cfg.Threshold - count
		if remaining < 0 {
			remaining = 0
		}
		fields = append(fields, discord.EmbedField{
			Name:  "Progress",
			Value: fmt.Sprintf("%d / %d (%d%%) — %d more needed", count, cfg.Threshold, pct, remaining),
		})
	}

	if searchResult != nil {
		filtered := &discordSearchResponse{Messages: filterMessages(searchResult, skip)}
		top := topChannels(filtered, 5)
		if len(top) > 0 {
			var lines []string
			for _, tc := range top {
				lines = append(lines, fmt.Sprintf("<#%d>: %d", tc.ChannelID, tc.Count))
			}
			fields = append(fields, discord.EmbedField{
				Name:  "Recent Activity (last 25 posts)",
				Value: strings.Join(lines, "\n"),
			})
		}
	}

	embed := discord.Embed{
		Title:  fmt.Sprintf("Marketplace Progress for %s", user.Username),
		Fields: fields,
	}

	msg := discord.MessageCreate{
		Embeds: []discord.Embed{embed},
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
