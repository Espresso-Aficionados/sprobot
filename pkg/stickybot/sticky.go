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

	ChannelID         snowflake.ID    `json:"channel_id"`
	GuildID           snowflake.ID    `json:"guild_id"`
	Content           string          `json:"content"`
	Embeds            []discord.Embed `json:"embeds,omitempty"`
	FileURLs          []string        `json:"file_urls,omitempty"`
	CreatedBy         snowflake.ID    `json:"created_by"`
	Active            bool            `json:"active"`
	LastMessageID     snowflake.ID    `json:"last_message_id"`
	MinIdleMins       int             `json:"min_idle_mins"`
	MaxIdleMins       int             `json:"max_idle_mins"`
	MsgThreshold      int             `json:"msg_threshold"`
	TimeThresholdMins int             `json:"time_threshold_mins"`
}

// stickyExport is used for JSON serialization since stickyMessage has unexported fields.
type stickyExport struct {
	ChannelID         snowflake.ID    `json:"channel_id"`
	GuildID           snowflake.ID    `json:"guild_id"`
	Content           string          `json:"content"`
	Embeds            []discord.Embed `json:"embeds,omitempty"`
	FileURLs          []string        `json:"file_urls,omitempty"`
	CreatedBy         snowflake.ID    `json:"created_by"`
	Active            bool            `json:"active"`
	LastMessageID     snowflake.ID    `json:"last_message_id"`
	MinIdleMins       int             `json:"min_idle_mins"`
	MaxIdleMins       int             `json:"max_idle_mins"`
	MsgThreshold      int             `json:"msg_threshold"`
	TimeThresholdMins int             `json:"time_threshold_mins"`
}

func (s *stickyMessage) toExport() stickyExport {
	return stickyExport{
		ChannelID:         s.ChannelID,
		GuildID:           s.GuildID,
		Content:           s.Content,
		Embeds:            s.Embeds,
		FileURLs:          s.FileURLs,
		CreatedBy:         s.CreatedBy,
		Active:            s.Active,
		LastMessageID:     s.LastMessageID,
		MinIdleMins:       s.MinIdleMins,
		MaxIdleMins:       s.MaxIdleMins,
		MsgThreshold:      s.MsgThreshold,
		TimeThresholdMins: s.TimeThresholdMins,
	}
}

func fromExport(e stickyExport) *stickyMessage {
	s := &stickyMessage{
		ChannelID:         e.ChannelID,
		GuildID:           e.GuildID,
		Content:           e.Content,
		Embeds:            e.Embeds,
		FileURLs:          e.FileURLs,
		CreatedBy:         e.CreatedBy,
		Active:            e.Active,
		LastMessageID:     e.LastMessageID,
		MinIdleMins:       e.MinIdleMins,
		MaxIdleMins:       e.MaxIdleMins,
		MsgThreshold:      e.MsgThreshold,
		TimeThresholdMins: e.TimeThresholdMins,
	}
	// Defaults for zero values (backwards compat with old S3 data)
	if s.MinIdleMins == 0 {
		s.MinIdleMins = 15
	}
	if s.MaxIdleMins == 0 {
		s.MaxIdleMins = 30
	}
	if s.MsgThreshold == 0 {
		s.MsgThreshold = 30
	}
	return s
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

// runStickyGoroutine is the per-sticky select loop. It watches for channel
// idle time and reposts during natural lulls in conversation.
//
// The idle trigger is "armed" by either MsgThreshold messages arriving or
// TimeThresholdMins elapsing (whichever comes first). Once armed, the idle
// timer (MinIdleMins) resets on each new message. When the idle timer fires
// (no messages for MinIdleMins), the sticky is reposted. MaxIdleMins starts
// counting from the moment of arming and forces a repost after that duration.
func (b *Bot) runStickyGoroutine(s *stickyMessage) {
	var msgsSinceLast int
	var idleArmed bool

	maxIdle := time.Duration(s.MaxIdleMins) * time.Minute
	minIdle := time.Duration(s.MinIdleMins) * time.Minute

	var timeThreshTimer *time.Timer
	if s.TimeThresholdMins > 0 {
		timeThreshTimer = time.NewTimer(time.Duration(s.TimeThresholdMins) * time.Minute)
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
			timeThreshTimer.Reset(time.Duration(s.TimeThresholdMins) * time.Minute)
		}
		if idleTimer != nil {
			idleTimer.Stop()
			idleTimer = nil
		}
	}

	for {
		select {
		case <-s.stopCh:
			return

		case <-s.msgCh:
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
			} else if s.MsgThreshold > 0 && msgsSinceLast >= s.MsgThreshold {
				arm()
			}

		case <-timeThreshC():
			if msgsSinceLast >= 1 {
				arm()
			}

		case <-idleTimerC():
			if b.repostSticky(s) {
				resetAll()
			} else {
				idleTimer = nil
			}

		case <-maxTimerC():
			if msgsSinceLast >= 1 {
				if b.repostSticky(s) {
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
