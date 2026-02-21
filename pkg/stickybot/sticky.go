package stickybot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

type stickyMessage struct {
	msgCh  chan struct{} `json:"-"` // buffered(128), message signal from onMessage
	stopCh chan struct{} `json:"-"` // closed to stop the goroutine

	ChannelID     snowflake.ID    `json:"channel_id"`
	GuildID       snowflake.ID    `json:"guild_id"`
	Content       string          `json:"content"`
	Embeds        []discord.Embed `json:"embeds,omitempty"`
	FileURLs      []string        `json:"file_urls,omitempty"`
	CreatedBy     snowflake.ID    `json:"created_by"`
	Active        bool            `json:"active"`
	LastMessageID snowflake.ID    `json:"last_message_id"`
	MinDwellMins  int             `json:"min_dwell_mins"`
	MaxDwellMins  int             `json:"max_dwell_mins"`
	MsgThreshold  int             `json:"msg_threshold"`
}

// stickyExport is used for JSON serialization since stickyMessage has unexported fields.
type stickyExport struct {
	ChannelID     snowflake.ID    `json:"channel_id"`
	GuildID       snowflake.ID    `json:"guild_id"`
	Content       string          `json:"content"`
	Embeds        []discord.Embed `json:"embeds,omitempty"`
	FileURLs      []string        `json:"file_urls,omitempty"`
	CreatedBy     snowflake.ID    `json:"created_by"`
	Active        bool            `json:"active"`
	LastMessageID snowflake.ID    `json:"last_message_id"`
	MinDwellMins  int             `json:"min_dwell_mins"`
	MaxDwellMins  int             `json:"max_dwell_mins"`
	MsgThreshold  int             `json:"msg_threshold"`
}

func (s *stickyMessage) toExport() stickyExport {
	return stickyExport{
		ChannelID:     s.ChannelID,
		GuildID:       s.GuildID,
		Content:       s.Content,
		Embeds:        s.Embeds,
		FileURLs:      s.FileURLs,
		CreatedBy:     s.CreatedBy,
		Active:        s.Active,
		LastMessageID: s.LastMessageID,
		MinDwellMins:  s.MinDwellMins,
		MaxDwellMins:  s.MaxDwellMins,
		MsgThreshold:  s.MsgThreshold,
	}
}

func fromExport(e stickyExport) *stickyMessage {
	return &stickyMessage{
		ChannelID:     e.ChannelID,
		GuildID:       e.GuildID,
		Content:       e.Content,
		Embeds:        e.Embeds,
		FileURLs:      e.FileURLs,
		CreatedBy:     e.CreatedBy,
		Active:        e.Active,
		LastMessageID: e.LastMessageID,
		MinDwellMins:  e.MinDwellMins,
		MaxDwellMins:  e.MaxDwellMins,
		MsgThreshold:  e.MsgThreshold,
	}
}

func (b *Bot) loadStickies() {
	ctx := context.Background()
	for _, guildID := range getGuildIDs(b.env) {
		data, err := b.s3.FetchStickies(ctx, fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.log.Info("No existing stickies data", "guild_id", guildID)
			continue
		}
		if err != nil {
			b.log.Error("Failed to load stickies", "guild_id", guildID, "error", err)
			continue
		}

		var exports map[string]stickyExport
		if err := json.Unmarshal(data, &exports); err != nil {
			b.log.Error("Failed to decode stickies", "guild_id", guildID, "error", err)
			continue
		}

		channels := make(map[snowflake.ID]*stickyMessage, len(exports))
		for _, e := range exports {
			s := fromExport(e)
			channels[s.ChannelID] = s
			if s.Active {
				b.startStickyGoroutine(s)
			}
		}
		b.stickies[guildID] = channels
		b.log.Info("Loaded stickies", "guild_id", guildID, "count", len(channels))
	}
}

func (b *Bot) saveStickiesForGuild(guildID snowflake.ID) {
	channels, ok := b.stickies[guildID]
	if !ok {
		return
	}

	exports := make(map[string]stickyExport, len(channels))
	for chID, s := range channels {
		exports[fmt.Sprintf("%d", chID)] = s.toExport()
	}

	data, err := json.Marshal(exports)
	if err != nil {
		b.log.Error("Failed to marshal stickies", "guild_id", guildID, "error", err)
		return
	}

	ctx := context.Background()
	if err := b.s3.SaveStickies(ctx, fmt.Sprintf("%d", guildID), data); err != nil {
		b.log.Error("Failed to save stickies", "guild_id", guildID, "error", err)
	} else {
		b.log.Info("Saved stickies", "guild_id", guildID, "count", len(exports))
	}
}

func (b *Bot) saveAllStickies() {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("Panic in stickies save", "error", r)
		}
	}()

	for guildID := range b.stickies {
		b.saveStickiesForGuild(guildID)
	}
}

func (b *Bot) stickySaveLoop() {
	for !b.ready.Load() {
		time.Sleep(1 * time.Second)
	}

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		b.saveAllStickies()
	}
}

// startStickyGoroutine creates channels and launches the per-sticky goroutine.
func (b *Bot) startStickyGoroutine(s *stickyMessage) {
	s.msgCh = make(chan struct{}, 128)
	s.stopCh = make(chan struct{})
	go b.runStickyGoroutine(s)
}

// stopStickyGoroutine signals the goroutine to exit by closing stopCh.
func (b *Bot) stopStickyGoroutine(s *stickyMessage) {
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
		s.msgCh = nil
	}
}

func (b *Bot) stopAllStickyGoroutines() {
	for _, channels := range b.stickies {
		for _, s := range channels {
			b.stopStickyGoroutine(s)
		}
	}
}

// runStickyGoroutine is the per-sticky select loop. It owns all mutable runtime
// state (message count, timers) and reposts when dwell conditions are met.
func (b *Bot) runStickyGoroutine(s *stickyMessage) {
	var msgsSinceLast int

	maxDwell := time.Duration(s.MaxDwellMins) * time.Minute
	minDwell := time.Duration(s.MinDwellMins) * time.Minute

	maxTimer := time.NewTimer(maxDwell)
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
		case <-s.stopCh:
			return

		case <-s.msgCh:
			msgsSinceLast++
			if minTimer == nil {
				minTimer = time.NewTimer(minDwell)
			}

		case <-minTimerC():
			if msgsSinceLast >= s.MsgThreshold {
				if b.repostSticky(s) {
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
				if b.repostSticky(s) {
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

func (b *Bot) repostSticky(s *stickyMessage) bool {
	// Delete old message (best-effort)
	if s.LastMessageID != 0 {
		_ = b.client.Rest.DeleteMessage(s.ChannelID, s.LastMessageID)
	}

	msg := discord.MessageCreate{
		Content: s.Content,
		Embeds:  s.Embeds,
	}

	var sent *discord.Message
	var err error
	for attempt := range 3 {
		sent, err = b.client.Rest.CreateMessage(s.ChannelID, msg)
		if err == nil {
			break
		}
		b.log.Warn("Repost attempt failed", "channel_id", s.ChannelID, "attempt", attempt+1, "error", err)
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	if err != nil {
		b.log.Error("Failed to repost sticky after retries", "channel_id", s.ChannelID, "error", err)
		return false
	}

	s.LastMessageID = sent.ID
	b.saveStickiesForGuild(s.GuildID)
	return true
}
