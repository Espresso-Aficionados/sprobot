package stickybot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/idleloop"
	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
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

// messageCreate builds the message that posts s. Image embeds hold saved
// files as bare keys, which fileURL (s3client.Client.FileURL) turns into
// their web links; Discord rejects an embed image that isn't a URL. The
// message pings no one: mentions in it would ping again on every repost.
func (s *stickyMessage) messageCreate(fileURL func(string) string) discord.MessageCreate {
	embeds := make([]discord.Embed, len(s.Embeds))
	copy(embeds, s.Embeds)
	for i := range embeds {
		if embeds[i].Image != nil {
			embeds[i].Image = &discord.EmbedResource{URL: fileURL(embeds[i].Image.URL)}
		}
	}
	return discord.MessageCreate{
		Content:         s.Content,
		Embeds:          embeds,
		AllowedMentions: &discord.AllowedMentions{},
	}
}

// loadStickies loads the stickies of every allowed guild not yet loaded and
// starts their repost loops. Run calls it before the gateway opens, so no
// command or event sees a guild whose stickies aren't in memory yet. A guild
// that fails to load stays out of b.stickies; retryFailed calls this again,
// and guildLoaded retries it when it is next used.
func (b *Bot) loadStickies() {
	for _, guildID := range botutil.AllowedGuildIDs {
		// Bounded, since the bot isn't online until the boot load returns,
		// and per guild, so one slow fetch doesn't fail the others.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		b.guildLoaded(ctx, guildID)
		cancel()
	}
}

// retryFailed retries what failed against S3: loads, saves and file
// deletions.
func (b *Bot) retryFailed() {
	b.loadStickies()
	b.saveUnsavedStickies()
	b.retryFileDeletes()
}

// guildLoaded reports whether guildID's stickies are in memory, first
// retrying the load if it failed. Nothing may change a guild that isn't
// loaded: saving it would overwrite the stickies that failed to load.
func (b *Bot) guildLoaded(ctx context.Context, guildID snowflake.ID) bool {
	b.mu.Lock()
	_, loaded := b.stickies[guildID]
	b.mu.Unlock()
	return loaded || (botutil.GuildAllowed(guildID) && b.loadGuildStickies(ctx, guildID))
}

// loadGuildStickies reads guildID's stored stickies into b.stickies and
// starts their loops, reporting whether the guild is now loaded.
func (b *Bot) loadGuildStickies(ctx context.Context, guildID snowflake.ID) bool {
	channels, err := b.fetchStickies(ctx, guildID)
	if err != nil {
		b.Log.Error("Failed to load stickies; they can't be changed until a retry succeeds", "guild_id", guildID, "error", err)
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, loaded := b.stickies[guildID]; loaded {
		return true // a concurrent retry got there first
	}
	b.stickies[guildID] = channels
	for _, s := range channels {
		b.startStickyGoroutine(s)
	}
	b.Log.Info("Loaded stickies", "guild_id", guildID, "count", len(channels))
	return true
}

// fetchStickies reads guildID's stored stickies, keyed by channel. A guild
// with none stored gets an empty map.
func (b *Bot) fetchStickies(ctx context.Context, guildID snowflake.ID) (map[snowflake.ID]*stickyMessage, error) {
	channels := make(map[snowflake.ID]*stickyMessage)
	data, err := b.S3.FetchStickies(ctx, guildID.String())
	if errors.Is(err, s3client.ErrNotFound) {
		return channels, nil
	}
	if err != nil {
		return nil, err
	}
	var loaded map[string]*stickyMessage
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, fmt.Errorf("decoding stickies: %w", err)
	}
	for _, s := range loaded {
		channels[s.ChannelID] = s
	}
	return channels, nil
}

// saveTimeout bounds each save, which holds saveMu and so stalls every other
// save, and the reposts and stops that wait on them. A variable so tests can
// shorten it.
var saveTimeout = 30 * time.Second

// saveStickiesForGuild writes guildID's stickies to S3, unless the guild
// isn't loaded. Saves run one at a time, so the last one taken is the last
// one stored. A failed save is retried by saveUnsavedStickies.
func (b *Bot) saveStickiesForGuild(guildID snowflake.ID) {
	b.saveMu.Lock()
	defer b.saveMu.Unlock()

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

	ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
	defer cancel()
	if err := b.S3.SaveStickies(ctx, fmt.Sprintf("%d", guildID), data); err != nil {
		b.Log.Error("Failed to save stickies", "guild_id", guildID, "error", err)
		b.unsaved[guildID] = true
		return
	}
	delete(b.unsaved, guildID)
	b.Log.Info("Saved stickies", "guild_id", guildID, "count", len(toSave))
}

// saveUnsavedStickies retries the saves that failed. Every change saves its
// guild straight away, so these are the only guilds S3 is behind on. Saving
// the rest could only overwrite changes made elsewhere, such as by another
// instance using the same bucket.
func (b *Bot) saveUnsavedStickies() {
	b.saveMu.Lock()
	guildIDs := slices.Collect(maps.Keys(b.unsaved))
	b.saveMu.Unlock()

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

// retireSticky stops s's repost loop, then deletes its last posted message and
// its saved files. s must already be out of b.stickies (see
// stopStickyGoroutine).
func (b *Bot) retireSticky(s *stickyMessage) {
	b.stopStickyGoroutine(s)
	// Read only after the stop: a repost in flight replaces the message.
	b.mu.Lock()
	lastMessageID := s.LastMessageID
	b.mu.Unlock()
	if lastMessageID != 0 {
		_ = b.Client.Rest.DeleteMessage(s.ChannelID, lastMessageID)
	}
	b.deleteStickyFiles(s)
}

// deleteStickyFiles deletes s's saved files.
func (b *Bot) deleteStickyFiles(s *stickyMessage) {
	b.deleteFiles(s.fileKeys(b.S3.Origin() + "/" + b.S3.Bucket() + "/"))
}

// deleteFiles deletes the saved files at keys. Once their sticky is gone they
// are recorded nowhere else, so if that fails they are logged and kept for
// retryFileDeletes. No sticky refers to them again: each sticky has its own
// copies (see createSticky).
func (b *Bot) deleteFiles(keys []string) {
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := b.S3.DeleteKeys(ctx, keys...); err != nil {
		b.Log.Error("Failed to delete sticky files; will retry", "keys", keys, "error", err)
		b.mu.Lock()
		b.undeleted = append(b.undeleted, keys...)
		b.mu.Unlock()
		return
	}
	b.Log.Info("Deleted sticky files", "count", len(keys))
}

// retryFileDeletes retries the file deletions that failed.
func (b *Bot) retryFileDeletes() {
	b.mu.Lock()
	keys := b.undeleted
	b.undeleted = nil
	b.mu.Unlock()
	b.deleteFiles(keys)
}

// fileKeys returns the bucket keys of s's saved files. Stickies made before
// files were stored as keys hold the file's bucket URL instead: bucketURL
// followed by the path-escaped key. Only keys of this guild's sticky files
// are returned, so nothing else can be deleted through them.
func (s *stickyMessage) fileKeys(bucketURL string) []string {
	var keys []string
	for _, stored := range s.FileURLs {
		key, err := url.PathUnescape(strings.TrimPrefix(stored, bucketURL))
		if err != nil {
			continue
		}
		if kind, guildID, _, ok := sprobot.ParseFileKey(key); ok && kind == "sticky" && guildID == s.GuildID.String() {
			keys = append(keys, key)
		}
	}
	return keys
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

	sent, err := botutil.PostWithRetry(b.Client.Rest, s.ChannelID, s.messageCreate(b.S3.FileURL), b.Log)
	if rest.IsJSONErrorCode(err, rest.JSONErrorCodeUnknownChannel) {
		// Deleted without an event this bot saw: while it was offline, or a
		// thread whose parent was deleted. In a goroutine, since dropping the
		// sticky stops this loop, which waits for this repost to return.
		go b.dropDeletedChannelSticky(s.GuildID, s.ChannelID)
		return false
	}
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
