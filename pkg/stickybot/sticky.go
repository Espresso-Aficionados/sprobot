package stickybot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

type stickyMessage struct {
	mu            sync.Mutex      `json:"-"`
	ChannelID     snowflake.ID    `json:"channel_id"`
	GuildID       snowflake.ID    `json:"guild_id"`
	Content       string          `json:"content"`
	Embeds        []discord.Embed `json:"embeds,omitempty"`
	FileURLs      []string        `json:"file_urls,omitempty"`
	CreatedBy     snowflake.ID    `json:"created_by"`
	Active        bool            `json:"active"`
	LastMessageID snowflake.ID    `json:"last_message_id"`
	DelaySeconds  int             `json:"delay_seconds"`
	MsgThreshold  int             `json:"msg_threshold"`
	LastPostTime  time.Time       `json:"-"`
	MsgsSinceLast int             `json:"-"`
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
	DelaySeconds  int             `json:"delay_seconds"`
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
		DelaySeconds:  s.DelaySeconds,
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
		DelaySeconds:  e.DelaySeconds,
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
		s.mu.Lock()
		exports[fmt.Sprintf("%d", chID)] = s.toExport()
		s.mu.Unlock()
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

func (b *Bot) repostSticky(s *stickyMessage, restClient rest.Rest) {
	// Delete old message (best-effort)
	if s.LastMessageID != 0 {
		_ = restClient.DeleteMessage(s.ChannelID, s.LastMessageID)
	}

	msg := discord.MessageCreate{
		Content: s.Content,
		Embeds:  s.Embeds,
	}

	sent, err := restClient.CreateMessage(s.ChannelID, msg)
	if err != nil {
		b.log.Error("Failed to repost sticky", "channel_id", s.ChannelID, "error", err)
		return
	}

	s.LastMessageID = sent.ID
	s.LastPostTime = time.Now()
	s.MsgsSinceLast = 0
}
