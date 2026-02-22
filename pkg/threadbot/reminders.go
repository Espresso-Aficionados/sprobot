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

	ChannelID     snowflake.ID `json:"channel_id"`
	GuildID       snowflake.ID `json:"guild_id"`
	EnabledBy     snowflake.ID `json:"enabled_by"`
	Enabled       bool         `json:"enabled"`
	LastMessageID snowflake.ID `json:"last_message_id"`
	LastPostTime  time.Time    `json:"last_post_time"`
	MinDwellMins  int          `json:"min_dwell_mins"`
	MaxDwellMins  int          `json:"max_dwell_mins"`
	MsgThreshold  int          `json:"msg_threshold"`
}

type reminderExport struct {
	ChannelID     snowflake.ID `json:"channel_id"`
	GuildID       snowflake.ID `json:"guild_id"`
	EnabledBy     snowflake.ID `json:"enabled_by"`
	Enabled       bool         `json:"enabled"`
	LastMessageID snowflake.ID `json:"last_message_id"`
	LastPostTime  time.Time    `json:"last_post_time"`
	MinDwellMins  int          `json:"min_dwell_mins"`
	MaxDwellMins  int          `json:"max_dwell_mins"`
	MsgThreshold  int          `json:"msg_threshold"`
}

func (r *threadReminder) toExport() reminderExport {
	return reminderExport{
		ChannelID:     r.ChannelID,
		GuildID:       r.GuildID,
		EnabledBy:     r.EnabledBy,
		Enabled:       r.Enabled,
		LastMessageID: r.LastMessageID,
		LastPostTime:  r.LastPostTime,
		MinDwellMins:  r.MinDwellMins,
		MaxDwellMins:  r.MaxDwellMins,
		MsgThreshold:  r.MsgThreshold,
	}
}

func fromExport(e reminderExport) *threadReminder {
	return &threadReminder{
		ChannelID:     e.ChannelID,
		GuildID:       e.GuildID,
		EnabledBy:     e.EnabledBy,
		Enabled:       e.Enabled,
		LastMessageID: e.LastMessageID,
		LastPostTime:  e.LastPostTime,
		MinDwellMins:  e.MinDwellMins,
		MaxDwellMins:  e.MaxDwellMins,
		MsgThreshold:  e.MsgThreshold,
	}
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

	maxDwell := time.Duration(r.MaxDwellMins) * time.Minute
	minDwell := time.Duration(r.MinDwellMins) * time.Minute

	// Calculate initial max dwell based on time since last post
	initialMax := maxDwell - time.Since(r.LastPostTime)
	if initialMax <= 0 || r.LastPostTime.IsZero() {
		initialMax = time.Second
	}

	maxTimer := time.NewTimer(initialMax)
	var minTimer *time.Timer

	defer func() {
		maxTimer.Stop()
		if minTimer != nil {
			minTimer.Stop()
		}
	}()

	minTimerC := func() <-chan time.Time {
		if minTimer == nil {
			return nil
		}
		return minTimer.C
	}

	for {
		select {
		case <-r.stopCh:
			return

		case <-r.msgCh:
			msgsSinceLast++
			if minTimer == nil {
				minTimer = time.NewTimer(minDwell)
			}

		case <-minTimerC():
			if msgsSinceLast >= r.MsgThreshold {
				if b.repostReminder(r) {
					msgsSinceLast = 0
					if !maxTimer.Stop() {
						select {
						case <-maxTimer.C:
						default:
						}
					}
					maxTimer.Reset(maxDwell)
				}
			}
			minTimer = nil

		case <-maxTimer.C:
			if msgsSinceLast >= 1 {
				if b.repostReminder(r) {
					msgsSinceLast = 0
				}
			}
			maxTimer.Reset(maxDwell)
			if minTimer != nil {
				minTimer.Stop()
				minTimer = nil
			}
		}
	}
}

func (b *Bot) repostReminder(r *threadReminder) bool {
	result, err := b.client.Rest.GetActiveGuildThreads(r.GuildID)
	if err != nil {
		b.log.Error("Failed to fetch active threads", "guild_id", r.GuildID, "error", err)
		return false
	}

	// Filter threads belonging to this channel
	type threadInfo struct {
		Name string
		ID   snowflake.ID
	}
	var threads []threadInfo
	for _, t := range result.Threads {
		if t.ThreadMetadata.Archived {
			continue
		}
		parentID := t.ParentID()
		if parentID == nil || *parentID != r.ChannelID {
			continue
		}
		threads = append(threads, threadInfo{Name: t.Name(), ID: t.ID()})
	}

	if len(threads) == 0 {
		return false
	}

	sort.Slice(threads, func(i, j int) bool {
		return threads[i].Name < threads[j].Name
	})

	// Build description with thread links
	const maxDescLen = 3900
	var desc strings.Builder
	var truncated int
	for i, t := range threads {
		line := fmt.Sprintf("- [%s](https://discord.com/channels/%d/%d)\n", t.Name, r.GuildID, t.ID)
		if desc.Len()+len(line) > maxDescLen {
			truncated = len(threads) - i
			break
		}
		desc.WriteString(line)
	}
	if truncated > 0 {
		desc.WriteString(fmt.Sprintf("...and %d more threads\n", truncated))
	}

	now := time.Now()
	color := 0x5865F2 // Discord blurple
	embed := discord.Embed{
		Title:       "Active Threads",
		Description: desc.String(),
		Color:       color,
		Footer: &discord.EmbedFooter{
			Text: fmt.Sprintf("%d active thread(s)", len(threads)),
		},
		Timestamp: &now,
	}

	// Delete old message (best-effort)
	if r.LastMessageID != 0 {
		_ = b.client.Rest.DeleteMessage(r.ChannelID, r.LastMessageID)
	}

	msg := discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	}

	var sent *discord.Message
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

	r.LastMessageID = sent.ID
	r.LastPostTime = now
	b.saveRemindersForGuild(r.GuildID)
	return true
}
