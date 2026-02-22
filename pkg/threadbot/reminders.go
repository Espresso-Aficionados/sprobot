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
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

type threadReminder struct {
	msgCh  chan struct{} `json:"-"`
	stopCh chan struct{} `json:"-"`

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

type reminderExport struct {
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

func (r *threadReminder) toExport() reminderExport {
	return reminderExport{
		ChannelID:         r.ChannelID,
		GuildID:           r.GuildID,
		EnabledBy:         r.EnabledBy,
		Enabled:           r.Enabled,
		LastMessageID:     r.LastMessageID,
		LastPostTime:      r.LastPostTime,
		MinIdleMins:       r.MinIdleMins,
		MaxIdleMins:       r.MaxIdleMins,
		MsgThreshold:      r.MsgThreshold,
		TimeThresholdMins: r.TimeThresholdMins,
	}
}

func fromExport(e reminderExport) *threadReminder {
	r := &threadReminder{
		ChannelID:         e.ChannelID,
		GuildID:           e.GuildID,
		EnabledBy:         e.EnabledBy,
		Enabled:           e.Enabled,
		LastMessageID:     e.LastMessageID,
		LastPostTime:      e.LastPostTime,
		MinIdleMins:       e.MinIdleMins,
		MaxIdleMins:       e.MaxIdleMins,
		MsgThreshold:      e.MsgThreshold,
		TimeThresholdMins: e.TimeThresholdMins,
	}
	// Defaults for zero values (backwards compat with old S3 data)
	if r.MinIdleMins == 0 {
		r.MinIdleMins = 30
	}
	if r.MaxIdleMins == 0 {
		r.MaxIdleMins = 720
	}
	if r.MsgThreshold == 0 {
		r.MsgThreshold = 30
	}
	return r
}

func (b *Bot) loadReminders() {
	ctx := context.Background()
	for _, guildID := range getGuildIDs(b.env) {
		data, err := b.s3.FetchThreadReminders(ctx, fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.log.Info("No existing thread reminders data", "guild_id", guildID)
			continue
		}
		if err != nil {
			b.log.Error("Failed to load thread reminders", "guild_id", guildID, "error", err)
			continue
		}

		var exports map[string]reminderExport
		if err := json.Unmarshal(data, &exports); err != nil {
			b.log.Error("Failed to decode thread reminders", "guild_id", guildID, "error", err)
			continue
		}

		channels := make(map[snowflake.ID]*threadReminder, len(exports))
		for _, e := range exports {
			r := fromExport(e)
			channels[r.ChannelID] = r
			if r.Enabled {
				b.startReminderGoroutine(r)
			}
		}
		b.reminders[guildID] = channels
		b.log.Info("Loaded thread reminders", "guild_id", guildID, "count", len(channels))
	}
}

func (b *Bot) saveRemindersForGuild(guildID snowflake.ID) {
	channels, ok := b.reminders[guildID]
	if !ok {
		return
	}

	exports := make(map[string]reminderExport, len(channels))
	for chID, r := range channels {
		exports[fmt.Sprintf("%d", chID)] = r.toExport()
	}

	data, err := json.Marshal(exports)
	if err != nil {
		b.log.Error("Failed to marshal thread reminders", "guild_id", guildID, "error", err)
		return
	}

	ctx := context.Background()
	if err := b.s3.SaveThreadReminders(ctx, fmt.Sprintf("%d", guildID), data); err != nil {
		b.log.Error("Failed to save thread reminders", "guild_id", guildID, "error", err)
	} else {
		b.log.Info("Saved thread reminders", "guild_id", guildID, "count", len(exports))
	}
}

func (b *Bot) saveAllReminders() {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("Panic in thread reminders save", "error", r)
		}
	}()

	for guildID := range b.reminders {
		b.saveRemindersForGuild(guildID)
	}
}

func (b *Bot) reminderSaveLoop() {
	for !b.ready.Load() {
		time.Sleep(1 * time.Second)
	}

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		b.saveAllReminders()
	}
}

func (b *Bot) startReminderGoroutine(r *threadReminder) {
	r.msgCh = make(chan struct{}, 128)
	r.stopCh = make(chan struct{})
	go b.runReminderGoroutine(r)
}

func (b *Bot) stopReminderGoroutine(r *threadReminder) {
	if r.stopCh != nil {
		close(r.stopCh)
		r.stopCh = nil
		r.msgCh = nil
	}
}

func (b *Bot) stopAllReminderGoroutines() {
	for _, channels := range b.reminders {
		for _, r := range channels {
			b.stopReminderGoroutine(r)
		}
	}
}

func (b *Bot) runReminderGoroutine(r *threadReminder) {
	var msgsSinceLast int
	var idleArmed bool

	maxIdle := time.Duration(r.MaxIdleMins) * time.Minute
	minIdle := time.Duration(r.MinIdleMins) * time.Minute

	var timeThreshTimer *time.Timer
	if r.TimeThresholdMins > 0 {
		initialTimeThresh := time.Duration(r.TimeThresholdMins)*time.Minute - time.Since(r.LastPostTime)
		if initialTimeThresh <= 0 || r.LastPostTime.IsZero() {
			initialTimeThresh = time.Second
		}
		timeThreshTimer = time.NewTimer(initialTimeThresh)
	}

	var idleTimer *time.Timer
	var maxTimer *time.Timer

	defer func() {
		if maxTimer != nil {
			maxTimer.Stop()
		}
		if timeThreshTimer != nil {
			timeThreshTimer.Stop()
		}
		if idleTimer != nil {
			idleTimer.Stop()
		}
	}()

	timeThreshC := func() <-chan time.Time {
		if timeThreshTimer == nil {
			return nil
		}
		return timeThreshTimer.C
	}

	idleTimerC := func() <-chan time.Time {
		if idleTimer == nil {
			return nil
		}
		return idleTimer.C
	}

	maxTimerC := func() <-chan time.Time {
		if maxTimer == nil {
			return nil
		}
		return maxTimer.C
	}

	arm := func() {
		if idleArmed {
			return
		}
		idleArmed = true
		if idleTimer == nil {
			idleTimer = time.NewTimer(minIdle)
		} else {
			idleTimer.Reset(minIdle)
		}
		if maxTimer == nil {
			maxTimer = time.NewTimer(maxIdle)
		} else {
			maxTimer.Reset(maxIdle)
		}
	}

	resetAll := func() {
		msgsSinceLast = 0
		idleArmed = false
		if maxTimer != nil {
			maxTimer.Stop()
			maxTimer = nil
		}
		if timeThreshTimer != nil {
			if !timeThreshTimer.Stop() {
				select {
				case <-timeThreshTimer.C:
				default:
				}
			}
			timeThreshTimer.Reset(time.Duration(r.TimeThresholdMins) * time.Minute)
		}
		if idleTimer != nil {
			idleTimer.Stop()
			idleTimer = nil
		}
	}

	for {
		select {
		case <-r.stopCh:
			return

		case <-r.msgCh:
			msgsSinceLast++
			if idleArmed {
				if idleTimer != nil {
					if !idleTimer.Stop() {
						select {
						case <-idleTimer.C:
						default:
						}
					}
					idleTimer.Reset(minIdle)
				}
			} else if r.MsgThreshold > 0 && msgsSinceLast >= r.MsgThreshold {
				arm()
			}

		case <-timeThreshC():
			if msgsSinceLast >= 1 {
				arm()
			}

		case <-idleTimerC():
			if b.repostReminder(r) {
				resetAll()
			} else {
				idleTimer = nil
			}

		case <-maxTimerC():
			if msgsSinceLast >= 1 {
				if b.repostReminder(r) {
					resetAll()
				} else {
					maxTimer.Reset(maxIdle)
				}
			} else {
				maxTimer.Reset(maxIdle)
			}
		}
	}
}

// buildThreadEmbed fetches active threads for the given guild/channel and
// returns an embed showing the top 10 by message count. Returns nil if there
// are no active threads.
func (b *Bot) buildThreadEmbed(guildID, channelID snowflake.ID) *discord.Embed {
	result, err := b.client.Rest.GetActiveGuildThreads(guildID)
	if err != nil {
		b.log.Error("Failed to fetch active threads", "guild_id", guildID, "error", err)
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
		threads = append(threads, threadInfo{
			Name:         t.Name(),
			ID:           t.ID(),
			MessageCount: t.MessageCount,
			MemberCount:  t.MemberCount,
			CreatedAt:    t.ThreadMetadata.CreateTimestamp,
		})
	}

	if len(threads) == 0 {
		return nil
	}

	sort.Slice(threads, func(i, j int) bool {
		return threads[i].MessageCount > threads[j].MessageCount
	})

	total := len(threads)
	if len(threads) > 10 {
		threads = threads[:10]
	}

	now := time.Now()
	const maxDescLen = 3900
	var desc strings.Builder
	for _, t := range threads {
		age := formatAge(now.Sub(t.CreatedAt))
		line := fmt.Sprintf("- [%s](https://discord.com/channels/%d/%d) — %d msgs, %d members, %s old\n", t.Name, guildID, t.ID, t.MessageCount, t.MemberCount, age)
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

	// Delete old message (best-effort)
	if r.LastMessageID != 0 {
		_ = b.client.Rest.DeleteMessage(r.ChannelID, r.LastMessageID)
	}

	msg := discord.MessageCreate{
		Embeds: []discord.Embed{*embed},
	}

	var sent *discord.Message
	var err error
	for attempt := range 3 {
		sent, err = b.client.Rest.CreateMessage(r.ChannelID, msg)
		if err == nil {
			break
		}
		b.log.Warn("Repost attempt failed", "channel_id", r.ChannelID, "attempt", attempt+1, "error", err)
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	if err != nil {
		b.log.Error("Failed to repost thread reminder after retries", "channel_id", r.ChannelID, "error", err)
		return false
	}

	now := time.Now()
	r.LastMessageID = sent.ID
	r.LastPostTime = now
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
