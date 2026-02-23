package stickybot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
}

func (s *stickyMessage) applyDefaults() {
	if s.MinIdleMins == 0 {
		s.MinIdleMins = 15
	}
	if s.MaxIdleMins == 0 {
		s.MaxIdleMins = 30
	}
	if s.MsgThreshold == 0 {
		s.MsgThreshold = 30
	}
}

func (b *Bot) loadStickies() {
	ctx := context.Background()
	for _, guildID := range botutil.GetGuildIDs(b.Env) {
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
			s.applyDefaults()
			channels[s.ChannelID] = s
			if s.Active {
				b.startStickyGoroutine(s)
			}
		}
		b.stickies[guildID] = channels
		b.Log.Info("Loaded stickies", "guild_id", guildID, "count", len(channels))
	}
}

func (b *Bot) saveStickiesForGuild(guildID snowflake.ID) {
	channels, ok := b.stickies[guildID]
	if !ok {
		return
	}

	toSave := make(map[string]*stickyMessage, len(channels))
	for chID, s := range channels {
		toSave[fmt.Sprintf("%d", chID)] = s
	}

	data, err := json.Marshal(toSave)
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
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in stickies save", "error", r)
		}
	}()

	for guildID := range b.stickies {
		b.saveStickiesForGuild(guildID)
	}
}

func (b *Bot) startStickyGoroutine(s *stickyMessage) {
	s.handle = idleloop.NewHandle()
	s.handle.Start(idleloop.Config{
		MinIdleMins:       s.MinIdleMins,
		MaxIdleMins:       s.MaxIdleMins,
		MsgThreshold:      s.MsgThreshold,
		TimeThresholdMins: s.TimeThresholdMins,
	}, func() bool { return b.repostSticky(s) })
}

func (b *Bot) stopStickyGoroutine(s *stickyMessage) {
	s.handle.Stop()
	s.handle = nil
}

func (b *Bot) stopAllStickyGoroutines() {
	for _, channels := range b.stickies {
		for _, s := range channels {
			s.handle.Stop()
			s.handle = nil
		}
	}
}

func (b *Bot) repostSticky(s *stickyMessage) bool {
	// Delete old message (best-effort)
	if s.LastMessageID != 0 {
		_ = b.Client.Rest.DeleteMessage(s.ChannelID, s.LastMessageID)
	}

	msg := discord.MessageCreate{
		Content: s.Content,
		Embeds:  s.Embeds,
	}

	sent, err := botutil.PostWithRetry(b.Client.Rest, s.ChannelID, msg, b.Log)
	if err != nil {
		b.Log.Error("Failed to repost sticky after retries", "channel_id", s.ChannelID, "error", err)
		return false
	}

	s.LastMessageID = sent.ID
	b.saveStickiesForGuild(s.GuildID)
	return true
}
