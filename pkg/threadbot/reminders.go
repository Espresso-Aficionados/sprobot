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
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/idleloop"
	"github.com/sadbox/sprobot/pkg/s3client"
)

// memberCountCap is where Discord's thread member_count stops counting. Below
// it member_count is shown as is; only threads at the cap need an exact count
// from List Thread Members.
const memberCountCap = 50

const (
	// memberCountTTL is how long an exact count is used when thread member
	// events may have been missed since it was fetched (it predates the
	// current gateway session).
	memberCountTTL = 24 * time.Hour
	// memberCountLiveTTL is how long an exact count is used when the gateway
	// session has been unbroken since it was fetched, so every join and leave
	// event has been applied. The full re-fetch still corrects changes Discord
	// sends no event for.
	memberCountLiveTTL = 7 * 24 * time.Hour
	memberCountRetain  = 7 * 24 * time.Hour // drop counts not re-fetched for this long
)

// memberCountCache holds exact member counts for threads at memberCountCap,
// each with the time it was fetched.
type memberCountCache struct {
	Counts  map[snowflake.ID]int       `json:"counts"`
	Fetched map[snowflake.ID]time.Time `json:"fetched"`
}

func newMemberCountCache() *memberCountCache {
	return &memberCountCache{Counts: make(map[snowflake.ID]int), Fetched: make(map[snowflake.ID]time.Time)}
}

// needsFetch returns the threads whose exact count is missing or expired.
// Counts fetched during the current gateway session (at or after
// sessionStart) last memberCountLiveTTL; older ones last memberCountTTL.
func (c *memberCountCache) needsFetch(threadIDs []snowflake.ID, now, sessionStart time.Time) []snowflake.ID {
	var out []snowflake.ID
	for _, id := range threadIDs {
		fetched, ok := c.Fetched[id]
		if !ok || now.Sub(fetched) >= countTTL(fetched, sessionStart) {
			out = append(out, id)
		}
	}
	return out
}

func countTTL(fetched, sessionStart time.Time) time.Duration {
	if !sessionStart.IsZero() && !fetched.Before(sessionStart) {
		return memberCountLiveTTL
	}
	return memberCountTTL
}

func (c *memberCountCache) set(threadID snowflake.ID, n int, now time.Time) {
	c.Counts[threadID] = n
	c.Fetched[threadID] = now
}

// prune drops counts not fetched within memberCountRetain: threads that were
// archived, fell below the cap, or stopped being shown.
func (c *memberCountCache) prune(now time.Time) {
	for id := range c.Counts {
		if fetched, ok := c.Fetched[id]; !ok || now.Sub(fetched) >= memberCountRetain {
			delete(c.Counts, id)
			delete(c.Fetched, id)
		}
	}
	for id := range c.Fetched {
		if _, ok := c.Counts[id]; !ok {
			delete(c.Fetched, id)
		}
	}
}

// exactCount returns the cached exact count for a thread whose member_count is
// at the cap. Callers hold b.mu.
func (c *memberCountCache) exactCount(threadID snowflake.ID, memberCount int) (int, bool) {
	if c == nil || memberCount < memberCountCap {
		return 0, false
	}
	n, ok := c.Counts[threadID]
	return n, ok && n > 0
}

// adjust applies a join (+1) or leave (-1) seen on the gateway, keeping counts
// current between REST refreshes. Uncached threads are left for the
// next refresh to fetch, and a cached 0 means the fetch failed, so neither is
// adjusted. Missed or duplicated events drift until the next refresh.
func (c *memberCountCache) adjust(threadID snowflake.ID, delta int) bool {
	n, ok := c.Counts[threadID]
	if !ok || n <= 0 {
		return false
	}
	c.Counts[threadID] = max(n+delta, 1)
	return true
}

// onSessionReady records when the current gateway session began. Join and
// leave events are complete only from then on: a resume replays missed
// events, but a new session does not.
func (b *Bot) onSessionReady(*events.Ready) {
	b.mu.Lock()
	b.sessionStart = time.Now()
	b.mu.Unlock()
}

func (b *Bot) onThreadMemberAdd(e *events.ThreadMemberAdd) {
	b.adjustMemberCount(e.GuildID, e.ThreadID, 1)
}

func (b *Bot) onThreadMemberRemove(e *events.ThreadMemberRemove) {
	b.adjustMemberCount(e.GuildID, e.ThreadID, -1)
}

func (b *Bot) adjustMemberCount(guildID, threadID snowflake.ID, delta int) {
	b.mu.Lock()
	cache := b.memberCounts[guildID]
	applied := cache != nil && cache.adjust(threadID, delta)
	b.mu.Unlock()
	if applied {
		b.Log.Info("Adjusted thread member count", "guild_id", guildID, "thread_id", threadID, "delta", delta)
	}
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
	Buffer            int          `json:"buffer"`
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
	if r.Buffer == 0 {
		r.Buffer = 5
	}
}

func (b *Bot) loadReminders() {
	ctx := context.Background()
	for _, guildID := range b.GuildIDs() {
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
	hidden := b.hiddenSet(guildID)
	var threads []threadInfo
	for _, t := range result.Threads {
		if _, ok := hidden[t.ID()]; ok || t.ThreadMetadata.Archived {
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

	sort.Slice(threads, func(i, j int) bool {
		return threads[i].MessageCount > threads[j].MessageCount
	})

	total := len(threads)
	if len(threads) > 10 {
		threads = threads[:10]
	}

	// Only shown threads at the member_count cap need an exact count.
	var capped []snowflake.ID
	for _, t := range threads {
		if t.MemberCount >= memberCountCap {
			capped = append(capped, t.ID)
		}
	}
	b.refreshMemberCounts(guildID, capped)

	var cachedCount int
	b.mu.Lock()
	for i, t := range threads {
		if n, ok := b.memberCounts[guildID].exactCount(t.ID, t.MemberCount); ok {
			threads[i].MemberCount = n
			cachedCount++
		}
	}
	b.mu.Unlock()

	now := time.Now()
	const maxDescLen = 3900
	var desc strings.Builder
	for _, t := range threads {
		age := botutil.FormatAge(now.Sub(t.CreatedAt))
		line := fmt.Sprintf("- [%s](https://discord.com/channels/%d/%d) — %d msgs, %d members, %s old\n", t.Name, guildID, t.ID, t.MessageCount, t.MemberCount, age)
		if desc.Len()+len(line) > maxDescLen {
			break
		}
		desc.WriteString(line)
	}
	b.Log.Info("Built thread embed", "guild_id", guildID, "channel_id", channelID, "threads", total, "cached_counts", cachedCount)
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

// buildAllThreadsEmbed fetches all active threads across the entire guild and
// returns an embed showing the top 25 by message count. Returns nil if there
// are no active threads.
func (b *Bot) buildAllThreadsEmbed(guildID snowflake.ID) *discord.Embed {
	result, err := b.Client.Rest.GetActiveGuildThreads(guildID)
	if err != nil {
		b.Log.Error("Failed to fetch active threads", "guild_id", guildID, "error", err)
		return nil
	}

	type threadInfo struct {
		Name         string
		ID           snowflake.ID
		ParentID     snowflake.ID
		MessageCount int
		MemberCount  int
		CreatedAt    time.Time
	}
	hidden := b.hiddenSet(guildID)
	var threads []threadInfo
	for _, t := range result.Threads {
		if _, ok := hidden[t.ID()]; ok || t.ThreadMetadata.Archived {
			continue
		}
		parentID := t.ParentID()
		if parentID == nil {
			continue
		}
		createdAt := t.ThreadMetadata.CreateTimestamp
		if createdAt.IsZero() {
			createdAt = t.ID().Time()
		}
		threads = append(threads, threadInfo{
			Name:         t.Name(),
			ID:           t.ID(),
			ParentID:     *parentID,
			MessageCount: t.MessageCount,
			MemberCount:  t.MemberCount,
			CreatedAt:    createdAt,
		})
	}

	if len(threads) == 0 {
		return nil
	}

	sort.Slice(threads, func(i, j int) bool {
		return threads[i].MessageCount > threads[j].MessageCount
	})

	total := len(threads)
	const maxShown = 25
	if len(threads) > maxShown {
		threads = threads[:maxShown]
	}

	// Only shown threads at the member_count cap need an exact count.
	var capped []snowflake.ID
	for _, t := range threads {
		if t.MemberCount >= memberCountCap {
			capped = append(capped, t.ID)
		}
	}
	b.refreshMemberCounts(guildID, capped)

	var cachedCount int
	b.mu.Lock()
	for i, t := range threads {
		if n, ok := b.memberCounts[guildID].exactCount(t.ID, t.MemberCount); ok {
			threads[i].MemberCount = n
			cachedCount++
		}
	}
	b.mu.Unlock()

	now := time.Now()
	const maxDescLen = 3900
	var desc strings.Builder
	for _, t := range threads {
		age := botutil.FormatAge(now.Sub(t.CreatedAt))
		line := fmt.Sprintf("- [%s](https://discord.com/channels/%d/%d) in <#%d> — %d msgs, %d members, %s old\n", t.Name, guildID, t.ID, t.ParentID, t.MessageCount, t.MemberCount, age)
		if desc.Len()+len(line) > maxDescLen {
			break
		}
		desc.WriteString(line)
	}
	b.Log.Info("Built all-threads embed", "guild_id", guildID, "threads", total, "cached_counts", cachedCount)
	if total > maxShown {
		desc.WriteString(fmt.Sprintf("...and %d more threads\n", total-maxShown))
	}

	color := 0x5865F2 // Discord blurple
	embed := discord.Embed{
		Title:       "All Active Threads",
		Description: desc.String(),
		Color:       color,
		Footer: &discord.EmbedFooter{
			Text: fmt.Sprintf("%d active thread(s) server-wide", total),
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

	// Skip repost if the reminder is still within the last N messages (buffer).
	if r.LastMessageID != 0 && r.Buffer > 0 {
		msgs, err := b.Client.Rest.GetMessages(r.ChannelID, 0, 0, 0, r.Buffer)
		if err == nil {
			for _, m := range msgs {
				if m.ID == r.LastMessageID {
					// Still visible — update the embed in place.
					_, err := b.Client.Rest.UpdateMessage(r.ChannelID, r.LastMessageID, discord.MessageUpdate{
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
					break
				}
			}
		}
	}

	// If only bot messages were posted after our reminder, edit in place instead
	// of delete+repost. This prevents repost loops when multiple bots are active.
	if r.LastMessageID != 0 && botutil.OnlyBotsAfter(b.Client.Rest, r.ChannelID, r.LastMessageID, b.Log) {
		_, err := b.Client.Rest.UpdateMessage(r.ChannelID, r.LastMessageID, discord.MessageUpdate{
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

func (b *Bot) loadMemberCounts() {
	ctx := context.Background()
	for _, guildID := range b.GuildIDs() {
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
		// Files from before per-thread fetch times have none, so every count
		// reads as stale and is re-fetched (and old ones pruned) on next use.
		if cache.Fetched == nil {
			cache.Fetched = make(map[snowflake.ID]time.Time)
		}
		b.memberCounts[guildID] = &cache
		b.Log.Info("Loaded thread member counts", "guild_id", guildID, "count", len(cache.Counts))
	}
}

func (b *Bot) saveMemberCounts() {
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
	if len(threadIDs) == 0 {
		return
	}

	b.mu.Lock()
	cache := b.memberCounts[guildID]
	if cache == nil {
		cache = newMemberCountCache()
		b.memberCounts[guildID] = cache
	}
	sessionStart := b.sessionStart
	toFetch := cache.needsFetch(threadIDs, time.Now(), sessionStart)
	b.mu.Unlock()

	if len(toFetch) == 0 {
		b.Log.Info("Member counts fresh, skipping refresh", "guild_id", guildID, "threads", len(threadIDs))
		return
	}

	// List Thread Members is ordered by user ID, not join time, so there is no
	// watermark to fetch only new members: each refresh lists the whole thread.
	b.Log.Info("Refreshing thread member counts", "guild_id", guildID, "fetching", len(toFetch))
	var fetched int
	for _, threadID := range toFetch {
		n := b.countThreadMembers(threadID)
		if n == 0 {
			// Failed; leave it unrecorded so the next use retries it.
			continue
		}
		b.mu.Lock()
		prev, hadPrev := cache.Counts[threadID]
		prevFetched := cache.Fetched[threadID]
		cache.set(threadID, n, time.Now())
		b.mu.Unlock()
		fetched++
		if hadPrev {
			// How far the event-adjusted count was off. Persistent drift on
			// live counts means Discord skips events for some changes.
			b.Log.Info("Thread member count drift", "guild_id", guildID, "thread_id", threadID,
				"cached", prev, "fetched", n, "drift", n-prev,
				"live", countTTL(prevFetched, sessionStart) == memberCountLiveTTL)
		}
	}
	b.mu.Lock()
	cache.prune(time.Now())
	b.mu.Unlock()
	b.Log.Info("Finished refreshing thread member counts", "guild_id", guildID, "fetched", fetched, "failed", len(toFetch)-fetched)

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
	members, err := b.Client.Rest.GetThreadMembers(threadID)
	if err != nil {
		b.Log.Error("Failed to count thread members", "thread_id", threadID, "error", err)
		return 0
	}
	b.Log.Info("Fetched thread member count", "thread_id", threadID, "count", len(members))
	return len(members)
}
