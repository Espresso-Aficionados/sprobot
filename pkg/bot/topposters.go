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
	mu           sync.Mutex
	TargetRoleID snowflake.ID `json:"target_role_id"` // Role to filter OUT (0 = no filtering)
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

	// Filter out users with the target role at recording time
	if cfgSt := b.topPostersConfig.get(guildID); cfgSt != nil && e.Message.Member != nil {
		cfgSt.mu.Lock()
		targetRoleID := cfgSt.TargetRoleID
		cfgSt.mu.Unlock()
		if targetRoleID != 0 {
			for _, roleID := range e.Message.Member.RoleIDs {
				if roleID == targetRoleID {
					return
				}
			}
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

	gc.mu.Lock()
	totals := aggregateCounts(gc.Counts)
	since := oldestDate(gc.Counts)
	usernames := make(map[string]string, len(gc.Usernames))
	for u, name := range gc.Usernames {
		usernames[u] = name
	}
	gc.mu.Unlock()

	// Sort by count descending
	entries := make([]posterEntry, 0, len(totals))
	for userID, count := range totals {
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
	case "set":
		b.handleTopPostersConfigSet(e, *guildID, st)
	case "show":
		b.handleTopPostersConfigShow(e, st)
	case "clear":
		b.handleTopPostersConfigClear(e, *guildID, st)
	}
}

func (b *Bot) handleTopPostersConfigSet(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *topPostersConfigState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	role, ok := data.OptRole("role")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a role.")
		return
	}

	b.Log.Info("Top posters config set", "user_id", e.User().ID, "guild_id", guildID, "role_id", role.ID)

	st.mu.Lock()
	st.TargetRoleID = role.ID
	st.mu.Unlock()

	if err := b.topPostersConfig.persist(guildID); err != nil {
		b.Log.Error("Failed to persist topposters config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Top posters exclude role set to <@&%d>.", role.ID))
}

func (b *Bot) handleTopPostersConfigShow(e *events.ApplicationCommandInteractionCreate, st *topPostersConfigState) {
	b.Log.Info("Top posters config show", "user_id", e.User().ID, "guild_id", *e.GuildID())

	st.mu.Lock()
	roleID := st.TargetRoleID
	st.mu.Unlock()

	if roleID == 0 {
		botutil.RespondEphemeral(e, "**Exclude role:** Not set")
		return
	}
	botutil.RespondEphemeral(e, fmt.Sprintf("**Exclude role:** <@&%d>", roleID))
}

func (b *Bot) handleTopPostersConfigClear(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *topPostersConfigState) {
	b.Log.Info("Top posters config clear", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	st.TargetRoleID = 0
	st.mu.Unlock()

	if err := b.topPostersConfig.persist(guildID); err != nil {
		b.Log.Error("Failed to persist topposters config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Top posters role exclusion cleared.")
}
