package threadbot

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
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

// loadReminders loads the stored reminders of every allowed guild that hasn't
// loaded yet and starts their loops. Run calls it before the gateway opens and
// again from the save loop, so a failed load is retried; so does the next
// command that needs the guild's reminders.
func (b *Bot) loadReminders() {
	for _, guildID := range botutil.AllowedGuildIDs {
		if err := b.ensureRemindersLoaded(guildID); err != nil {
			b.Log.Error("Failed to load thread reminders; will retry", "guild_id", guildID, "error", err)
		}
	}
}

// ensureRemindersLoaded loads a guild's stored reminders, unless they have
// loaded already, and starts their loops. A guild gets its b.reminders entry
// only from a load that succeeded or found nothing stored, and never loses
// it, so stored reminders that failed to load are never saved over.
func (b *Bot) ensureRemindersLoaded(guildID snowflake.ID) error {
	b.mu.Lock()
	_, loaded := b.reminders[guildID]
	b.mu.Unlock()
	if loaded {
		return nil
	}

	channels := make(map[snowflake.ID]*threadReminder)
	data, err := b.S3.FetchThreadReminders(context.Background(), fmt.Sprintf("%d", guildID))
	switch {
	case errors.Is(err, s3client.ErrNotFound):
	case err != nil:
		return err
	default:
		var stored map[string]*threadReminder
		if err := json.Unmarshal(data, &stored); err != nil {
			return fmt.Errorf("decoding thread reminders: %w", err)
		}
		for _, r := range stored {
			channels[r.ChannelID] = r
		}
	}

	b.mu.Lock()
	if _, ok := b.reminders[guildID]; ok {
		b.mu.Unlock()
		return nil // a concurrent load got there first
	}
	b.reminders[guildID] = channels
	for _, r := range channels {
		b.startReminderGoroutine(r)
	}
	b.mu.Unlock()
	b.Log.Info("Loaded thread reminders", "guild_id", guildID, "count", len(channels))
	return nil
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

// startReminderGoroutine starts r's repost loop, unless r is no longer its
// channel's enabled reminder or the bot is shutting down. The caller holds
// b.mu, so the loop starts in the same critical section as the change that
// calls for it. Any earlier loop of r is stopped, and the new one runs only
// once it has exited, so r never has two reposts in flight.
func (b *Bot) startReminderGoroutine(r *threadReminder) {
	select {
	case <-b.stop:
		return
	default:
	}
	if !r.Enabled || b.reminders[r.GuildID][r.ChannelID] != r {
		return
	}
	prev := r.handle
	r.handle = idleloop.NewHandle()
	r.handle.StartAfter(prev, idleloop.Config{
		MinIdleMins:       r.MinIdleMins,
		MaxIdleMins:       r.MaxIdleMins,
		MsgThreshold:      r.MsgThreshold,
		TimeThresholdMins: r.TimeThresholdMins,
		LastPostTime:      r.LastPostTime,
	}, func() bool { return b.repostReminder(r) })
}

// stopReminderGoroutine stops r's repost loop and waits out any repost in
// progress, so r.LastMessageID is final when it returns. The caller must not
// hold b.mu, which the repost takes, and must already have taken r out of
// b.reminders, so no new loop can start for it.
func (b *Bot) stopReminderGoroutine(r *threadReminder) {
	b.mu.Lock()
	h := r.handle
	b.mu.Unlock()
	h.Stop()
}

func (b *Bot) stopAllReminderGoroutines() {
	b.mu.Lock()
	var handles []*idleloop.Handle
	for _, channels := range b.reminders {
		for _, r := range channels {
			// Left in place: a command stopping the same reminder meanwhile
			// must wait for this goroutine too.
			handles = append(handles, r.handle)
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

// threadInfo is a thread as a thread list shows it.
type threadInfo struct {
	Name         string
	ID           snowflake.ID
	ParentID     snowflake.ID
	MessageCount int
	MemberCount  int
	CreatedAt    time.Time
}

// listedThreads returns the threads a list shows, busiest first: open threads
// that aren't hidden, under channelID, or anywhere in the server when
// channelID is 0. Private threads are never listed: the lists are shown to
// people who aren't in them.
func listedThreads(all []discord.GuildThread, channelID snowflake.ID, hidden map[snowflake.ID]struct{}) []threadInfo {
	var threads []threadInfo
	for _, t := range all {
		if _, ok := hidden[t.ID()]; ok || t.ThreadMetadata.Archived || t.Type() == discord.ChannelTypeGuildPrivateThread {
			continue
		}
		parentID := t.ParentID()
		if parentID == nil || (channelID != 0 && *parentID != channelID) {
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
	slices.SortStableFunc(threads, func(a, b threadInfo) int {
		return cmp.Compare(b.MessageCount, a.MessageCount)
	})
	return threads
}

// buildThreadsEmbed fetches the guild's active threads and returns an embed
// of the busiest: up to 10 under channelID, or with channelID 0, up to 25
// across the server. The embed is nil if there are none to show.
func (b *Bot) buildThreadsEmbed(guildID, channelID snowflake.ID) (*discord.Embed, error) {
	result, err := b.Client.Rest.GetActiveGuildThreads(guildID)
	if err != nil {
		return nil, fmt.Errorf("fetching active threads: %w", err)
	}
	threads := listedThreads(result.Threads, channelID, b.hiddenSet(guildID))
	if len(threads) == 0 {
		return nil, nil
	}
	total := len(threads)
	maxShown := 10
	if channelID == 0 {
		maxShown = 25
	}
	threads = threads[:min(total, maxShown)]

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

	b.Log.Info("Built thread embed", "guild_id", guildID, "channel_id", channelID, "threads", total, "cached_counts", cachedCount)
	embed := threadsEmbed(guildID, channelID, threads, total, time.Now())
	return &embed, nil
}

// linkTextEscaper backslash-escapes Discord markdown in a thread name used as
// masked link text. Anyone can name a thread, and a name like
// "x](https://phish.example) [y" would otherwise put a link of its choosing
// in the bot's message. With every "(" escaped, the only "](" in a line is
// the one ending the name, so the link always goes to the thread. The rest
// keeps names from rendering as formatting, mentions or timestamps. Discord
// drops a backslash before punctuation, so the name reads as typed.
var linkTextEscaper = strings.NewReplacer(
	`\`, `\\`, `[`, `\[`, `]`, `\]`, `(`, `\(`, `)`, `\)`,
	`*`, `\*`, `_`, `\_`, "`", "\\`", `~`, `\~`, `|`, `\|`, `<`, `\<`, `>`, `\>`,
)

// threadsEmbed formats a thread list: threads are the ones to show out of
// total listed, and channelID 0 marks a server-wide list.
func threadsEmbed(guildID, channelID snowflake.ID, threads []threadInfo, total int, now time.Time) discord.Embed {
	const maxDescLen = 3900
	var desc strings.Builder
	var shown int
	for _, t := range threads {
		var parent string
		if channelID == 0 {
			parent = fmt.Sprintf(" in <#%d>", t.ParentID)
		}
		line := fmt.Sprintf("- [%s](https://discord.com/channels/%d/%d)%s — %d msgs, %d members, %s old\n",
			linkTextEscaper.Replace(t.Name), guildID, t.ID, parent, t.MessageCount, t.MemberCount, botutil.FormatAge(now.Sub(t.CreatedAt)))
		if desc.Len()+len(line) > maxDescLen {
			break
		}
		desc.WriteString(line)
		shown++
	}
	if more := total - shown; more > 0 {
		fmt.Fprintf(&desc, "...and %d more threads\n", more)
	}

	title, footer := "Active Threads", fmt.Sprintf("%d active thread(s)", total)
	if channelID == 0 {
		title, footer = "All Active Threads", fmt.Sprintf("%d active thread(s) server-wide", total)
	}
	return discord.Embed{
		Title:       title,
		Description: desc.String(),
		Color:       0x5865F2, // Discord blurple
		Footer:      &discord.EmbedFooter{Text: footer},
		Timestamp:   &now,
	}
}

func (b *Bot) repostReminder(r *threadReminder) bool {
	embed, err := b.buildThreadsEmbed(r.GuildID, r.ChannelID)
	if err != nil {
		b.Log.Error("Failed to build thread reminder", "guild_id", r.GuildID, "channel_id", r.ChannelID, "error", err)
		return false
	}
	if embed == nil {
		return false
	}

	// Update the reminder in place while it is still among the channel's last
	// Buffer messages, or when only bots have posted since: reposting then
	// would start a repost loop between bots.
	if r.LastMessageID != 0 && (b.reminderInBuffer(r) || botutil.OnlyBotsAfter(b.Client.Rest, r.ChannelID, r.LastMessageID, b.Log)) {
		if b.editReminder(r, *embed) {
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

	b.mu.Lock()
	r.LastMessageID = sent.ID
	r.LastPostTime = time.Now()
	b.mu.Unlock()
	b.saveRemindersForGuild(r.GuildID)
	return true
}

// reminderInBuffer reports whether r's message is among the channel's last
// r.Buffer messages.
func (b *Bot) reminderInBuffer(r *threadReminder) bool {
	if r.Buffer <= 0 {
		return false
	}
	msgs, err := b.Client.Rest.GetMessages(r.ChannelID, 0, 0, 0, r.Buffer)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(msgs, func(m discord.Message) bool { return m.ID == r.LastMessageID })
}

// editReminder replaces the embed on r's message and records the post time.
// It reports whether the edit succeeded.
func (b *Bot) editReminder(r *threadReminder, embed discord.Embed) bool {
	if _, err := b.Client.Rest.UpdateMessage(r.ChannelID, r.LastMessageID, discord.MessageUpdate{
		Embeds: &[]discord.Embed{embed},
	}); err != nil {
		return false
	}
	b.mu.Lock()
	r.LastPostTime = time.Now()
	b.mu.Unlock()
	b.saveRemindersForGuild(r.GuildID)
	return true
}

// loadMemberCounts loads the stored member counts of every allowed guild. A
// failed load is retried the next time the guild's counts are needed.
func (b *Bot) loadMemberCounts() {
	for _, guildID := range botutil.AllowedGuildIDs {
		if err := b.ensureMemberCountsLoaded(guildID); err != nil {
			b.Log.Error("Failed to load thread member counts; will retry on next use", "guild_id", guildID, "error", err)
		}
	}
}

// ensureMemberCountsLoaded loads a guild's stored member counts unless they
// have loaded already. As with reminders, a guild gets its b.memberCounts
// entry only from a load that succeeded or found nothing stored (or from
// /threadbot refreshcounts), so counts that failed to load are never saved
// over.
func (b *Bot) ensureMemberCountsLoaded(guildID snowflake.ID) error {
	b.mu.Lock()
	_, loaded := b.memberCounts[guildID]
	b.mu.Unlock()
	if loaded {
		return nil
	}

	cache := newMemberCountCache()
	data, err := b.S3.FetchThreadMemberCounts(context.Background(), fmt.Sprintf("%d", guildID))
	switch {
	case errors.Is(err, s3client.ErrNotFound):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, cache); err != nil {
			return fmt.Errorf("decoding thread member counts: %w", err)
		}
		if cache.Counts == nil {
			cache.Counts = make(map[snowflake.ID]int)
		}
		// Files from before per-thread fetch times have none, so every count
		// reads as stale and is re-fetched (and old ones pruned) on next use.
		if cache.Fetched == nil {
			cache.Fetched = make(map[snowflake.ID]time.Time)
		}
	}

	b.mu.Lock()
	if _, ok := b.memberCounts[guildID]; !ok {
		b.memberCounts[guildID] = cache
	}
	b.mu.Unlock()
	b.Log.Info("Loaded thread member counts", "guild_id", guildID, "count", len(cache.Counts))
	return nil
}

func (b *Bot) saveMemberCounts() {
	b.mu.Lock()
	guildIDs := make([]snowflake.ID, 0, len(b.memberCounts))
	for id := range b.memberCounts {
		guildIDs = append(guildIDs, id)
	}
	b.mu.Unlock()

	for _, guildID := range guildIDs {
		b.saveMemberCountsForGuild(guildID)
	}
}

// saveMemberCountsForGuild persists a guild's member counts. A guild whose
// counts haven't loaded has nothing to save.
func (b *Bot) saveMemberCountsForGuild(guildID snowflake.ID) {
	b.mu.Lock()
	cache, ok := b.memberCounts[guildID]
	if !ok {
		b.mu.Unlock()
		return
	}
	data, err := json.Marshal(cache)
	b.mu.Unlock()
	if err != nil {
		b.Log.Error("Failed to marshal thread member counts", "guild_id", guildID, "error", err)
		return
	}

	if err := b.S3.SaveThreadMemberCounts(context.Background(), fmt.Sprintf("%d", guildID), data); err != nil {
		b.Log.Error("Failed to save thread member counts", "guild_id", guildID, "error", err)
	} else {
		b.Log.Info("Saved thread member counts", "guild_id", guildID)
	}
}

func (b *Bot) refreshMemberCounts(guildID snowflake.ID, threadIDs []snowflake.ID) {
	if len(threadIDs) == 0 {
		return
	}
	if err := b.ensureMemberCountsLoaded(guildID); err != nil {
		// Counts fetched now couldn't be saved without overwriting the stored
		// ones, so member_count is shown until the load succeeds.
		b.Log.Error("Failed to load thread member counts; showing capped counts", "guild_id", guildID, "error", err)
		return
	}

	b.mu.Lock()
	cache := b.memberCounts[guildID]
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
	b.saveMemberCountsForGuild(guildID)
}

// threadMembersPageSize is the most members List Thread Members returns at a
// time.
const threadMembersPageSize = 100

// countThreadMembers counts a thread's members with List Thread Members, page
// by page: from API v11 the endpoint always paginates, so a single call would
// stop at 100. Members are counted, never stored. Returns 0 on failure.
func (b *Bot) countThreadMembers(threadID snowflake.ID) int {
	var n int
	var after snowflake.ID
	for {
		// A new page per request: disgo's ThreadMemberPage.Next continues
		// after the first member of the previous page instead of the last.
		page := b.Client.Rest.GetThreadMembersPage(threadID, after, threadMembersPageSize)
		if !page.Next() {
			if errors.Is(page.Err, rest.ErrNoMorePages) {
				break
			}
			b.Log.Error("Failed to count thread members", "thread_id", threadID, "error", page.Err)
			return 0
		}
		n += len(page.Items)
		if len(page.Items) < threadMembersPageSize {
			break
		}
		// Members come ordered by user ID.
		after = page.Items[len(page.Items)-1].UserID
	}
	b.Log.Info("Fetched thread member count", "thread_id", threadID, "count", n)
	return n
}
