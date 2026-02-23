package stickybot

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
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
		stickies: make(map[snowflake.ID]map[snowflake.ID]*stickyMessage),
	}
}

func TestJSONRoundTrip(t *testing.T) {
	s := &stickyMessage{
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

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var s2 stickyMessage
	if err := json.Unmarshal(data, &s2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if s2.ChannelID != s.ChannelID {
		t.Errorf("ChannelID = %d, want %d", s2.ChannelID, s.ChannelID)
	}
	if s2.Content != s.Content {
		t.Errorf("Content = %q, want %q", s2.Content, s.Content)
	}
	if s2.Active != s.Active {
		t.Errorf("Active = %v, want %v", s2.Active, s.Active)
	}
	if s2.MinIdleMins != s.MinIdleMins {
		t.Errorf("MinIdleMins = %d, want %d", s2.MinIdleMins, s.MinIdleMins)
	}
	if s2.MaxIdleMins != s.MaxIdleMins {
		t.Errorf("MaxIdleMins = %d, want %d", s2.MaxIdleMins, s.MaxIdleMins)
	}
	if s2.MsgThreshold != s.MsgThreshold {
		t.Errorf("MsgThreshold = %d, want %d", s2.MsgThreshold, s.MsgThreshold)
	}
	if s2.TimeThresholdMins != s.TimeThresholdMins {
		t.Errorf("TimeThresholdMins = %d, want %d", s2.TimeThresholdMins, s.TimeThresholdMins)
	}
	// handle should be nil after unmarshal
	if s2.handle != nil {
		t.Error("handle should be nil after unmarshal")
	}
}

func TestJSONOmitsEmptyEmbeds(t *testing.T) {
	s := &stickyMessage{
		ChannelID: 100,
		GuildID:   200,
		Content:   "test",
		Active:    true,
	}

	data, err := json.Marshal(s)
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

func TestApplyDefaults(t *testing.T) {
	s := &stickyMessage{}
	s.applyDefaults()
	if s.MinIdleMins != 15 {
		t.Errorf("MinIdleMins = %d, want 15", s.MinIdleMins)
	}
	if s.MaxIdleMins != 30 {
		t.Errorf("MaxIdleMins = %d, want 30", s.MaxIdleMins)
	}
	if s.MsgThreshold != 30 {
		t.Errorf("MsgThreshold = %d, want 30", s.MsgThreshold)
	}

	// Non-zero values should not be overwritten
	s2 := &stickyMessage{MinIdleMins: 5, MaxIdleMins: 10, MsgThreshold: 2}
	s2.applyDefaults()
	if s2.MinIdleMins != 5 {
		t.Errorf("MinIdleMins = %d, want 5", s2.MinIdleMins)
	}
}

func TestLoadStickiesNotFound(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
	b := newTestBot(t, c)

	// Should not panic or error on missing data
	b.loadStickies()

	guildID := snowflake.ID(1013566342345019512)
	if channels, ok := b.stickies[guildID]; ok && len(channels) > 0 {
		t.Error("expected no stickies after loading from empty S3")
	}
}

func TestLoadStickiesFromS3(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)

	// Seed some stickies data — Active: false so no goroutine is started
	stickies := map[string]*stickyMessage{
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
	data, _ := json.Marshal(stickies)
	fake.Mu.Lock()
	fake.Objects["/test-bucket/stickies/1013566342345019512.json"] = data
	fake.Mu.Unlock()

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
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
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
	fake.Mu.Lock()
	data, ok := fake.Objects["/test-bucket/stickies/1013566342345019512.json"]
	fake.Mu.Unlock()

	if !ok {
		t.Fatal("stickies not saved to S3")
	}

	var saved map[string]*stickyMessage
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal saved data: %v", err)
	}

	s, ok := saved["222"]
	if !ok {
		t.Fatal("channel 222 not in saved data")
	}
	if s.Content != "saved content" {
		t.Errorf("Content = %q, want %q", s.Content, "saved content")
	}
	if s.MinIdleMins != 20 {
		t.Errorf("MinIdleMins = %d, want 20", s.MinIdleMins)
	}
}

func TestSaveStickiesForGuildNoData(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
	b := newTestBot(t, c)

	// Saving for a guild that isn't in the map should not panic
	b.saveStickiesForGuild(999)

	fake.Mu.Lock()
	_, ok := fake.Objects["/test-bucket/stickies/999.json"]
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
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
	b := newTestBot(t, c)

	guild1 := snowflake.ID(1013566342345019512)
	b.stickies[guild1] = map[snowflake.ID]*stickyMessage{
		100: {ChannelID: 100, GuildID: guild1, Content: "g1", Active: true, MinIdleMins: 5, MaxIdleMins: 10, MsgThreshold: 2},
	}

	b.saveAllStickies()

	fake.Mu.Lock()
	_, ok := fake.Objects["/test-bucket/stickies/1013566342345019512.json"]
	fake.Mu.Unlock()

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
	b := &Bot{
		BaseBot: &botutil.BaseBot{Log: testutil.DiscardLogger()},
	}
	s := &stickyMessage{
		ChannelID:    100,
		MinIdleMins:  15,
		MaxIdleMins:  30,
		MsgThreshold: 4,
	}

	b.startStickyGoroutine(s)
	if s.handle == nil {
		t.Fatal("handle should be set after start")
	}

	b.stopStickyGoroutine(s)
	if s.handle != nil {
		t.Error("handle should be nil after stop")
	}

	// Second stop should not panic
	b.stopStickyGoroutine(s)
}

func TestLoadStickiesInvalidJSON(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)

	// Seed invalid JSON
	fake.Mu.Lock()
	fake.Objects["/test-bucket/stickies/1013566342345019512.json"] = []byte("not valid json")
	fake.Mu.Unlock()

	b := newTestBot(t, c)
	// Should not panic
	b.loadStickies()

	guildID := snowflake.ID(1013566342345019512)
	if channels, ok := b.stickies[guildID]; ok && len(channels) > 0 {
		t.Error("should not load stickies from invalid JSON")
	}
}

func TestTruncatePreview(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncated", "hello world", 5, "hello..."},
		{"empty", "", 5, ""},
		{"cut inside discord token", "see <#123456>", 7, "see ..."},
		{"complete token preserved", "see <#123456> ok", 14, "see <#123456> ..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncatePreview(tt.input, tt.max)
			if got != tt.expected {
				t.Errorf("truncatePreview(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.expected)
			}
		})
	}
}

func TestSaveStickiesMultipleChannels(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	c := testutil.NewTestS3Client(t, server)
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
