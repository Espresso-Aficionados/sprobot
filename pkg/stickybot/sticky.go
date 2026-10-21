package stickybot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/idleloop"
	"github.com/sadbox/sprobot/pkg/s3client"
)

type stickyMessage struct {
	handle *idleloop.Handle `json:"-"`

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
	Buffer            int             `json:"buffer"`
}

func (b *Bot) loadStickies() {
	ctx := context.Background()
	for _, guildID := range b.GuildIDs() {
		data, err := b.S3.FetchStickies(ctx, fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing stickies data", "guild_id", guildID)
			continue
		}
		if err != nil {
			b.Log.Error("Failed to load stickies", "guild_id", guildID, "error", err)
			continue
		}

		var loaded map[string]*stickyMessage
		if err := json.Unmarshal(data, &loaded); err != nil {
			b.Log.Error("Failed to decode stickies", "guild_id", guildID, "error", err)
			continue
		}

		channels := make(map[snowflake.ID]*stickyMessage, len(loaded))
		for _, s := range loaded {
			channels[s.ChannelID] = s
		}
		// The gateway is already dispatching events that read b.stickies.
		b.mu.Lock()
		b.stickies[guildID] = channels
		for _, s := range channels {
			b.startStickyGoroutine(s)
		}
		b.mu.Unlock()
		b.Log.Info("Loaded stickies", "guild_id", guildID, "count", len(channels))
	}
}

func (b *Bot) saveStickiesForGuild(guildID snowflake.ID) {
	b.mu.Lock()
	channels, ok := b.stickies[guildID]
	if !ok {
		b.mu.Unlock()
		return
	}
	toSave := make(map[string]*stickyMessage, len(channels))
	for chID, s := range channels {
		toSave[fmt.Sprintf("%d", chID)] = s
	}
	data, err := json.Marshal(toSave)
	b.mu.Unlock()

	if err != nil {
		b.Log.Error("Failed to marshal stickies", "guild_id", guildID, "error", err)
		return
	}

	ctx := context.Background()
	if err := b.S3.SaveStickies(ctx, fmt.Sprintf("%d", guildID), data); err != nil {
		b.Log.Error("Failed to save stickies", "guild_id", guildID, "error", err)
	} else {
		b.Log.Info("Saved stickies", "guild_id", guildID, "count", len(toSave))
	}
}

func (b *Bot) saveAllStickies() {
	b.mu.Lock()
	guildIDs := make([]snowflake.ID, 0, len(b.stickies))
	for id := range b.stickies {
		guildIDs = append(guildIDs, id)
	}
	b.mu.Unlock()

	for _, guildID := range guildIDs {
		b.saveStickiesForGuild(guildID)
	}
}

// startStickyGoroutine starts s's repost loop, unless s is no longer its
// channel's active sticky or the bot is shutting down. The caller holds b.mu,
// so the loop starts in the same critical section as the change that calls
// for it. Any earlier loop of s is stopped, and the new one runs only once it
// has exited, so s never has two reposts in flight.
func (b *Bot) startStickyGoroutine(s *stickyMessage) {
	select {
	case <-b.stop:
		return
	default:
	}
	if !s.Active || b.getSticky(s.GuildID, s.ChannelID) != s {
		return
	}
	var lastPostTime time.Time
	if s.LastMessageID != 0 {
		lastPostTime = s.LastMessageID.Time()
	}
	prev := s.handle
	s.handle = idleloop.NewHandle()
	s.handle.StartAfter(prev, idleloop.Config{
		MinIdleMins:       s.MinIdleMins,
		MaxIdleMins:       s.MaxIdleMins,
		MsgThreshold:      s.MsgThreshold,
		TimeThresholdMins: s.TimeThresholdMins,
		LastPostTime:      lastPostTime,
	}, func() bool { return b.repostSticky(s) })
}

// stopStickyGoroutine stops s's repost loop and waits out any repost in
// progress, so s.LastMessageID is final when it returns. The caller must not
// hold b.mu, which the repost takes, and must already have taken s out of
// b.stickies, so no new loop can start for it. To pause a sticky that stays,
// stop the handle read in the critical section that deactivates it instead.
func (b *Bot) stopStickyGoroutine(s *stickyMessage) {
	b.mu.Lock()
	h := s.handle
	b.mu.Unlock()
	h.Stop()
}

func (b *Bot) stopAllStickyGoroutines() {
	b.mu.Lock()
	var handles []*idleloop.Handle
	for _, channels := range b.stickies {
		for _, s := range channels {
			// Left in place: a command stopping the same sticky meanwhile
			// must wait for this goroutine too.
			handles = append(handles, s.handle)
		}
	}
	b.mu.Unlock()

	// Stop in parallel so shutdown waits for the slowest in-flight repost,
	// not the sum of them.
	var wg sync.WaitGroup
	for _, h := range handles {
		wg.Go(h.Stop)
	}
	wg.Wait()
}

func (b *Bot) repostSticky(s *stickyMessage) bool {
	// Skip repost if the sticky is still within the last N messages (buffer).
	if s.LastMessageID != 0 && s.Buffer > 0 {
		msgs, err := b.Client.Rest.GetMessages(s.ChannelID, 0, 0, 0, s.Buffer)
		if err == nil {
			for _, m := range msgs {
				if m.ID == s.LastMessageID {
					b.Log.Info("Repost skipped, sticky within buffer", "channel_id", s.ChannelID, "guild_id", s.GuildID, "buffer", s.Buffer)
					return true
				}
			}
		}
	}

	// Skip repost if only bot messages were posted after the sticky.
	// This prevents repost loops when multiple bots are active in the same channel.
	if s.LastMessageID != 0 && botutil.OnlyBotsAfter(b.Client.Rest, s.ChannelID, s.LastMessageID, b.Log) {
		b.Log.Debug("Repost skipped, only bot messages after sticky", "channel_id", s.ChannelID, "guild_id", s.GuildID)
		return true
	}

	// Delete old message (best-effort)
	if s.LastMessageID != 0 {
		_ = b.Client.Rest.DeleteMessage(s.ChannelID, s.LastMessageID)
	}

	embeds := make([]discord.Embed, len(s.Embeds))
	copy(embeds, s.Embeds)
	for i := range embeds {
		if embeds[i].Image != nil {
			embeds[i].Image = &discord.EmbedResource{
				URL: b.S3.FileURL(embeds[i].Image.URL),
			}
		}
	}

	msg := discord.MessageCreate{
		Content: s.Content,
		Embeds:  embeds,
	}

	sent, err := botutil.PostWithRetry(b.Client.Rest, s.ChannelID, msg, b.Log)
	if err != nil {
		b.Log.Error("Failed to repost sticky after retries", "channel_id", s.ChannelID, "error", err)
		return false
	}

	b.mu.Lock()
	s.LastMessageID = sent.ID
	b.mu.Unlock()
	b.saveStickiesForGuild(s.GuildID)
	return true
}
