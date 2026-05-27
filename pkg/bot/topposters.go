package bot

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

type topPostersConfigState struct {
	mu sync.Mutex

	// Legacy fields — migrated to Filtered* on load, kept for JSON compat.
	TargetRoleID     snowflake.ID   `json:"target_role_id,omitempty"`
	BlacklistedUsers []snowflake.ID `json:"blacklisted_users,omitempty"`

	// Unified filter lists.
	FilteredUsers    []snowflake.ID `json:"filtered_users,omitempty"`
	FilteredRoles    []snowflake.ID `json:"filtered_roles,omitempty"`
	FilteredChannels []snowflake.ID `json:"filtered_channels,omitempty"`
}

// migrateLegacy moves TargetRoleID and BlacklistedUsers into the unified
// filter slices. Called via postLoad after unmarshal.
func (st *topPostersConfigState) migrateLegacy() {
	if st.TargetRoleID != 0 {
		if !containsAny(st.FilteredRoles, st.TargetRoleID) {
			st.FilteredRoles = append(st.FilteredRoles, st.TargetRoleID)
		}
		st.TargetRoleID = 0
	}
	for _, uid := range st.BlacklistedUsers {
		if !containsAny(st.FilteredUsers, uid) {
			st.FilteredUsers = append(st.FilteredUsers, uid)
		}
	}
	st.BlacklistedUsers = nil
}

func (st *topPostersConfigState) isUserFiltered(userID snowflake.ID) bool {
	return containsAny(st.FilteredUsers, userID)
}

func (st *topPostersConfigState) hasFilteredRole(roleIDs []snowflake.ID) bool {
	for _, rid := range roleIDs {
		if containsAny(st.FilteredRoles, rid) {
			return true
		}
	}
	return false
}

func (st *topPostersConfigState) isChannelFiltered(ids ...snowflake.ID) bool {
	return containsAny(st.FilteredChannels, ids...)
}

func defaultTopPostersConfig() map[snowflake.ID]snowflake.ID {
	return map[snowflake.ID]snowflake.ID{
		726985544038612993:  791104833117225000,
		1013566342345019512: 0,
	}
}

type guildPostCounts struct {
	mu        sync.Mutex
	Counts    map[string]map[string]int `json:"counts"`    // date -> userID -> count
	Usernames map[string]string         `json:"usernames"` // userID -> last known username
}

// UnmarshalJSON handles both the new {"counts":…,"usernames":…} format and the
// legacy bare map[string]map[string]int format (counts only, no wrapper).
func (g *guildPostCounts) UnmarshalJSON(data []byte) error {
	// Try new format first.
	type alias guildPostCounts
	var a alias
	if err := json.Unmarshal(data, &a); err == nil && a.Counts != nil {
		g.Counts = a.Counts
		g.Usernames = a.Usernames
		return nil
	}

	// Fall back to legacy bare counts map.
	var counts map[string]map[string]int
	if err := json.Unmarshal(data, &counts); err != nil {
		return err
	}
	g.Counts = counts
	return nil
}

func (b *Bot) onMessage(e *events.MessageCreate) {
	if e.Message.Author.Bot {
		return
	}
	if e.GuildID == nil {
		return
	}

	guildID := *e.GuildID
	b.ensureAutoRole(guildID, e.Message)
	b.checkPosterRole(guildID, e.ChannelID, e.Message)
	b.checkTempRolesOnMessage(guildID, e.Message)

	gc := b.topPosters.get(guildID)
	if gc == nil {
		return
	}

	// Apply unified filters: user, role, channel
	if cfgSt := b.topPostersConfig.get(guildID); cfgSt != nil {
		cfgSt.mu.Lock()
		userBlocked := cfgSt.isUserFiltered(e.Message.Author.ID)
		roleBlocked := e.Message.Member != nil && cfgSt.hasFilteredRole(e.Message.Member.RoleIDs)
		channelBlocked := cfgSt.isChannelFiltered(e.ChannelID)
		cfgSt.mu.Unlock()
		if userBlocked || roleBlocked || channelBlocked {
			return
		}
	}

	today := time.Now().UTC().Format("2006-01-02")
	userID := fmt.Sprintf("%d", e.Message.Author.ID)

	gc.mu.Lock()
	defer gc.mu.Unlock()

	if gc.Counts[today] == nil {
		gc.Counts[today] = make(map[string]int)
	}
	gc.Counts[today][userID]++
	if gc.Usernames == nil {
		gc.Usernames = make(map[string]string)
	}
	gc.Usernames[userID] = e.Message.Author.Username
}

func (b *Bot) saveTopPosters() {
	cutoff := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")
	b.topPosters.each(func(_ snowflake.ID, gc *guildPostCounts) {
		gc.mu.Lock()
		pruneOldDays(gc.Counts, cutoff)
		gc.mu.Unlock()
	})
	b.topPosters.save()
}

func pruneOldDays(counts map[string]map[string]int, cutoff string) {
	for date := range counts {
		if date < cutoff {
			delete(counts, date)
		}
	}
}

func oldestDate(counts map[string]map[string]int) string {
	oldest := ""
	for date := range counts {
		if oldest == "" || date < oldest {
			oldest = date
		}
	}
	return oldest
}

func aggregateCounts(counts map[string]map[string]int) map[string]int {
	totals := make(map[string]int)
	for _, users := range counts {
		for userID, count := range users {
			totals[userID] += count
		}
	}
	return totals
}

type posterEntry struct {
	UserID string
	Count  int
}

func (b *Bot) handleTopPosters(e *events.ApplicationCommandInteractionCreate) {
	if e.GuildID() == nil {
		return
	}
	guildID := *e.GuildID()

	b.Log.Info("Top posters", "user_id", e.User().ID, "guild_id", guildID)

	// Check ManageMessages permission
	if member := e.Member(); member == nil || member.Permissions&discord.PermissionManageMessages == 0 {
		botutil.RespondEphemeral(e, "You don't have permission to use this command.")
		return
	}

	gc := b.topPosters.get(guildID)
	if gc == nil {
		botutil.RespondEphemeral(e, "No data available yet.")
		return
	}

	// Snapshot filters for display
	var filteredUsers map[snowflake.ID]struct{}
	var filteredRoles []snowflake.ID
	if cfgSt := b.topPostersConfig.get(guildID); cfgSt != nil {
		cfgSt.mu.Lock()
		if len(cfgSt.FilteredUsers) > 0 {
			filteredUsers = make(map[snowflake.ID]struct{}, len(cfgSt.FilteredUsers))
			for _, id := range cfgSt.FilteredUsers {
				filteredUsers[id] = struct{}{}
			}
		}
		filteredRoles = make([]snowflake.ID, len(cfgSt.FilteredRoles))
		copy(filteredRoles, cfgSt.FilteredRoles)
		cfgSt.mu.Unlock()
	}

	gc.mu.Lock()
	totals := aggregateCounts(gc.Counts)
	since := oldestDate(gc.Counts)
	usernames := make(map[string]string, len(gc.Usernames))
	for u, name := range gc.Usernames {
		usernames[u] = name
	}
	gc.mu.Unlock()

	// Sort by count descending, excluding filtered users and roles
	entries := make([]posterEntry, 0, len(totals))
	for userID, count := range totals {
		uid, err := snowflake.Parse(userID)
		if err != nil {
			entries = append(entries, posterEntry{UserID: userID, Count: count})
			continue
		}
		if _, blocked := filteredUsers[uid]; blocked {
			continue
		}
		if len(filteredRoles) > 0 {
			if member, ok := b.Client.Caches.Member(guildID, uid); ok {
				skip := false
				for _, rid := range filteredRoles {
					for _, mrid := range member.RoleIDs {
						if rid == mrid {
							skip = true
							break
						}
					}
					if skip {
						break
					}
				}
				if skip {
					continue
				}
			}
		}
		entries = append(entries, posterEntry{UserID: userID, Count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Count > entries[j].Count
	})

	// Build top 20
	var lines []string
	for rank, entry := range entries {
		if rank >= 20 {
			break
		}
		username := usernames[entry.UserID]
		if uid, err := snowflake.Parse(entry.UserID); err == nil {
			if member, ok := b.Client.Caches.Member(guildID, uid); ok {
				username = member.User.Username
			}
		}
		if username != "" {
			lines = append(lines, fmt.Sprintf("%d. <@%s> (%s) — %d messages", rank+1, entry.UserID, username, entry.Count))
		} else {
			lines = append(lines, fmt.Sprintf("%d. <@%s> — %d messages", rank+1, entry.UserID, entry.Count))
		}
	}

	description := "No messages tracked yet."
	if len(lines) > 0 {
		description = strings.Join(lines, "\n")
	}

	title := "Top Posters (Last 7 Days)"
	if since != "" {
		if t, err := time.Parse("2006-01-02", since); err == nil {
			title = fmt.Sprintf("Top Posters (Since %s)", t.Format("Jan 2, 2006"))
		}
	}

	embed := discord.Embed{
		Title:       title,
		Description: description,
	}

	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	})
}

// --- /config topposters handlers ---

func (b *Bot) handleTopPostersConfigCmd(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.topPostersConfig.get(*guildID)
	if st == nil {
		st = &topPostersConfigState{}
		b.topPostersConfig.set(*guildID, st)
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "filter-add":
		b.handleTopPostersFilterAdd(e, *guildID, st)
	case "filter-remove":
		b.handleTopPostersFilterRemove(e, *guildID, st)
	case "filter-list":
		b.handleTopPostersFilterList(e, st)
	case "filter-clear":
		b.handleTopPostersFilterClear(e, *guildID, st)
	}
}

func (b *Bot) handleTopPostersFilterAdd(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *topPostersConfigState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	user, hasUser := data.OptUser("user")
	role, hasRole := data.OptRole("role")
	ch, hasCh := data.OptChannel("channel")

	if !hasUser && !hasRole && !hasCh {
		botutil.RespondEphemeral(e, "Please provide at least one of: user, role, or channel.")
		return
	}

	b.Log.Info("Top posters filter add", "user_id", e.User().ID, "guild_id", guildID)

	var added []string
	st.mu.Lock()
	if hasUser && !containsAny(st.FilteredUsers, user.ID) {
		st.FilteredUsers = append(st.FilteredUsers, user.ID)
		added = append(added, fmt.Sprintf("user %s", userMention(user.ID)))
	}
	if hasRole && !containsAny(st.FilteredRoles, role.ID) {
		st.FilteredRoles = append(st.FilteredRoles, role.ID)
		added = append(added, fmt.Sprintf("role <@&%d>", role.ID))
	}
	if hasCh && !containsAny(st.FilteredChannels, ch.ID) {
		st.FilteredChannels = append(st.FilteredChannels, ch.ID)
		added = append(added, fmt.Sprintf("channel <#%d>", ch.ID))
	}
	st.mu.Unlock()

	if len(added) == 0 {
		botutil.RespondEphemeral(e, "All specified items are already filtered.")
		return
	}

	if err := b.topPostersConfig.persist(guildID); err != nil {
		b.Log.Error("Failed to persist topposters filter", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save filter.")
		return
	}

	botutil.RespondEphemeral(e, "Added to filter: "+strings.Join(added, ", "))
}

func (b *Bot) handleTopPostersFilterRemove(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *topPostersConfigState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	user, hasUser := data.OptUser("user")
	role, hasRole := data.OptRole("role")
	ch, hasCh := data.OptChannel("channel")

	if !hasUser && !hasRole && !hasCh {
		botutil.RespondEphemeral(e, "Please provide at least one of: user, role, or channel.")
		return
	}

	b.Log.Info("Top posters filter remove", "user_id", e.User().ID, "guild_id", guildID)

	var removed []string
	st.mu.Lock()
	if hasUser {
		if removeID(&st.FilteredUsers, user.ID) {
			removed = append(removed, fmt.Sprintf("user %s", userMention(user.ID)))
		}
	}
	if hasRole {
		if removeID(&st.FilteredRoles, role.ID) {
			removed = append(removed, fmt.Sprintf("role <@&%d>", role.ID))
		}
	}
	if hasCh {
		if removeID(&st.FilteredChannels, ch.ID) {
			removed = append(removed, fmt.Sprintf("channel <#%d>", ch.ID))
		}
	}
	st.mu.Unlock()

	if len(removed) == 0 {
		botutil.RespondEphemeral(e, "None of the specified items were in the filter.")
		return
	}

	if err := b.topPostersConfig.persist(guildID); err != nil {
		b.Log.Error("Failed to persist topposters filter removal", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save changes.")
		return
	}

	botutil.RespondEphemeral(e, "Removed from filter: "+strings.Join(removed, ", "))
}

func (b *Bot) handleTopPostersFilterList(e *events.ApplicationCommandInteractionCreate, st *topPostersConfigState) {
	b.Log.Info("Top posters filter list", "user_id", e.User().ID, "guild_id", *e.GuildID())

	st.mu.Lock()
	users := make([]snowflake.ID, len(st.FilteredUsers))
	copy(users, st.FilteredUsers)
	roles := make([]snowflake.ID, len(st.FilteredRoles))
	copy(roles, st.FilteredRoles)
	channels := make([]snowflake.ID, len(st.FilteredChannels))
	copy(channels, st.FilteredChannels)
	st.mu.Unlock()

	if len(users) == 0 && len(roles) == 0 && len(channels) == 0 {
		botutil.RespondEphemeral(e, "No filters configured.")
		return
	}

	var sections []string
	if len(users) > 0 {
		var lines []string
		for _, id := range users {
			lines = append(lines, fmt.Sprintf("- %s", userMention(id)))
		}
		sections = append(sections, "**Users:**\n"+strings.Join(lines, "\n"))
	}
	if len(roles) > 0 {
		var lines []string
		for _, id := range roles {
			lines = append(lines, fmt.Sprintf("- <@&%d>", id))
		}
		sections = append(sections, "**Roles:**\n"+strings.Join(lines, "\n"))
	}
	if len(channels) > 0 {
		var lines []string
		for _, id := range channels {
			lines = append(lines, fmt.Sprintf("- <#%d>", id))
		}
		sections = append(sections, "**Channels:**\n"+strings.Join(lines, "\n"))
	}

	botutil.RespondEphemeral(e, strings.Join(sections, "\n\n"))
}

func (b *Bot) handleTopPostersFilterClear(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *topPostersConfigState) {
	b.Log.Info("Top posters filter clear", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	count := len(st.FilteredUsers) + len(st.FilteredRoles) + len(st.FilteredChannels)
	st.FilteredUsers = nil
	st.FilteredRoles = nil
	st.FilteredChannels = nil
	st.mu.Unlock()

	if count == 0 {
		botutil.RespondEphemeral(e, "No filters to clear.")
		return
	}

	if err := b.topPostersConfig.persist(guildID); err != nil {
		b.Log.Error("Failed to persist topposters filter clear", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save changes.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Cleared %d filter entries.", count))
}

// removeID removes the first occurrence of id from the slice. Returns true if found.
func removeID(slice *[]snowflake.ID, id snowflake.ID) bool {
	for i, v := range *slice {
		if v == id {
			*slice = append((*slice)[:i], (*slice)[i+1:]...)
			return true
		}
	}
	return false
}
