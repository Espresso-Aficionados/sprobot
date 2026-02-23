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
			S3:  s3c,
			Env: "dev",
			Log: testutil.DiscardLogger(),
		},
		reminders: make(map[snowflake.ID]map[snowflake.ID]*threadReminder),
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

func TestApplyDefaults(t *testing.T) {
	r := &threadReminder{}
	r.applyDefaults()
	if r.MinIdleMins != 30 {
		t.Errorf("MinIdleMins = %d, want 30", r.MinIdleMins)
	}
	if r.MaxIdleMins != 720 {
		t.Errorf("MaxIdleMins = %d, want 720", r.MaxIdleMins)
	}
	if r.MsgThreshold != 30 {
		t.Errorf("MsgThreshold = %d, want 30", r.MsgThreshold)
	}

	// Non-zero values should not be overwritten
	r2 := &threadReminder{MinIdleMins: 5, MaxIdleMins: 10, MsgThreshold: 2}
	r2.applyDefaults()
	if r2.MinIdleMins != 5 {
		t.Errorf("MinIdleMins = %d, want 5", r2.MinIdleMins)
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
	b := &Bot{
		BaseBot: &botutil.BaseBot{Log: testutil.DiscardLogger()},
	}
	r := &threadReminder{
		ChannelID:    100,
		MinIdleMins:  30,
		MaxIdleMins:  720,
		MsgThreshold: 30,
	}

	b.startReminderGoroutine(r)
	if r.handle == nil {
		t.Fatal("handle should be set after start")
	}

	b.stopReminderGoroutine(r)
	if r.handle != nil {
		t.Error("handle should be nil after stop")
	}

	// Second stop should not panic (handle is nil, Stop is nil-safe)
	r.handle.Stop()
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
	// Should not panic
	b.loadReminders()

	guildID := snowflake.ID(1013566342345019512)
	if channels, ok := b.reminders[guildID]; ok && len(channels) > 0 {
		t.Error("should not load reminders from invalid JSON")
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
