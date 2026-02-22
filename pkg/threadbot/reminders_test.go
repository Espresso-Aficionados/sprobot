package threadbot

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/disgoorg/snowflake/v2"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

// fakeS3 is an in-memory S3-compatible server for testing.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: make(map[string][]byte)}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	key := r.URL.Path

	switch r.Method {
	case http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>Not found</Message></Error>`)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(data)

	case http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		f.objects[key] = data
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func newTestS3Client(t *testing.T, server *httptest.Server) *s3client.Client {
	t.Helper()
	endpoint := server.URL
	bucket := "test-bucket"

	cache, err := lru.New[string, map[string]string](500)
	if err != nil {
		t.Fatalf("creating cache: %v", err)
	}

	client := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: &endpoint,
		Credentials:  credentials.NewStaticCredentialsProvider("key", "secret", ""),
		UsePathStyle: true,
	})

	return s3client.NewDirect(client, bucket, endpoint, cache, discardLogger())
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestBot(t *testing.T, s3c *s3client.Client) *Bot {
	t.Helper()
	return &Bot{
		s3:        s3c,
		env:       "dev",
		log:       discardLogger(),
		reminders: make(map[snowflake.ID]map[snowflake.ID]*threadReminder),
	}
}

func TestToExportAndFromExport(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	r := &threadReminder{
		ChannelID:     100,
		GuildID:       200,
		EnabledBy:     300,
		Enabled:       true,
		LastMessageID: 400,
		LastPostTime:  now,
		MinDwellMins:  30,
		MaxDwellMins:  720,
		MsgThreshold:  30,
	}

	e := r.toExport()

	if e.ChannelID != 100 {
		t.Errorf("ChannelID = %d, want 100", e.ChannelID)
	}
	if e.GuildID != 200 {
		t.Errorf("GuildID = %d, want 200", e.GuildID)
	}
	if e.EnabledBy != 300 {
		t.Errorf("EnabledBy = %d, want 300", e.EnabledBy)
	}
	if !e.Enabled {
		t.Error("Enabled should be true")
	}
	if e.LastMessageID != 400 {
		t.Errorf("LastMessageID = %d, want 400", e.LastMessageID)
	}
	if !e.LastPostTime.Equal(now) {
		t.Errorf("LastPostTime = %v, want %v", e.LastPostTime, now)
	}
	if e.MinDwellMins != 30 {
		t.Errorf("MinDwellMins = %d, want 30", e.MinDwellMins)
	}
	if e.MaxDwellMins != 720 {
		t.Errorf("MaxDwellMins = %d, want 720", e.MaxDwellMins)
	}
	if e.MsgThreshold != 30 {
		t.Errorf("MsgThreshold = %d, want 30", e.MsgThreshold)
	}

	// Round-trip through fromExport
	r2 := fromExport(e)
	if r2.ChannelID != r.ChannelID {
		t.Error("round-trip ChannelID mismatch")
	}
	if r2.EnabledBy != r.EnabledBy {
		t.Error("round-trip EnabledBy mismatch")
	}
	if r2.Enabled != r.Enabled {
		t.Error("round-trip Enabled mismatch")
	}
	if !r2.LastPostTime.Equal(r.LastPostTime) {
		t.Error("round-trip LastPostTime mismatch")
	}
	// Channel fields should be nil after fromExport
	if r2.msgCh != nil {
		t.Error("msgCh should be nil after fromExport")
	}
	if r2.stopCh != nil {
		t.Error("stopCh should be nil after fromExport")
	}
}

func TestExportJSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	e := reminderExport{
		ChannelID:     100,
		GuildID:       200,
		EnabledBy:     300,
		Enabled:       true,
		LastMessageID: 400,
		LastPostTime:  now,
		MinDwellMins:  30,
		MaxDwellMins:  720,
		MsgThreshold:  30,
	}

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var e2 reminderExport
	if err := json.Unmarshal(data, &e2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if e2.ChannelID != e.ChannelID {
		t.Errorf("ChannelID = %d, want %d", e2.ChannelID, e.ChannelID)
	}
	if e2.Enabled != e.Enabled {
		t.Errorf("Enabled = %v, want %v", e2.Enabled, e.Enabled)
	}
	if e2.MinDwellMins != e.MinDwellMins {
		t.Errorf("MinDwellMins = %d, want %d", e2.MinDwellMins, e.MinDwellMins)
	}
	if e2.MaxDwellMins != e.MaxDwellMins {
		t.Errorf("MaxDwellMins = %d, want %d", e2.MaxDwellMins, e.MaxDwellMins)
	}
	if e2.MsgThreshold != e.MsgThreshold {
		t.Errorf("MsgThreshold = %d, want %d", e2.MsgThreshold, e.MsgThreshold)
	}
	if !e2.LastPostTime.Equal(e.LastPostTime) {
		t.Errorf("LastPostTime mismatch after JSON round-trip")
	}
}

func TestLoadRemindersNotFound(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)
	b := newTestBot(t, c)

	// Should not panic or error on missing data
	b.loadReminders()

	guildID := snowflake.ID(1013566342345019512)
	if channels, ok := b.reminders[guildID]; ok && len(channels) > 0 {
		t.Error("expected no reminders after loading from empty S3")
	}
}

func TestLoadRemindersFromS3(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)

	// Seed some reminders data — Enabled: false so no goroutine is started
	exports := map[string]reminderExport{
		"111": {
			ChannelID:    111,
			GuildID:      1013566342345019512,
			EnabledBy:    222,
			Enabled:      false,
			MinDwellMins: 30,
			MaxDwellMins: 720,
			MsgThreshold: 30,
		},
	}
	data, _ := json.Marshal(exports)
	fake.mu.Lock()
	fake.objects["/test-bucket/threadreminders/1013566342345019512.json"] = data
	fake.mu.Unlock()

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
	if r.MinDwellMins != 30 {
		t.Errorf("MinDwellMins = %d, want 30", r.MinDwellMins)
	}
}

func TestSaveRemindersForGuild(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)
	b := newTestBot(t, c)

	guildID := snowflake.ID(1013566342345019512)
	b.reminders[guildID] = map[snowflake.ID]*threadReminder{
		222: {
			ChannelID:    222,
			GuildID:      guildID,
			EnabledBy:    333,
			Enabled:      true,
			MinDwellMins: 30,
			MaxDwellMins: 720,
			MsgThreshold: 30,
		},
	}

	b.saveRemindersForGuild(guildID)

	// Verify written to S3
	fake.mu.Lock()
	data, ok := fake.objects["/test-bucket/threadreminders/1013566342345019512.json"]
	fake.mu.Unlock()

	if !ok {
		t.Fatal("reminders not saved to S3")
	}

	var exports map[string]reminderExport
	if err := json.Unmarshal(data, &exports); err != nil {
		t.Fatalf("unmarshal saved data: %v", err)
	}

	e, ok := exports["222"]
	if !ok {
		t.Fatal("channel 222 not in saved data")
	}
	if e.EnabledBy != 333 {
		t.Errorf("EnabledBy = %d, want 333", e.EnabledBy)
	}
	if e.MinDwellMins != 30 {
		t.Errorf("MinDwellMins = %d, want 30", e.MinDwellMins)
	}
}

func TestSaveRemindersForGuildNoData(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)
	b := newTestBot(t, c)

	// Saving for a guild that isn't in the map should not panic
	b.saveRemindersForGuild(999)

	fake.mu.Lock()
	_, ok := fake.objects["/test-bucket/threadreminders/999.json"]
	fake.mu.Unlock()

	if ok {
		t.Error("should not write anything for unknown guild")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)
	b := newTestBot(t, c)

	now := time.Now().Truncate(time.Second)
	guildID := snowflake.ID(1013566342345019512)
	b.reminders[guildID] = map[snowflake.ID]*threadReminder{
		333: {
			ChannelID:     333,
			GuildID:       guildID,
			EnabledBy:     444,
			Enabled:       false,
			LastMessageID: 555,
			LastPostTime:  now,
			MinDwellMins:  30,
			MaxDwellMins:  720,
			MsgThreshold:  30,
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
	if r.MinDwellMins != 30 {
		t.Errorf("MinDwellMins = %d, want 30", r.MinDwellMins)
	}
	if r.MaxDwellMins != 720 {
		t.Errorf("MaxDwellMins = %d, want 720", r.MaxDwellMins)
	}
	if r.MsgThreshold != 30 {
		t.Errorf("MsgThreshold = %d, want 30", r.MsgThreshold)
	}
}

func TestSaveAllReminders(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)
	b := newTestBot(t, c)

	guild1 := snowflake.ID(1013566342345019512)
	b.reminders[guild1] = map[snowflake.ID]*threadReminder{
		100: {ChannelID: 100, GuildID: guild1, Enabled: true, MinDwellMins: 30, MaxDwellMins: 720, MsgThreshold: 30},
	}

	b.saveAllReminders()

	fake.mu.Lock()
	_, ok := fake.objects["/test-bucket/threadreminders/1013566342345019512.json"]
	fake.mu.Unlock()

	if !ok {
		t.Error("saveAllReminders did not save guild data")
	}
}

func TestStopReminderGoroutineIdempotent(t *testing.T) {
	b := &Bot{log: discardLogger()}
	r := &threadReminder{
		ChannelID:    100,
		MinDwellMins: 30,
		MaxDwellMins: 720,
		MsgThreshold: 30,
	}

	b.startReminderGoroutine(r)
	if r.msgCh == nil || r.stopCh == nil {
		t.Fatal("channels should be set after start")
	}

	b.stopReminderGoroutine(r)
	if r.msgCh != nil || r.stopCh != nil {
		t.Error("channels should be nil after stop")
	}

	// Second stop should not panic
	b.stopReminderGoroutine(r)
}

func TestLoadRemindersInvalidJSON(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)

	// Seed invalid JSON
	fake.mu.Lock()
	fake.objects["/test-bucket/threadreminders/1013566342345019512.json"] = []byte("not valid json")
	fake.mu.Unlock()

	b := newTestBot(t, c)
	// Should not panic
	b.loadReminders()

	guildID := snowflake.ID(1013566342345019512)
	if channels, ok := b.reminders[guildID]; ok && len(channels) > 0 {
		t.Error("should not load reminders from invalid JSON")
	}
}

func TestSaveRemindersMultipleChannels(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)
	b := newTestBot(t, c)

	guildID := snowflake.ID(1013566342345019512)
	b.reminders[guildID] = map[snowflake.ID]*threadReminder{
		100: {ChannelID: 100, GuildID: guildID, EnabledBy: 111, Enabled: true, MinDwellMins: 30, MaxDwellMins: 720, MsgThreshold: 30},
		200: {ChannelID: 200, GuildID: guildID, EnabledBy: 222, Enabled: false, MinDwellMins: 60, MaxDwellMins: 1440, MsgThreshold: 50},
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
