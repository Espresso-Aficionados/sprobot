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

func (b *Bot) loadTopPostersConfig() {
	ctx := context.Background()
	defaults := defaultTopPostersConfig()
	for _, guildID := range b.GuildIDs() {
		st := &topPostersConfigState{}

		data, err := b.S3.FetchGuildJSON(ctx, "toppostersconfig", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			if roleID, ok := defaults[guildID]; ok {
				st.TargetRoleID = roleID
			}
			b.Log.Info("No existing topposters config, using defaults", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load topposters config", "guild_id", guildID, "error", err)
			if roleID, ok := defaults[guildID]; ok {
				st.TargetRoleID = roleID
			}
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode topposters config", "guild_id", guildID, "error", err)
			}
		}

		b.topPostersConfig[guildID] = st
		b.Log.Info("Loaded topposters config", "guild_id", guildID, "target_role_id", st.TargetRoleID)
	}
}

func (b *Bot) persistTopPostersConfig(guildID snowflake.ID, st *topPostersConfigState) error {
	st.mu.Lock()
	data, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return b.S3.SaveGuildJSON(context.Background(), "toppostersconfig", fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) saveTopPostersConfig() {
	for guildID, st := range b.topPostersConfig {
		if err := b.persistTopPostersConfig(guildID, st); err != nil {
			b.Log.Error("Failed to save topposters config", "guild_id", guildID, "error", err)
		}
	}
}

type guildPostCounts struct {
	mu        sync.Mutex
	Counts    map[string]map[string]int // date -> userID -> count
	Usernames map[string]string         // userID -> last known username
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

	gc := b.topPosters[guildID]
	if gc == nil {
		return
	}

	// Filter out users with the target role at recording time
	if cfgSt := b.topPostersConfig[guildID]; cfgSt != nil && e.Message.Member != nil {
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

func (b *Bot) loadTopPosters() {
	ctx := context.Background()
	for _, guildID := range b.GuildIDs() {
		gc := &guildPostCounts{
			Counts:    make(map[string]map[string]int),
			Usernames: make(map[string]string),
		}

		data, err := b.S3.FetchTopPosters(ctx, fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing top posters data, starting fresh", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load top posters data", "guild_id", guildID, "error", err)
		} else {
			gc.Counts = data
		}

		// Load usernames from separate key
		unData, err := b.S3.FetchGuildJSON(ctx, "topposters_usernames", fmt.Sprintf("%d", guildID))
		if err == nil {
			_ = json.Unmarshal(unData, &gc.Usernames)
		}

		b.topPosters[guildID] = gc
		b.Log.Info("Loaded top posters", "guild_id", guildID, "days", len(gc.Counts))
	}
}

func (b *Bot) saveTopPosters() {
	ctx := context.Background()
	cutoff := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")

	for guildID, gc := range b.topPosters {
		gc.mu.Lock()
		pruneOldDays(gc.Counts, cutoff)
		// Copy data while holding lock
		data := make(map[string]map[string]int, len(gc.Counts))
		for date, users := range gc.Counts {
			cp := make(map[string]int, len(users))
			for u, c := range users {
				cp[u] = c
			}
			data[date] = cp
		}
		usernames := make(map[string]string, len(gc.Usernames))
		for u, name := range gc.Usernames {
			usernames[u] = name
		}
		gc.mu.Unlock()

		if err := b.S3.SaveTopPosters(ctx, fmt.Sprintf("%d", guildID), data); err != nil {
			b.Log.Error("Failed to save top posters", "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Saved top posters", "guild_id", guildID, "days", len(data))
		}

		// Save usernames
		if unData, err := json.Marshal(usernames); err == nil {
			if err := b.S3.SaveGuildJSON(ctx, "topposters_usernames", fmt.Sprintf("%d", guildID), unData); err != nil {
				b.Log.Error("Failed to save top poster usernames", "guild_id", guildID, "error", err)
			}
		}
	}
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

	gc := b.topPosters[guildID]
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

	st := b.topPostersConfig[*guildID]
	if st == nil {
		st = &topPostersConfigState{}
		b.topPostersConfig[*guildID] = st
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

	if err := b.persistTopPostersConfig(guildID, st); err != nil {
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

	if err := b.persistTopPostersConfig(guildID, st); err != nil {
		b.Log.Error("Failed to persist topposters config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Top posters role exclusion cleared.")
}
