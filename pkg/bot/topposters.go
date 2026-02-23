package bot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

type topPostersConfig struct {
	TargetRoleID snowflake.ID // Role to filter OUT (0 = no filtering)
}

func getTopPostersConfig(env string) map[snowflake.ID]topPostersConfig {
	switch env {
	case "prod":
		return map[snowflake.ID]topPostersConfig{
			726985544038612993: {
				TargetRoleID: 791104833117225000,
			},
		}
	case "dev":
		return map[snowflake.ID]topPostersConfig{
			1013566342345019512: {
				TargetRoleID: 0,
			},
		}
	default:
		return nil
	}
}

type guildPostCounts struct {
	mu     sync.Mutex
	Counts map[string]map[string]int // date -> userID -> count
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

	configs := getTopPostersConfig(b.Env)
	cfg, ok := configs[guildID]
	if !ok {
		return
	}

	// Filter out users with the target role at recording time
	if cfg.TargetRoleID != 0 && e.Message.Member != nil {
		for _, roleID := range e.Message.Member.RoleIDs {
			if roleID == cfg.TargetRoleID {
				return
			}
		}
	}

	gc := b.topPosters[guildID]
	if gc == nil {
		return
	}

	today := time.Now().UTC().Format("2006-01-02")
	userID := fmt.Sprintf("%d", e.Message.Author.ID)

	gc.mu.Lock()
	defer gc.mu.Unlock()

	if gc.Counts[today] == nil {
		gc.Counts[today] = make(map[string]int)
	}
	gc.Counts[today][userID]++
}

func (b *Bot) loadTopPosters() {
	configs := getTopPostersConfig(b.Env)
	if configs == nil {
		return
	}

	ctx := context.Background()
	for guildID := range configs {
		gc := &guildPostCounts{Counts: make(map[string]map[string]int)}

		data, err := b.S3.FetchTopPosters(ctx, fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing top posters data, starting fresh", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load top posters data", "guild_id", guildID, "error", err)
		} else {
			gc.Counts = data
		}

		b.topPosters[guildID] = gc
		b.Log.Info("Loaded top posters", "guild_id", guildID, "days", len(gc.Counts))
	}
}

func (b *Bot) saveTopPosters() {
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in top posters save", "error", r)
		}
	}()

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
		gc.mu.Unlock()

		if err := b.S3.SaveTopPosters(ctx, fmt.Sprintf("%d", guildID), data); err != nil {
			b.Log.Error("Failed to save top posters", "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Saved top posters", "guild_id", guildID, "days", len(data))
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

	configs := getTopPostersConfig(b.Env)
	if _, ok := configs[guildID]; !ok {
		respondEphemeral(e, "This command is not configured for this server.")
		return
	}

	// Check ManageMessages permission
	if member := e.Member(); member == nil || member.Permissions&discord.PermissionManageMessages == 0 {
		respondEphemeral(e, "You don't have permission to use this command.")
		return
	}

	gc := b.topPosters[guildID]
	if gc == nil {
		respondEphemeral(e, "No data available yet.")
		return
	}

	gc.mu.Lock()
	totals := aggregateCounts(gc.Counts)
	since := oldestDate(gc.Counts)
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
		lines = append(lines, fmt.Sprintf("%d. <@%s> — %d messages", rank+1, entry.UserID, entry.Count))
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
