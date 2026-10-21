package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

type posterRoleSettings struct {
	RoleID           snowflake.ID   `json:"role_id"`
	Threshold        int            `json:"threshold"`
	SkipChannels     []snowflake.ID `json:"skip_channels"`
	BlacklistedUsers []snowflake.ID `json:"blacklisted_users,omitempty"`
}

func (s *posterRoleSettings) isSkipped(ids ...snowflake.ID) bool {
	return containsAny(s.SkipChannels, ids...)
}

func (s *posterRoleSettings) isUserBlacklisted(userID snowflake.ID) bool {
	for _, id := range s.BlacklistedUsers {
		if id == userID {
			return true
		}
	}
	return false
}

type posterRoleState struct {
	mu       sync.Mutex
	Settings posterRoleSettings `json:"settings"`
	Counts   map[string]int     `json:"counts"`
	Fetched  map[string]bool    `json:"fetched"`

	searches    map[string]*historySearch // history searches in flight, by user
	searchRetry retryAfter                // users whose history search failed
	grantRetry  retryAfter                // users whose role grant failed

	parentCache sync.Map // channelID → channelParents
}

func (st *posterRoleState) lock()                          { st.mu.Lock() }
func (st *posterRoleState) unlock()                        { st.mu.Unlock() }
func (st *posterRoleState) getBlacklist() []snowflake.ID   { return st.Settings.SkipChannels }
func (st *posterRoleState) setBlacklist(bl []snowflake.ID) { st.Settings.SkipChannels = bl }

// posterSearchRetry is how long a user's history search waits after the
// search index wasn't ready or the search failed.
const posterSearchRetry = 5 * time.Minute

// historySearch is a search of one user's post history in flight. before is
// their count when it started.
type historySearch struct {
	before int
}

// countMessage counts a message from userID and, if their history still needs
// fetching, returns a search for the caller to run. Only one search runs per
// user: messages that arrive meanwhile are only counted. Callers hold st.mu.
func (st *posterRoleState) countMessage(userID snowflake.ID, now time.Time) *historySearch {
	key := userID.String()
	st.Counts[key]++
	if st.Fetched[key] || st.searches[key] != nil || !st.searchRetry.ready(userID, now) {
		return nil
	}
	if st.searches == nil {
		st.searches = make(map[string]*historySearch)
	}
	s := &historySearch{before: st.Counts[key]}
	st.searches[key] = s
	return s
}

// finishSearch records the searched count plus the messages counted while the
// search ran. It drops the result, returning false, if tracking was cleared or
// the user was banned meanwhile. Callers hold st.mu.
func (st *posterRoleState) finishSearch(userID snowflake.ID, s *historySearch, searched int) bool {
	key := userID.String()
	if st.searches[key] != s {
		return false
	}
	delete(st.searches, key)
	if st.Settings.isUserBlacklisted(userID) {
		return false
	}
	st.Counts[key] = searched + max(0, st.Counts[key]-s.before)
	st.Fetched[key] = true
	return true
}

// failSearch ends a search that failed and holds off the next one for the
// user. Callers hold st.mu.
func (st *posterRoleState) failSearch(userID snowflake.ID, s *historySearch, now time.Time) {
	key := userID.String()
	if st.searches[key] != s {
		return
	}
	delete(st.searches, key)
	st.searchRetry.fail(userID, now, posterSearchRetry)
}

// grantDue reports whether userID should get the poster role now: their
// history is fetched, their count has reached the threshold, they aren't
// banned and no failed grant is holding off. Callers hold st.mu.
func (st *posterRoleState) grantDue(userID snowflake.ID, now time.Time) (roleID snowflake.ID, count int, due bool) {
	key := userID.String()
	s := st.Settings
	count = st.Counts[key]
	due = s.RoleID != 0 && s.Threshold != 0 && st.Fetched[key] && count >= s.Threshold &&
		!s.isUserBlacklisted(userID) && st.grantRetry.ready(userID, now)
	return s.RoleID, count, due
}

// clearTracking forgets userID's count, and any search still running for
// them, whose result is then dropped. Callers hold st.mu.
func (st *posterRoleState) clearTracking(userID snowflake.ID) {
	key := userID.String()
	delete(st.Counts, key)
	delete(st.Fetched, key)
	delete(st.searches, key)
	st.searchRetry.clear(userID)
	st.grantRetry.clear(userID)
}

func (b *Bot) checkPosterRole(guildID snowflake.ID, channelID snowflake.ID, msg discord.Message) {
	st := b.posterRole.get(guildID)
	if st == nil {
		return
	}

	st.mu.Lock()
	cfg := st.Settings
	st.mu.Unlock()

	if cfg.RoleID == 0 || cfg.Threshold == 0 {
		return
	}

	if msg.Member == nil {
		return
	}
	userID := msg.Author.ID
	if st.forgetIfRoleHeld(userID, msg.Member.RoleIDs) {
		return
	}

	if cfg.isSkipped(b.resolveChannelParents(channelID, &st.parentCache)...) {
		return
	}

	if cfg.isUserBlacklisted(msg.Author.ID) {
		return
	}

	now := time.Now()

	st.mu.Lock()
	search := st.countMessage(userID, now)
	st.mu.Unlock()

	if search != nil {
		b.fetchPosterRoleHistory(guildID, st, userID, search)
	}

	b.grantPosterRoleIfDue(guildID, st, userID, now)
}

// grantPosterRoleIfDue grants the poster role once grantDue says so. A market
// ban can land while the grant is in flight, so the ban list is checked under
// the lock right before it and again after it, revoking the role if needed.
func (b *Bot) grantPosterRoleIfDue(guildID snowflake.ID, st *posterRoleState, userID snowflake.ID, now time.Time) {
	st.mu.Lock()
	roleID, count, due := st.grantDue(userID, now)
	st.mu.Unlock()
	if !due {
		return
	}

	var err error
	if reason := b.grantBlocked(guildID, roleID); reason != "" {
		err = errors.New(reason)
	} else {
		err = b.Client.Rest.AddMemberRole(guildID, userID, roleID, rest.WithReason("Reached marketplace post threshold"))
	}
	if err != nil {
		st.mu.Lock()
		st.grantRetry.fail(userID, now, roleGrantRetry)
		st.mu.Unlock()
		b.Log.Error("Failed to grant poster role", "user_id", userID, "guild_id", guildID, "role_id", roleID, "error", err)
		return
	}

	st.mu.Lock()
	banned := st.Settings.isUserBlacklisted(userID)
	st.mu.Unlock()
	if banned {
		b.Log.Info("Market ban landed while granting poster role, revoking it", "user_id", userID, "guild_id", guildID)
		if err := b.Client.Rest.RemoveMemberRole(guildID, userID, roleID, rest.WithReason("Banned from the marketplace")); err != nil {
			b.Log.Error("Failed to revoke poster role from banned user", "user_id", userID, "guild_id", guildID, "role_id", roleID, "error", err)
		}
		return
	}
	b.Log.Info("Granted poster role", "user_id", userID, "guild_id", guildID, "total", count)
	b.clearPosterRoleTracking(guildID, userID)
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

func (b *Bot) fetchPosterRoleHistory(guildID snowflake.ID, st *posterRoleState, userID snowflake.ID, search *historySearch) {
	result, err := b.searchUserMessages(guildID, userID)
	if err != nil {
		st.mu.Lock()
		st.failSearch(userID, search, time.Now())
		st.mu.Unlock()
		if errors.Is(err, errSearchIndexing) {
			b.Log.Info("Search index not ready, will retry later", "user_id", userID, "retry_in", posterSearchRetry)
		} else {
			b.Log.Error("Failed to fetch poster role history", "user_id", userID, "retry_in", posterSearchRetry, "error", err)
		}
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
	recorded := st.finishSearch(userID, search, count)
	st.mu.Unlock()

	if recorded {
		b.Log.Info("Cached historical post count", "user_id", userID, "guild_id", guildID, "count", count)
	} else {
		b.Log.Info("Dropped post history search: tracking was cleared or the user was banned", "user_id", userID, "guild_id", guildID)
	}
}

// forgetIfRoleHeld reports whether a member has the poster role and, if so,
// drops their count, so a role granted by staff doesn't leave one behind.
func (st *posterRoleState) forgetIfRoleHeld(userID snowflake.ID, roleIDs []snowflake.ID) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !slices.Contains(roleIDs, st.Settings.RoleID) {
		return false
	}
	st.clearTracking(userID)
	return true
}

func (b *Bot) clearPosterRoleTracking(guildID snowflake.ID, userID snowflake.ID) {
	st := b.posterRole.get(guildID)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.clearTracking(userID)
	st.mu.Unlock()
}

func (b *Bot) handleMarketProgress(e *events.ApplicationCommandInteractionCreate) {
	if e.GuildID() == nil {
		return
	}
	guildID := *e.GuildID()

	st := b.posterRole.get(guildID)
	if st == nil {
		botutil.RespondEphemeral(e, "Poster role is not configured for this server.")
		return
	}

	st.mu.Lock()
	cfg := st.Settings
	st.mu.Unlock()

	if cfg.RoleID == 0 || cfg.Threshold == 0 {
		botutil.RespondEphemeral(e, "Poster role is not configured. Use `/config market set` first.")
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

	p := marketProgress{
		Username:  user.Username,
		Threshold: cfg.Threshold,
		Banned:    cfg.isUserBlacklisted(user.ID),
	}

	// Fetch member info (for role check + join date)
	if member, err := b.Client.Rest.GetMember(guildID, user.ID); err == nil {
		p.MemberKnown = true
		p.HasRole = slices.Contains(member.RoleIDs, cfg.RoleID)
		p.JoinedAt = member.JoinedAt
	}

	// Fetch live search results
	searchResult, searchErr := b.searchUserMessages(guildID, user.ID)
	if errors.Is(searchErr, errSearchIndexing) {
		msg := discord.MessageCreate{
			Content: "Search index is building, please try again shortly.",
		}
		if ephemeral {
			msg.Flags = discord.MessageFlagEphemeral
		}
		b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), msg)
		return
	}
	if searchErr != nil {
		b.Log.Error("Failed to search post history for market progress", "guild_id", guildID, "target_user", user.ID, "error", searchErr)
	} else {
		skip := func(chID snowflake.ID) bool {
			return cfg.isSkipped(b.resolveChannelParents(chID, &st.parentCache)...)
		}
		p.Searched = true
		p.Count = filteredSearchCount(searchResult, skip)
		p.Top = topChannels(&discordSearchResponse{Messages: filterMessages(searchResult, skip)}, 5)
	}

	msg := discord.MessageCreate{
		Embeds: []discord.Embed{p.embed()},
	}
	if ephemeral {
		msg.Flags = discord.MessageFlagEphemeral
	}
	b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), msg)
}

// marketProgress is what /market progress shows about a user.
type marketProgress struct {
	Username    string
	Threshold   int
	Banned      bool
	MemberKnown bool // the member lookup worked, so HasRole is known
	HasRole     bool
	JoinedAt    *time.Time
	Searched    bool // the history search worked, so Count and Top are known
	Count       int
	Top         []channelCount
}

func (p marketProgress) embed() discord.Embed {
	var fields []discord.EmbedField
	if p.Banned {
		fields = append(fields, discord.EmbedField{
			Name:   "Banned",
			Value:  "Yes",
			Inline: boolPtr(true),
		})
	}
	if p.MemberKnown {
		hasRoleStr := "No"
		if p.HasRole {
			hasRoleStr = "Yes"
		}
		fields = append(fields, discord.EmbedField{
			Name:   "Has Role",
			Value:  hasRoleStr,
			Inline: boolPtr(true),
		})
	}
	if p.JoinedAt != nil {
		fields = append(fields, discord.EmbedField{
			Name:   "Joined",
			Value:  fmt.Sprintf("<t:%d:R>", p.JoinedAt.Unix()),
			Inline: boolPtr(true),
		})
	}

	if !p.Searched {
		// A failed search says nothing about the count; don't show it as 0.
		fields = append(fields, discord.EmbedField{
			Name:  "Total Posts",
			Value: "Couldn't fetch post history. Please try again later.",
		})
	} else {
		fields = append(fields, discord.EmbedField{
			Name:   "Total Posts",
			Value:  fmt.Sprintf("%d", p.Count),
			Inline: boolPtr(true),
		})
	}

	if p.Searched && !p.HasRole {
		pct, remaining := thresholdProgress(p.Count, p.Threshold)
		fields = append(fields, discord.EmbedField{
			Name:  "Progress",
			Value: fmt.Sprintf("%d / %d (%d%%) — %d more needed", p.Count, p.Threshold, pct, remaining),
		})
	}

	if len(p.Top) > 0 {
		var lines []string
		for _, tc := range p.Top {
			lines = append(lines, fmt.Sprintf("<#%d>: %d", tc.ChannelID, tc.Count))
		}
		fields = append(fields, discord.EmbedField{
			Name:  "Recent Activity (last 25 posts)",
			Value: strings.Join(lines, "\n"),
		})
	}

	return discord.Embed{
		Title:  fmt.Sprintf("Marketplace Progress for %s", p.Username),
		Fields: fields,
	}
}

// thresholdProgress returns count as a percentage of threshold and how many
// posts are still needed.
func thresholdProgress(count, threshold int) (pct, remaining int) {
	if threshold > 0 {
		pct = count * 100 / threshold
	}
	return pct, max(0, threshold-count)
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

	st := b.posterRole.get(guildID)
	if st == nil {
		botutil.RespondEphemeral(e, "Poster role is not configured for this server.")
		return
	}

	st.mu.Lock()
	cfg := st.Settings
	st.mu.Unlock()

	if cfg.RoleID == 0 || cfg.Threshold == 0 {
		botutil.RespondEphemeral(e, "Poster role is not configured. Use `/config market set` first.")
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
		pct, remaining := thresholdProgress(entry.Total, cfg.Threshold)
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

// --- /config market handlers ---

func (b *Bot) handleMarketConfig(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.posterRole.get(*guildID)
	if st == nil {
		botutil.RespondEphemeral(e, "Marketplace feature is not available for this server.")
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	persist := func() error { return b.posterRole.persist(*guildID) }
	switch *subCmd {
	case "config":
		b.handleMarketConfigSet(e, *guildID, st)
	case "show-config":
		b.handleMarketConfigShow(e, st)
	case "ban":
		b.handleMarketBanAdd(e, *guildID, st)
	case "unban":
		b.handleMarketBanRemove(e, *guildID, st)
	case "banlist":
		b.handleMarketBanList(e, st)
	case "filter-add":
		b.handleBlacklistAdd(e, *guildID, st, persist, "Market")
	case "filter-remove":
		b.handleBlacklistRemove(e, *guildID, st, persist, "Market")
	case "filter-list":
		b.handleBlacklistList(e, st, "Market")
	case "filter-clear":
		b.handleBlacklistClear(e, *guildID, st, persist, "Market")
	}
}

func (b *Bot) handleMarketConfigSet(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *posterRoleState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	b.Log.Info("Market config set", "user_id", e.User().ID, "guild_id", guildID)

	role, setRole := data.OptRole("role")
	if setRole {
		if reason := b.checkGrantableRole(e, guildID, role); reason != "" {
			botutil.RespondEphemeral(e, reason)
			return
		}
	}

	st.mu.Lock()
	if setRole {
		st.Settings.RoleID = role.ID
	}
	if v, ok := data.OptInt("threshold"); ok {
		st.Settings.Threshold = v
	}
	st.mu.Unlock()

	if err := b.posterRole.persist(guildID); err != nil {
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
		fmt.Sprintf("**Filtered Channels:** %d", len(s.SkipChannels)),
		fmt.Sprintf("**Banned Users:** %d", len(s.BlacklistedUsers)),
	}

	botutil.RespondEphemeral(e, strings.Join(lines, "\n"))
}

func (b *Bot) handleMarketBanAdd(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *posterRoleState) {
	data := e.Data.(discord.SlashCommandInteractionData)
	user, ok := data.OptUser("user")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a user.")
		return
	}
	reason := data.String("reason")

	b.Log.Info("Market ban add", "user_id", e.User().ID, "guild_id", guildID, "target_user_id", user.ID, "reason", reason)

	st.mu.Lock()
	alreadyBanned := st.Settings.isUserBlacklisted(user.ID)
	if !alreadyBanned {
		st.Settings.BlacklistedUsers = append(st.Settings.BlacklistedUsers, user.ID)
	}
	roleID := st.Settings.RoleID
	st.mu.Unlock()

	if !alreadyBanned {
		if err := b.posterRole.persist(guildID); err != nil {
			b.Log.Error("Failed to persist market ban", "guild_id", guildID, "error", err)
			// Undo the ban so that a retry runs all of it, not "already banned".
			st.mu.Lock()
			st.Settings.BlacklistedUsers, _ = withoutID(st.Settings.BlacklistedUsers, user.ID)
			st.mu.Unlock()
			botutil.RespondEphemeral(e, "Failed to save ban.")
			return
		}
	}

	// Clear tracking and remove the poster role even if the user was already
	// banned: an earlier ban may have failed before getting this far.
	b.clearPosterRoleTracking(guildID, user.ID)
	if roleID != 0 {
		if err := b.Client.Rest.RemoveMemberRole(guildID, user.ID, roleID, rest.WithReason(fmt.Sprintf("Marketplace ban by %s: %s", e.User().Username, reason))); err != nil {
			b.Log.Error("Failed to remove poster role during market ban", "guild_id", guildID, "user_id", user.ID, "role_id", roleID, "error", err)
		}
	}

	if alreadyBanned {
		botutil.RespondEphemeral(e, fmt.Sprintf("%s is already banned from the marketplace.", userMention(user.ID)))
		return
	}

	b.crossPostToModLog(guildID, user, discord.Embed{
		Title:       "Marketplace Ban",
		Description: fmt.Sprintf("%s banned %s from the marketplace.\n\n**Reason:** %s", userMention(e.User().ID), userMention(user.ID), reason),
	})

	botutil.RespondEphemeral(e, fmt.Sprintf("%s has been banned from the marketplace.", userMention(user.ID)))
}

func (b *Bot) handleMarketBanRemove(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *posterRoleState) {
	data := e.Data.(discord.SlashCommandInteractionData)
	user, ok := data.OptUser("user")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a user.")
		return
	}

	b.Log.Info("Market ban remove", "user_id", e.User().ID, "guild_id", guildID, "target_user_id", user.ID)

	st.mu.Lock()
	list, found := withoutID(st.Settings.BlacklistedUsers, user.ID)
	st.Settings.BlacklistedUsers = list
	st.mu.Unlock()

	if !found {
		botutil.RespondEphemeral(e, fmt.Sprintf("%s is not banned from the marketplace.", userMention(user.ID)))
		return
	}

	if err := b.posterRole.persist(guildID); err != nil {
		b.Log.Error("Failed to persist market ban removal", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save changes.")
		return
	}

	b.crossPostToModLog(guildID, user, discord.Embed{
		Title:       "Marketplace Unban",
		Description: fmt.Sprintf("%s unbanned %s from the marketplace.", userMention(e.User().ID), userMention(user.ID)),
	})

	botutil.RespondEphemeral(e, fmt.Sprintf("%s has been unbanned from the marketplace.", userMention(user.ID)))
}

func (b *Bot) handleMarketBanList(e *events.ApplicationCommandInteractionCreate, st *posterRoleState) {
	b.Log.Info("Market ban list", "user_id", e.User().ID, "guild_id", *e.GuildID())

	st.mu.Lock()
	list := make([]snowflake.ID, len(st.Settings.BlacklistedUsers))
	copy(list, st.Settings.BlacklistedUsers)
	st.mu.Unlock()

	if len(list) == 0 {
		botutil.RespondEphemeral(e, "No users are banned from the marketplace.")
		return
	}

	var lines []string
	for _, id := range list {
		lines = append(lines, fmt.Sprintf("- %s", userMention(id)))
	}
	botutil.RespondEphemeral(e, "**Banned users:**\n"+strings.Join(lines, "\n"))
}
