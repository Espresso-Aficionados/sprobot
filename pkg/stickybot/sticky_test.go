package stickybot

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/disgoorg/disgo/discord"
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
		s3:       s3c,
		env:      "dev",
		log:      discardLogger(),
		stickies: make(map[snowflake.ID]map[snowflake.ID]*stickyMessage),
	}
}

func TestToExportAndFromExport(t *testing.T) {
	s := &stickyMessage{
		ChannelID:         100,
		GuildID:           200,
		Content:           "hello",
		Embeds:            []discord.Embed{{Title: "test"}},
		FileURLs:          []string{"https://example.com/file.png"},
		CreatedBy:         300,
		Active:            true,
		LastMessageID:     400,
		MinIdleMins:       15,
		MaxIdleMins:       30,
		MsgThreshold:      4,
		TimeThresholdMins: 10,
	}

	e := s.toExport()

	if e.ChannelID != 100 {
		t.Errorf("ChannelID = %d, want 100", e.ChannelID)
	}
	if e.GuildID != 200 {
		t.Errorf("GuildID = %d, want 200", e.GuildID)
	}
	if e.Content != "hello" {
		t.Errorf("Content = %q, want %q", e.Content, "hello")
	}
	if len(e.Embeds) != 1 || e.Embeds[0].Title != "test" {
		t.Errorf("Embeds not preserved")
	}
	if len(e.FileURLs) != 1 || e.FileURLs[0] != "https://example.com/file.png" {
		t.Errorf("FileURLs not preserved")
	}
	if e.CreatedBy != 300 {
		t.Errorf("CreatedBy = %d, want 300", e.CreatedBy)
	}
	if !e.Active {
		t.Error("Active should be true")
	}
	if e.LastMessageID != 400 {
		t.Errorf("LastMessageID = %d, want 400", e.LastMessageID)
	}
	if e.MinIdleMins != 15 {
		t.Errorf("MinIdleMins = %d, want 15", e.MinIdleMins)
	}
	if e.MaxIdleMins != 30 {
		t.Errorf("MaxIdleMins = %d, want 30", e.MaxIdleMins)
	}
	if e.MsgThreshold != 4 {
		t.Errorf("MsgThreshold = %d, want 4", e.MsgThreshold)
	}
	if e.TimeThresholdMins != 10 {
		t.Errorf("TimeThresholdMins = %d, want 10", e.TimeThresholdMins)
	}

	// Round-trip through fromExport
	s2 := fromExport(e)
	if s2.ChannelID != s.ChannelID {
		t.Error("round-trip ChannelID mismatch")
	}
	if s2.Content != s.Content {
		t.Error("round-trip Content mismatch")
	}
	if s2.Active != s.Active {
		t.Error("round-trip Active mismatch")
	}
	// Channel fields should be nil after fromExport
	if s2.msgCh != nil {
		t.Error("msgCh should be nil after fromExport")
	}
	if s2.stopCh != nil {
		t.Error("stopCh should be nil after fromExport")
	}
}

func TestExportJSONRoundTrip(t *testing.T) {
	e := stickyExport{
		ChannelID:         100,
		GuildID:           200,
		Content:           "sticky text",
		Active:            true,
		MinIdleMins:       15,
		MaxIdleMins:       30,
		MsgThreshold:      5,
		TimeThresholdMins: 10,
		LastMessageID:     500,
	}

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var e2 stickyExport
	if err := json.Unmarshal(data, &e2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if e2.ChannelID != e.ChannelID {
		t.Errorf("ChannelID = %d, want %d", e2.ChannelID, e.ChannelID)
	}
	if e2.Content != e.Content {
		t.Errorf("Content = %q, want %q", e2.Content, e.Content)
	}
	if e2.Active != e.Active {
		t.Errorf("Active = %v, want %v", e2.Active, e.Active)
	}
	if e2.MinIdleMins != e.MinIdleMins {
		t.Errorf("MinIdleMins = %d, want %d", e2.MinIdleMins, e.MinIdleMins)
	}
	if e2.MaxIdleMins != e.MaxIdleMins {
		t.Errorf("MaxIdleMins = %d, want %d", e2.MaxIdleMins, e.MaxIdleMins)
	}
	if e2.MsgThreshold != e.MsgThreshold {
		t.Errorf("MsgThreshold = %d, want %d", e2.MsgThreshold, e.MsgThreshold)
	}
	if e2.TimeThresholdMins != e.TimeThresholdMins {
		t.Errorf("TimeThresholdMins = %d, want %d", e2.TimeThresholdMins, e.TimeThresholdMins)
	}
}

func TestExportOmitsEmptyEmbeds(t *testing.T) {
	e := stickyExport{
		ChannelID: 100,
		GuildID:   200,
		Content:   "test",
		Active:    true,
	}

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if strings.Contains(string(data), "embeds") {
		t.Error("expected embeds to be omitted when empty")
	}
	if strings.Contains(string(data), "file_urls") {
		t.Error("expected file_urls to be omitted when empty")
	}
}

func TestLoadStickiesNotFound(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)
	b := newTestBot(t, c)

	// Should not panic or error on missing data
	b.loadStickies()

	guildID := snowflake.ID(1013566342345019512)
	if channels, ok := b.stickies[guildID]; ok && len(channels) > 0 {
		t.Error("expected no stickies after loading from empty S3")
	}
}

func TestLoadStickiesFromS3(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)

	// Seed some stickies data — Active: false so no goroutine is started
	exports := map[string]stickyExport{
		"111": {
			ChannelID:    111,
			GuildID:      1013566342345019512,
			Content:      "sticky content",
			Active:       false,
			MinIdleMins:  10,
			MaxIdleMins:  20,
			MsgThreshold: 3,
		},
	}
	data, _ := json.Marshal(exports)
	fake.mu.Lock()
	fake.objects["/test-bucket/stickies/1013566342345019512.json"] = data
	fake.mu.Unlock()

	b := newTestBot(t, c)
	b.loadStickies()

	guildID := snowflake.ID(1013566342345019512)
	channels, ok := b.stickies[guildID]
	if !ok {
		t.Fatal("guild stickies not loaded")
	}
	s, ok := channels[111]
	if !ok {
		t.Fatal("channel sticky not loaded")
	}
	if s.Content != "sticky content" {
		t.Errorf("Content = %q, want %q", s.Content, "sticky content")
	}
	if s.Active {
		t.Error("expected Active to be false")
	}
	if s.MinIdleMins != 10 {
		t.Errorf("MinIdleMins = %d, want 10", s.MinIdleMins)
	}
}

func TestSaveStickiesForGuild(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)
	b := newTestBot(t, c)

	guildID := snowflake.ID(1013566342345019512)
	b.stickies[guildID] = map[snowflake.ID]*stickyMessage{
		222: {
			ChannelID:    222,
			GuildID:      guildID,
			Content:      "saved content",
			Active:       true,
			MinIdleMins:  20,
			MaxIdleMins:  40,
			MsgThreshold: 5,
		},
	}

	b.saveStickiesForGuild(guildID)

	// Verify written to S3
	fake.mu.Lock()
	data, ok := fake.objects["/test-bucket/stickies/1013566342345019512.json"]
	fake.mu.Unlock()

	if !ok {
		t.Fatal("stickies not saved to S3")
	}

	var exports map[string]stickyExport
	if err := json.Unmarshal(data, &exports); err != nil {
		t.Fatalf("unmarshal saved data: %v", err)
	}

	e, ok := exports["222"]
	if !ok {
		t.Fatal("channel 222 not in saved data")
	}
	if e.Content != "saved content" {
		t.Errorf("Content = %q, want %q", e.Content, "saved content")
	}
	if e.MinIdleMins != 20 {
		t.Errorf("MinIdleMins = %d, want 20", e.MinIdleMins)
	}
}

func TestSaveStickiesForGuildNoData(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)
	b := newTestBot(t, c)

	// Saving for a guild that isn't in the map should not panic
	b.saveStickiesForGuild(999)

	fake.mu.Lock()
	_, ok := fake.objects["/test-bucket/stickies/999.json"]
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

	guildID := snowflake.ID(1013566342345019512)
	b.stickies[guildID] = map[snowflake.ID]*stickyMessage{
		333: {
			ChannelID:         333,
			GuildID:           guildID,
			Content:           "round trip",
			Embeds:            []discord.Embed{{Title: "embed1"}},
			FileURLs:          []string{"https://example.com/a.png"},
			CreatedBy:         444,
			Active:            false,
			LastMessageID:     555,
			MinIdleMins:       15,
			MaxIdleMins:       30,
			MsgThreshold:      10,
			TimeThresholdMins: 10,
		},
	}

	b.saveStickiesForGuild(guildID)

	// Load into a fresh bot
	b2 := newTestBot(t, c)
	b2.loadStickies()

	channels, ok := b2.stickies[guildID]
	if !ok {
		t.Fatal("guild not loaded")
	}
	s, ok := channels[333]
	if !ok {
		t.Fatal("channel 333 not loaded")
	}
	if s.Content != "round trip" {
		t.Errorf("Content = %q, want %q", s.Content, "round trip")
	}
	if len(s.Embeds) != 1 || s.Embeds[0].Title != "embed1" {
		t.Error("Embeds not preserved in round trip")
	}
	if len(s.FileURLs) != 1 || s.FileURLs[0] != "https://example.com/a.png" {
		t.Error("FileURLs not preserved in round trip")
	}
	if s.CreatedBy != 444 {
		t.Errorf("CreatedBy = %d, want 444", s.CreatedBy)
	}
	if s.Active {
		t.Error("Active should be false")
	}
	if s.LastMessageID != 555 {
		t.Errorf("LastMessageID = %d, want 555", s.LastMessageID)
	}
	if s.MinIdleMins != 15 {
		t.Errorf("MinIdleMins = %d, want 15", s.MinIdleMins)
	}
	if s.MaxIdleMins != 30 {
		t.Errorf("MaxIdleMins = %d, want 30", s.MaxIdleMins)
	}
	if s.MsgThreshold != 10 {
		t.Errorf("MsgThreshold = %d, want 10", s.MsgThreshold)
	}
	if s.TimeThresholdMins != 10 {
		t.Errorf("TimeThresholdMins = %d, want 10", s.TimeThresholdMins)
	}
}

func TestSaveAllStickies(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)
	b := newTestBot(t, c)

	guild1 := snowflake.ID(1013566342345019512)
	b.stickies[guild1] = map[snowflake.ID]*stickyMessage{
		100: {ChannelID: 100, GuildID: guild1, Content: "g1", Active: true, MinIdleMins: 5, MaxIdleMins: 10, MsgThreshold: 2},
	}

	b.saveAllStickies()

	fake.mu.Lock()
	_, ok := fake.objects["/test-bucket/stickies/1013566342345019512.json"]
	fake.mu.Unlock()

	if !ok {
		t.Error("saveAllStickies did not save guild data")
	}
}

func TestGetSticky(t *testing.T) {
	b := &Bot{
		stickies: map[snowflake.ID]map[snowflake.ID]*stickyMessage{
			100: {
				200: {ChannelID: 200, Content: "found"},
			},
		},
	}

	s := b.getSticky(100, 200)
	if s == nil {
		t.Fatal("expected to find sticky")
	}
	if s.Content != "found" {
		t.Errorf("Content = %q, want %q", s.Content, "found")
	}

	// Wrong guild
	if b.getSticky(999, 200) != nil {
		t.Error("expected nil for unknown guild")
	}

	// Wrong channel
	if b.getSticky(100, 999) != nil {
		t.Error("expected nil for unknown channel")
	}
}

func TestStopStickyGoroutineIdempotent(t *testing.T) {
	b := &Bot{log: discardLogger()}
	s := &stickyMessage{
		ChannelID:    100,
		MinIdleMins:  15,
		MaxIdleMins:  30,
		MsgThreshold: 4,
	}

	b.startStickyGoroutine(s)
	if s.msgCh == nil || s.stopCh == nil {
		t.Fatal("channels should be set after start")
	}

	b.stopStickyGoroutine(s)
	if s.msgCh != nil || s.stopCh != nil {
		t.Error("channels should be nil after stop")
	}

	// Second stop should not panic
	b.stopStickyGoroutine(s)
}

func TestLoadStickiesInvalidJSON(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)

	// Seed invalid JSON
	fake.mu.Lock()
	fake.objects["/test-bucket/stickies/1013566342345019512.json"] = []byte("not valid json")
	fake.mu.Unlock()

	b := newTestBot(t, c)
	// Should not panic
	b.loadStickies()

	guildID := snowflake.ID(1013566342345019512)
	if channels, ok := b.stickies[guildID]; ok && len(channels) > 0 {
		t.Error("should not load stickies from invalid JSON")
	}
}

func TestSaveStickiesMultipleChannels(t *testing.T) {
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := newTestS3Client(t, server)
	b := newTestBot(t, c)

	guildID := snowflake.ID(1013566342345019512)
	b.stickies[guildID] = map[snowflake.ID]*stickyMessage{
		100: {ChannelID: 100, GuildID: guildID, Content: "first", Active: true, MinIdleMins: 5, MaxIdleMins: 10, MsgThreshold: 2},
		200: {ChannelID: 200, GuildID: guildID, Content: "second", Active: false, MinIdleMins: 10, MaxIdleMins: 20, MsgThreshold: 3},
	}

	b.saveStickiesForGuild(guildID)

	// Load and verify both
	b2 := newTestBot(t, c)
	b2.loadStickies()

	channels := b2.stickies[guildID]
	if len(channels) != 2 {
		t.Fatalf("expected 2 channels, got %d", len(channels))
	}

	s1 := channels[100]
	if s1 == nil || s1.Content != "first" || !s1.Active {
		t.Errorf("channel 100 mismatch: %+v", s1)
	}

	s2 := channels[200]
	if s2 == nil || s2.Content != "second" || s2.Active {
		t.Errorf("channel 200 mismatch: %+v", s2)
	}
}
