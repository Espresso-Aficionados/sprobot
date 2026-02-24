package threadbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/idleloop"
	"github.com/sadbox/sprobot/pkg/s3client"
)

type memberCountCache struct {
	Counts      map[snowflake.ID]int `json:"counts"`
	LastRefresh time.Time            `json:"last_refresh"`
}

type threadReminder struct {
	handle *idleloop.Handle `json:"-"`

	ChannelID         snowflake.ID `json:"channel_id"`
	GuildID           snowflake.ID `json:"guild_id"`
	EnabledBy         snowflake.ID `json:"enabled_by"`
	Enabled           bool         `json:"enabled"`
	LastMessageID     snowflake.ID `json:"last_message_id"`
	LastPostTime      time.Time    `json:"last_post_time"`
	MinIdleMins       int          `json:"min_idle_mins"`
	MaxIdleMins       int          `json:"max_idle_mins"`
	MsgThreshold      int          `json:"msg_threshold"`
	TimeThresholdMins int          `json:"time_threshold_mins"`
}

func (r *threadReminder) applyDefaults() {
	if r.MinIdleMins == 0 {
		r.MinIdleMins = 30
	}
	if r.MaxIdleMins == 0 {
		r.MaxIdleMins = 720
	}
	if r.MsgThreshold == 0 {
		r.MsgThreshold = 30
	}
}

func (b *Bot) loadReminders() {
	ctx := context.Background()
	for _, guildID := range botutil.GetGuildIDs(b.Env) {
		data, err := b.S3.FetchThreadReminders(ctx, fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing thread reminders data", "guild_id", guildID)
			continue
		}
		if err != nil {
			b.Log.Error("Failed to load thread reminders", "guild_id", guildID, "error", err)
			continue
		}

		var loaded map[string]*threadReminder
		if err := json.Unmarshal(data, &loaded); err != nil {
			b.Log.Error("Failed to decode thread reminders", "guild_id", guildID, "error", err)
			continue
		}

		channels := make(map[snowflake.ID]*threadReminder, len(loaded))
		for _, r := range loaded {
			r.applyDefaults()
			channels[r.ChannelID] = r
			if r.Enabled {
				b.startReminderGoroutine(r)
			}
		}
		b.reminders[guildID] = channels
		b.Log.Info("Loaded thread reminders", "guild_id", guildID, "count", len(channels))
	}
}

func (b *Bot) saveRemindersForGuild(guildID snowflake.ID) {
	b.mu.Lock()
	channels, ok := b.reminders[guildID]
	if !ok {
		b.mu.Unlock()
		return
	}
	toSave := make(map[string]*threadReminder, len(channels))
	for chID, r := range channels {
		toSave[fmt.Sprintf("%d", chID)] = r
	}
	data, err := json.Marshal(toSave)
	b.mu.Unlock()

	if err != nil {
		b.Log.Error("Failed to marshal thread reminders", "guild_id", guildID, "error", err)
		return
	}

	ctx := context.Background()
	if err := b.S3.SaveThreadReminders(ctx, fmt.Sprintf("%d", guildID), data); err != nil {
		b.Log.Error("Failed to save thread reminders", "guild_id", guildID, "error", err)
	} else {
		b.Log.Info("Saved thread reminders", "guild_id", guildID, "count", len(toSave))
	}
}

func (b *Bot) saveAllReminders() {
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in thread reminders save", "error", r)
		}
	}()

	b.mu.Lock()
	guildIDs := make([]snowflake.ID, 0, len(b.reminders))
	for id := range b.reminders {
		guildIDs = append(guildIDs, id)
	}
	b.mu.Unlock()

	for _, guildID := range guildIDs {
		b.saveRemindersForGuild(guildID)
	}
}

func (b *Bot) startReminderGoroutine(r *threadReminder) {
	r.handle = idleloop.NewHandle()
	r.handle.Start(idleloop.Config{
		MinIdleMins:       r.MinIdleMins,
		MaxIdleMins:       r.MaxIdleMins,
		MsgThreshold:      r.MsgThreshold,
		TimeThresholdMins: r.TimeThresholdMins,
		LastPostTime:      r.LastPostTime,
	}, func() bool { return b.repostReminder(r) })
}

func (b *Bot) stopReminderGoroutine(r *threadReminder) {
	r.handle.Stop()
	r.handle = nil
}

func (b *Bot) stopAllReminderGoroutines() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, channels := range b.reminders {
		for _, r := range channels {
			r.handle.Stop()
			r.handle = nil
		}
	}
}

// buildThreadEmbed fetches active threads for the given guild/channel and
// returns an embed showing the top 10 by message count. Returns nil if there
// are no active threads.
func (b *Bot) buildThreadEmbed(guildID, channelID snowflake.ID) *discord.Embed {
	result, err := b.Client.Rest.GetActiveGuildThreads(guildID)
	if err != nil {
		b.Log.Error("Failed to fetch active threads", "guild_id", guildID, "error", err)
		return nil
	}

	type threadInfo struct {
		Name         string
		ID           snowflake.ID
		MessageCount int
		MemberCount  int
		CreatedAt    time.Time
	}
	var threads []threadInfo
	for _, t := range result.Threads {
		if t.ThreadMetadata.Archived {
			continue
		}
		parentID := t.ParentID()
		if parentID == nil || *parentID != channelID {
			continue
		}
		createdAt := t.ThreadMetadata.CreateTimestamp
		if createdAt.IsZero() {
			createdAt = t.ID().Time()
		}
		threads = append(threads, threadInfo{
			Name:         t.Name(),
			ID:           t.ID(),
			MessageCount: t.MessageCount,
			MemberCount:  t.MemberCount,
			CreatedAt:    createdAt,
		})
	}

	if len(threads) == 0 {
		return nil
	}

	threadIDs := make([]snowflake.ID, len(threads))
	for i, t := range threads {
		threadIDs[i] = t.ID
	}
	b.refreshMemberCounts(guildID, threadIDs)

	sort.Slice(threads, func(i, j int) bool {
		return threads[i].MessageCount > threads[j].MessageCount
	})

	total := len(threads)
	if len(threads) > 10 {
		threads = threads[:10]
	}

	b.mu.Lock()
	cache := b.memberCounts[guildID]
	b.mu.Unlock()

	now := time.Now()
	const maxDescLen = 3900
	var desc strings.Builder
	for _, t := range threads {
		memberCount := t.MemberCount
		if cache != nil {
			if n, ok := cache.Counts[t.ID]; ok && n > 0 {
				memberCount = n
			}
		}
		age := formatAge(now.Sub(t.CreatedAt))
		line := fmt.Sprintf("- [%s](https://discord.com/channels/%d/%d) — %d msgs, %d members, %s old\n", t.Name, guildID, t.ID, t.MessageCount, memberCount, age)
		if desc.Len()+len(line) > maxDescLen {
			break
		}
		desc.WriteString(line)
	}
	if total > 10 {
		desc.WriteString(fmt.Sprintf("...and %d more threads\n", total-10))
	}

	color := 0x5865F2 // Discord blurple
	embed := discord.Embed{
		Title:       "Active Threads",
		Description: desc.String(),
		Color:       color,
		Footer: &discord.EmbedFooter{
			Text: fmt.Sprintf("%d active thread(s)", total),
		},
		Timestamp: &now,
	}
	return &embed
}

func (b *Bot) repostReminder(r *threadReminder) bool {
	embed := b.buildThreadEmbed(r.GuildID, r.ChannelID)
	if embed == nil {
		return false
	}

	// If our reminder is still the last message, edit in place instead of delete+repost
	if r.LastMessageID != 0 {
		msgs, err := b.Client.Rest.GetMessages(r.ChannelID, 0, 0, 0, 1)
		if err == nil && len(msgs) == 1 && msgs[0].ID == r.LastMessageID {
			_, err = b.Client.Rest.UpdateMessage(r.ChannelID, r.LastMessageID, discord.MessageUpdate{
				Embeds: &[]discord.Embed{*embed},
			})
			if err == nil {
				now := time.Now()
				b.mu.Lock()
				r.LastPostTime = now
				b.mu.Unlock()
				b.saveRemindersForGuild(r.GuildID)
				return true
			}
			// Edit failed; fall through to delete+repost
		}
	}

	// Delete old message (best-effort)
	if r.LastMessageID != 0 {
		_ = b.Client.Rest.DeleteMessage(r.ChannelID, r.LastMessageID)
	}

	msg := discord.MessageCreate{
		Embeds: []discord.Embed{*embed},
	}

	sent, err := botutil.PostWithRetry(b.Client.Rest, r.ChannelID, msg, b.Log)
	if err != nil {
		b.Log.Error("Failed to repost thread reminder after retries", "channel_id", r.ChannelID, "error", err)
		return false
	}

	now := time.Now()
	b.mu.Lock()
	r.LastMessageID = sent.ID
	r.LastPostTime = now
	b.mu.Unlock()
	b.saveRemindersForGuild(r.GuildID)
	return true
}

func formatAge(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	case d >= time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	default:
		mins := int(d.Minutes())
		if mins <= 1 {
			return "1 min"
		}
		return fmt.Sprintf("%d mins", mins)
	}
}

func (b *Bot) loadMemberCounts() {
	ctx := context.Background()
	for _, guildID := range botutil.GetGuildIDs(b.Env) {
		data, err := b.S3.FetchThreadMemberCounts(ctx, fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing thread member counts", "guild_id", guildID)
			continue
		}
		if err != nil {
			b.Log.Error("Failed to load thread member counts", "guild_id", guildID, "error", err)
			continue
		}

		var cache memberCountCache
		if err := json.Unmarshal(data, &cache); err != nil {
			b.Log.Error("Failed to decode thread member counts", "guild_id", guildID, "error", err)
			continue
		}
		if cache.Counts == nil {
			cache.Counts = make(map[snowflake.ID]int)
		}
		b.memberCounts[guildID] = &cache
		b.Log.Info("Loaded thread member counts", "guild_id", guildID, "count", len(cache.Counts))
	}
}

func (b *Bot) saveMemberCounts() {
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in thread member counts save", "error", r)
		}
	}()

	b.mu.Lock()
	guildIDs := make([]snowflake.ID, 0, len(b.memberCounts))
	for id := range b.memberCounts {
		guildIDs = append(guildIDs, id)
	}
	b.mu.Unlock()

	ctx := context.Background()
	for _, guildID := range guildIDs {
		b.mu.Lock()
		cache, ok := b.memberCounts[guildID]
		if !ok {
			b.mu.Unlock()
			continue
		}
		data, err := json.Marshal(cache)
		b.mu.Unlock()

		if err != nil {
			b.Log.Error("Failed to marshal thread member counts", "guild_id", guildID, "error", err)
			continue
		}

		if err := b.S3.SaveThreadMemberCounts(ctx, fmt.Sprintf("%d", guildID), data); err != nil {
			b.Log.Error("Failed to save thread member counts", "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Saved thread member counts", "guild_id", guildID)
		}
	}
}

func (b *Bot) refreshMemberCounts(guildID snowflake.ID, threadIDs []snowflake.ID) {
	b.mu.Lock()
	cache := b.memberCounts[guildID]
	if cache != nil && time.Since(cache.LastRefresh) < 24*time.Hour {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	counts := make(map[snowflake.ID]int, len(threadIDs))
	for _, threadID := range threadIDs {
		n := b.countThreadMembers(threadID)
		counts[threadID] = n
	}

	b.mu.Lock()
	b.memberCounts[guildID] = &memberCountCache{
		Counts:      counts,
		LastRefresh: time.Now(),
	}
	b.mu.Unlock()

	ctx := context.Background()
	b.mu.Lock()
	data, err := json.Marshal(b.memberCounts[guildID])
	b.mu.Unlock()
	if err != nil {
		b.Log.Error("Failed to marshal thread member counts", "guild_id", guildID, "error", err)
		return
	}
	if err := b.S3.SaveThreadMemberCounts(ctx, fmt.Sprintf("%d", guildID), data); err != nil {
		b.Log.Error("Failed to save thread member counts", "guild_id", guildID, "error", err)
	}
}

func (b *Bot) countThreadMembers(threadID snowflake.ID) int {
	page := b.Client.Rest.GetThreadMembersPage(threadID, 0, 100)
	total := 0
	for page.Next() {
		total += len(page.Items)
	}
	if page.Err != nil && !errors.Is(page.Err, rest.ErrNoMorePages) {
		b.Log.Error("Failed to count thread members", "thread_id", threadID, "error", page.Err)
	}
	return total
}
