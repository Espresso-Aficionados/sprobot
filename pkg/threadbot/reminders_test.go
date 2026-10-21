package threadbot

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/testutil"
)

func newTestBot(t *testing.T, s3c *s3client.Client) *Bot {
	t.Helper()
	return &Bot{
		BaseBot: &botutil.BaseBot{
			S3:                   s3c,
			Log:                  testutil.DiscardLogger(),
			TestGuildIDsOverride: []snowflake.ID{1013566342345019512},
		},
		reminders:    make(map[snowflake.ID]map[snowflake.ID]*threadReminder),
		memberCounts: make(map[snowflake.ID]*memberCountCache),
		hidden:       make(map[snowflake.ID]map[snowflake.ID]struct{}),
	}
}

func TestJSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	r := &threadReminder{
		ChannelID:         100,
		GuildID:           200,
		EnabledBy:         300,
		Enabled:           true,
		LastMessageID:     400,
		LastPostTime:      now,
		MinIdleMins:       30,
		MaxIdleMins:       720,
		MsgThreshold:      30,
		TimeThresholdMins: 60,
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var r2 threadReminder
	if err := json.Unmarshal(data, &r2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if r2.ChannelID != r.ChannelID {
		t.Errorf("ChannelID = %d, want %d", r2.ChannelID, r.ChannelID)
	}
	if r2.Enabled != r.Enabled {
		t.Errorf("Enabled = %v, want %v", r2.Enabled, r.Enabled)
	}
	if r2.MinIdleMins != r.MinIdleMins {
		t.Errorf("MinIdleMins = %d, want %d", r2.MinIdleMins, r.MinIdleMins)
	}
	if r2.MaxIdleMins != r.MaxIdleMins {
		t.Errorf("MaxIdleMins = %d, want %d", r2.MaxIdleMins, r.MaxIdleMins)
	}
	if r2.MsgThreshold != r.MsgThreshold {
		t.Errorf("MsgThreshold = %d, want %d", r2.MsgThreshold, r.MsgThreshold)
	}
	if r2.TimeThresholdMins != r.TimeThresholdMins {
		t.Errorf("TimeThresholdMins = %d, want %d", r2.TimeThresholdMins, r.TimeThresholdMins)
	}
	if !r2.LastPostTime.Equal(r.LastPostTime) {
		t.Errorf("LastPostTime mismatch after JSON round-trip")
	}
	// handle should be nil after unmarshal
	if r2.handle != nil {
		t.Error("handle should be nil after unmarshal")
	}
}

// Zero is a valid setting for min_idle, msg_threshold (time-only reminders)
// and buffer, and must survive a restart as zero.
func TestZeroSettingsSurviveReload(t *testing.T) {
	server := httptest.NewServer(testutil.NewFakeS3())
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)

	guildID := snowflake.ID(1013566342345019512)
	b := newTestBot(t, c)
	b.reminders[guildID] = map[snowflake.ID]*threadReminder{
		100: {ChannelID: 100, GuildID: guildID, MinIdleMins: 0, MaxIdleMins: 10, MsgThreshold: 0, TimeThresholdMins: 60, Buffer: 0},
	}
	b.saveRemindersForGuild(guildID)

	b2 := newTestBot(t, c)
	b2.loadReminders()
	r := b2.reminders[guildID][100]
	if r == nil {
		t.Fatal("reminder not reloaded")
	}
	if r.MinIdleMins != 0 || r.MaxIdleMins != 10 || r.MsgThreshold != 0 || r.TimeThresholdMins != 60 || r.Buffer != 0 {
		t.Errorf("reloaded settings = %+v, want them as saved", r)
	}
}

func TestLoadRemindersNotFound(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
	b := newTestBot(t, c)

	// Should not panic or error on missing data
	b.loadReminders()

	guildID := snowflake.ID(1013566342345019512)
	if channels, ok := b.reminders[guildID]; ok && len(channels) > 0 {
		t.Error("expected no reminders after loading from empty S3")
	}
}

func TestLoadRemindersFromS3(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)

	// Seed some reminders data — Enabled: false so no goroutine is started
	reminders := map[string]*threadReminder{
		"111": {
			ChannelID:    111,
			GuildID:      1013566342345019512,
			EnabledBy:    222,
			Enabled:      false,
			MinIdleMins:  30,
			MaxIdleMins:  720,
			MsgThreshold: 30,
		},
	}
	data, _ := json.Marshal(reminders)
	fake.Mu.Lock()
	fake.Objects["/test-bucket/threadreminders/1013566342345019512.json"] = data
	fake.Mu.Unlock()

	b := newTestBot(t, c)
	b.loadReminders()

	guildID := snowflake.ID(1013566342345019512)
	channels, ok := b.reminders[guildID]
	if !ok {
		t.Fatal("guild reminders not loaded")
	}
	r, ok := channels[111]
	if !ok {
		t.Fatal("channel reminder not loaded")
	}
	if r.EnabledBy != 222 {
		t.Errorf("EnabledBy = %d, want 222", r.EnabledBy)
	}
	if r.Enabled {
		t.Error("expected Enabled to be false")
	}
	if r.MinIdleMins != 30 {
		t.Errorf("MinIdleMins = %d, want 30", r.MinIdleMins)
	}
}

func TestSaveRemindersForGuild(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
	b := newTestBot(t, c)

	guildID := snowflake.ID(1013566342345019512)
	b.reminders[guildID] = map[snowflake.ID]*threadReminder{
		222: {
			ChannelID:    222,
			GuildID:      guildID,
			EnabledBy:    333,
			Enabled:      true,
			MinIdleMins:  30,
			MaxIdleMins:  720,
			MsgThreshold: 30,
		},
	}

	b.saveRemindersForGuild(guildID)

	// Verify written to S3
	fake.Mu.Lock()
	data, ok := fake.Objects["/test-bucket/threadreminders/1013566342345019512.json"]
	fake.Mu.Unlock()

	if !ok {
		t.Fatal("reminders not saved to S3")
	}

	var saved map[string]*threadReminder
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal saved data: %v", err)
	}

	r, ok := saved["222"]
	if !ok {
		t.Fatal("channel 222 not in saved data")
	}
	if r.EnabledBy != 333 {
		t.Errorf("EnabledBy = %d, want 333", r.EnabledBy)
	}
	if r.MinIdleMins != 30 {
		t.Errorf("MinIdleMins = %d, want 30", r.MinIdleMins)
	}
}

func TestSaveRemindersForGuildNoData(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
	b := newTestBot(t, c)

	// Saving for a guild that isn't in the map should not panic
	b.saveRemindersForGuild(999)

	fake.Mu.Lock()
	_, ok := fake.Objects["/test-bucket/threadreminders/999.json"]
	fake.Mu.Unlock()

	if ok {
		t.Error("should not write anything for unknown guild")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
	b := newTestBot(t, c)

	now := time.Now().Truncate(time.Second)
	guildID := snowflake.ID(1013566342345019512)
	b.reminders[guildID] = map[snowflake.ID]*threadReminder{
		333: {
			ChannelID:         333,
			GuildID:           guildID,
			EnabledBy:         444,
			Enabled:           false,
			LastMessageID:     555,
			LastPostTime:      now,
			MinIdleMins:       30,
			MaxIdleMins:       720,
			MsgThreshold:      30,
			TimeThresholdMins: 60,
		},
	}

	b.saveRemindersForGuild(guildID)

	// Load into a fresh bot
	b2 := newTestBot(t, c)
	b2.loadReminders()

	channels, ok := b2.reminders[guildID]
	if !ok {
		t.Fatal("guild not loaded")
	}
	r, ok := channels[333]
	if !ok {
		t.Fatal("channel 333 not loaded")
	}
	if r.EnabledBy != 444 {
		t.Errorf("EnabledBy = %d, want 444", r.EnabledBy)
	}
	if r.Enabled {
		t.Error("Enabled should be false")
	}
	if r.LastMessageID != 555 {
		t.Errorf("LastMessageID = %d, want 555", r.LastMessageID)
	}
	if !r.LastPostTime.Equal(now) {
		t.Errorf("LastPostTime mismatch")
	}
	if r.MinIdleMins != 30 {
		t.Errorf("MinIdleMins = %d, want 30", r.MinIdleMins)
	}
	if r.MaxIdleMins != 720 {
		t.Errorf("MaxIdleMins = %d, want 720", r.MaxIdleMins)
	}
	if r.MsgThreshold != 30 {
		t.Errorf("MsgThreshold = %d, want 30", r.MsgThreshold)
	}
	if r.TimeThresholdMins != 60 {
		t.Errorf("TimeThresholdMins = %d, want 60", r.TimeThresholdMins)
	}
}

func TestSaveAllReminders(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
	b := newTestBot(t, c)

	guild1 := snowflake.ID(1013566342345019512)
	b.reminders[guild1] = map[snowflake.ID]*threadReminder{
		100: {ChannelID: 100, GuildID: guild1, Enabled: true, MinIdleMins: 30, MaxIdleMins: 720, MsgThreshold: 30},
	}

	b.saveAllReminders()

	fake.Mu.Lock()
	_, ok := fake.Objects["/test-bucket/threadreminders/1013566342345019512.json"]
	fake.Mu.Unlock()

	if !ok {
		t.Error("saveAllReminders did not save guild data")
	}
}

func TestStopReminderGoroutineIdempotent(t *testing.T) {
	b := newTestBot(t, nil)
	r := &threadReminder{
		ChannelID:    100,
		GuildID:      1,
		Enabled:      true,
		MinIdleMins:  30,
		MaxIdleMins:  720,
		MsgThreshold: 30,
	}
	b.reminders[1] = map[snowflake.ID]*threadReminder{100: r}

	b.mu.Lock()
	b.startReminderGoroutine(r)
	b.mu.Unlock()
	if r.handle == nil {
		t.Fatal("handle should be set after start")
	}

	b.stopReminderGoroutine(r)
	// A second stop, as when a command and shutdown stop the same reminder,
	// returns too.
	b.stopReminderGoroutine(r)
}

// A loop only starts for the reminder its channel currently has, and only
// while it is enabled: a start that loses a race with /threadbot disable, a
// replacement or shutdown must not leave a loop nothing can stop.
func TestStartReminderGoroutineOnlyForCurrentReminder(t *testing.T) {
	tests := []struct {
		name  string
		setup func(b *Bot, r *threadReminder)
		want  bool
	}{
		{"current and enabled", func(*Bot, *threadReminder) {}, true},
		{"disabled", func(_ *Bot, r *threadReminder) { r.Enabled = false }, false},
		{"removed", func(b *Bot, _ *threadReminder) { delete(b.reminders[1], 100) }, false},
		{"replaced", func(b *Bot, _ *threadReminder) { b.reminders[1][100] = &threadReminder{ChannelID: 100, GuildID: 1} }, false},
		{"shutting down", func(b *Bot, _ *threadReminder) { close(b.stop) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTestBot(t, nil)
			b.stop = make(chan struct{})
			r := &threadReminder{ChannelID: 100, GuildID: 1, Enabled: true, MinIdleMins: 30, MaxIdleMins: 720, MsgThreshold: 30}
			b.reminders[1] = map[snowflake.ID]*threadReminder{100: r}
			tt.setup(b, r)

			b.mu.Lock()
			b.startReminderGoroutine(r)
			started := r.handle != nil
			b.mu.Unlock()
			r.handle.Stop()
			if started != tt.want {
				t.Errorf("loop started = %v, want %v", started, tt.want)
			}
		})
	}
}

func TestLoadRemindersInvalidJSON(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)

	// Seed invalid JSON
	fake.Mu.Lock()
	fake.Objects["/test-bucket/threadreminders/1013566342345019512.json"] = []byte("not valid json")
	fake.Mu.Unlock()

	b := newTestBot(t, c)
	b.loadReminders()

	// Left unloaded, so the stored file isn't saved over.
	guildID := snowflake.ID(1013566342345019512)
	if _, ok := b.reminders[guildID]; ok {
		t.Error("guild with undecodable reminders marked loaded")
	}
}

func TestMemberCountCacheJSONRoundTrip(t *testing.T) {
	cache := &memberCountCache{
		Counts: map[snowflake.ID]int{
			snowflake.ID(111): 5,
			snowflake.ID(222): 75,
		},
		Fetched: map[snowflake.ID]time.Time{snowflake.ID(111): time.Now().Truncate(time.Second)},
	}

	data, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var cache2 memberCountCache
	if err := json.Unmarshal(data, &cache2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(cache2.Counts) != 2 {
		t.Fatalf("expected 2 counts, got %d", len(cache2.Counts))
	}
	if cache2.Counts[snowflake.ID(111)] != 5 {
		t.Errorf("count for 111 = %d, want 5", cache2.Counts[snowflake.ID(111)])
	}
	if cache2.Counts[snowflake.ID(222)] != 75 {
		t.Errorf("count for 222 = %d, want 75", cache2.Counts[snowflake.ID(222)])
	}
	if !cache2.Fetched[111].Equal(cache.Fetched[111]) {
		t.Error("Fetched mismatch after round-trip")
	}
}

func TestMemberCountNeedsFetch(t *testing.T) {
	now := time.Now()
	const fresh, stale, missing = snowflake.ID(1), snowflake.ID(2), snowflake.ID(3)
	c := newMemberCountCache()
	c.set(fresh, 60, now.Add(-time.Hour))
	c.set(stale, 70, now.Add(-memberCountTTL))

	// No session recorded yet: every count uses the short TTL.
	got := c.needsFetch([]snowflake.ID{fresh, stale, missing}, now, time.Time{})
	if len(got) != 2 || got[0] != stale || got[1] != missing {
		t.Errorf("needsFetch = %v, want [%d %d]", got, stale, missing)
	}
}

func TestMemberCountNeedsFetchSessionAware(t *testing.T) {
	now := time.Now()
	sessionStart := now.Add(-10 * 24 * time.Hour)
	const live, liveExpired, preSession = snowflake.ID(1), snowflake.ID(2), snowflake.ID(3)
	c := newMemberCountCache()
	// Fetched during the unbroken session: events kept these current.
	c.set(live, 60, now.Add(-3*24*time.Hour))
	c.set(liveExpired, 70, now.Add(-memberCountLiveTTL))
	// Fetched before a reconnect: events may have been missed.
	c.set(preSession, 80, sessionStart.Add(-time.Minute))

	got := c.needsFetch([]snowflake.ID{live, liveExpired, preSession}, now, sessionStart)
	if len(got) != 2 || got[0] != liveExpired || got[1] != preSession {
		t.Errorf("needsFetch = %v, want [%d %d]", got, liveExpired, preSession)
	}

	// A new session started after the fetch: back to the short TTL.
	if got := c.needsFetch([]snowflake.ID{live}, now, now.Add(-time.Hour)); len(got) != 1 {
		t.Errorf("count from before the new session not re-fetched: got %v", got)
	}
}

func TestMemberCountPrune(t *testing.T) {
	now := time.Now()
	const kept, old, legacy = snowflake.ID(1), snowflake.ID(2), snowflake.ID(3)
	c := newMemberCountCache()
	c.set(kept, 60, now.Add(-memberCountTTL))
	c.set(old, 70, now.Add(-memberCountRetain))
	c.Counts[legacy] = 80 // from a file written before per-thread fetch times

	c.prune(now)
	if _, ok := c.Counts[kept]; !ok {
		t.Error("pruned a count still within retention")
	}
	for _, id := range []snowflake.ID{old, legacy} {
		if _, ok := c.Counts[id]; ok {
			t.Errorf("count for %d not pruned", id)
		}
		if _, ok := c.Fetched[id]; ok {
			t.Errorf("fetch time for %d not pruned", id)
		}
	}
}

func TestMemberCountExactCount(t *testing.T) {
	const id = snowflake.ID(1)
	c := newMemberCountCache()
	c.set(id, 120, time.Now())

	if n, ok := c.exactCount(id, memberCountCap); !ok || n != 120 {
		t.Errorf("at cap: got %d, %v; want 120, true", n, ok)
	}
	// Below the cap, member_count is used even if a stale exact count lingers.
	if _, ok := c.exactCount(id, memberCountCap-1); ok {
		t.Error("below cap: want member_count, got cached count")
	}
	var nilCache *memberCountCache
	if _, ok := nilCache.exactCount(id, memberCountCap); ok {
		t.Error("nil cache returned a count")
	}
}

func TestLoadMemberCountsNotFound(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
	b := newTestBot(t, c)

	b.loadMemberCounts()

	// Nothing stored: the guild starts with an empty cache it may save.
	guildID := snowflake.ID(1013566342345019512)
	if cache := b.memberCounts[guildID]; cache == nil || len(cache.Counts) != 0 {
		t.Errorf("member counts after loading from empty S3 = %+v, want an empty cache", cache)
	}
}

func TestLoadMemberCountsFromS3(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)

	cache := &memberCountCache{
		Counts: map[snowflake.ID]int{
			snowflake.ID(111): 42,
			snowflake.ID(222): 100,
		},
		Fetched: map[snowflake.ID]time.Time{snowflake.ID(111): time.Now().Add(-2 * time.Hour)},
	}
	data, _ := json.Marshal(cache)
	fake.Mu.Lock()
	fake.Objects["/test-bucket/threadmembercounts/1013566342345019512.json"] = data
	fake.Mu.Unlock()

	b := newTestBot(t, c)
	b.loadMemberCounts()

	guildID := snowflake.ID(1013566342345019512)
	loaded, ok := b.memberCounts[guildID]
	if !ok || loaded == nil {
		t.Fatal("member counts not loaded")
	}
	if loaded.Counts[snowflake.ID(111)] != 42 {
		t.Errorf("count for 111 = %d, want 42", loaded.Counts[snowflake.ID(111)])
	}
	if loaded.Counts[snowflake.ID(222)] != 100 {
		t.Errorf("count for 222 = %d, want 100", loaded.Counts[snowflake.ID(222)])
	}
}

func TestSaveMemberCounts(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
	b := newTestBot(t, c)

	guildID := snowflake.ID(1013566342345019512)
	b.memberCounts[guildID] = &memberCountCache{
		Counts: map[snowflake.ID]int{
			snowflake.ID(333): 55,
		},
		Fetched: map[snowflake.ID]time.Time{snowflake.ID(111): time.Now().Truncate(time.Second)},
	}

	b.saveMemberCounts()

	fake.Mu.Lock()
	data, ok := fake.Objects["/test-bucket/threadmembercounts/1013566342345019512.json"]
	fake.Mu.Unlock()

	if !ok {
		t.Fatal("member counts not saved to S3")
	}

	var saved memberCountCache
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if saved.Counts[snowflake.ID(333)] != 55 {
		t.Errorf("count for 333 = %d, want 55", saved.Counts[snowflake.ID(333)])
	}
}

func TestLoadMemberCountsInvalidJSON(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)

	fake.Mu.Lock()
	fake.Objects["/test-bucket/threadmembercounts/1013566342345019512.json"] = []byte("not json")
	fake.Mu.Unlock()

	b := newTestBot(t, c)
	b.loadMemberCounts()

	// Left unloaded, so the stored file isn't saved over.
	guildID := snowflake.ID(1013566342345019512)
	if _, ok := b.memberCounts[guildID]; ok {
		t.Error("guild with undecodable member counts marked loaded")
	}
}

func TestSaveRemindersMultipleChannels(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
	b := newTestBot(t, c)

	guildID := snowflake.ID(1013566342345019512)
	b.reminders[guildID] = map[snowflake.ID]*threadReminder{
		100: {ChannelID: 100, GuildID: guildID, EnabledBy: 111, Enabled: true, MinIdleMins: 30, MaxIdleMins: 720, MsgThreshold: 30},
		200: {ChannelID: 200, GuildID: guildID, EnabledBy: 222, Enabled: false, MinIdleMins: 60, MaxIdleMins: 1440, MsgThreshold: 50},
	}

	b.saveRemindersForGuild(guildID)

	// Load and verify both
	b2 := newTestBot(t, c)
	b2.loadReminders()

	channels := b2.reminders[guildID]
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}

	r1 := channels[100]
	if r1 == nil || r1.EnabledBy != 111 || !r1.Enabled {
		t.Errorf("channel 100 mismatch: %+v", r1)
	}

	r2 := channels[200]
	if r2 == nil || r2.EnabledBy != 222 || r2.Enabled {
		t.Errorf("channel 200 mismatch: %+v", r2)
	}
}

func TestMemberCountAdjust(t *testing.T) {
	const cached, failed, uncached = snowflake.ID(1), snowflake.ID(2), snowflake.ID(3)
	c := &memberCountCache{Counts: map[snowflake.ID]int{cached: 120, failed: 0}}

	if !c.adjust(cached, 1) || c.Counts[cached] != 121 {
		t.Errorf("join: got %d, want 121", c.Counts[cached])
	}
	if !c.adjust(cached, -1) || c.Counts[cached] != 120 {
		t.Errorf("leave: got %d, want 120", c.Counts[cached])
	}
	// 0 marks a failed fetch; adjusting it would replace the member_count
	// fallback with a tiny wrong number.
	if c.adjust(failed, 1) || c.Counts[failed] != 0 {
		t.Errorf("failed fetch adjusted to %d, want untouched 0", c.Counts[failed])
	}
	if c.adjust(uncached, 1) {
		t.Error("uncached thread adjusted, want it left for the next refresh")
	}
	if _, ok := c.Counts[uncached]; ok {
		t.Error("adjust added an uncached thread")
	}
}

func TestMemberCountAdjustFloorsAtOne(t *testing.T) {
	const id = snowflake.ID(1)
	c := &memberCountCache{Counts: map[snowflake.ID]int{id: 1}}
	// Drift (a missed join, then a leave) must not reach 0, which would read
	// as a failed fetch until the next refresh.
	c.adjust(id, -1)
	c.adjust(id, -1)
	if c.Counts[id] != 1 {
		t.Errorf("got %d, want 1", c.Counts[id])
	}
}
