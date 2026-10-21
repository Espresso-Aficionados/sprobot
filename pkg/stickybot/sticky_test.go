package stickybot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/testutil"
)

func newTestBot(t *testing.T, s3c *s3client.Client) *Bot {
	t.Helper()
	return &Bot{
		BaseBot:  &botutil.BaseBot{S3: s3c, Log: testutil.DiscardLogger()},
		stickies: make(map[snowflake.ID]map[snowflake.ID]*stickyMessage),
		unsaved:  make(map[snowflake.ID]bool),
	}
}

// The JSON names are the stored format: renaming one drops that field from
// every sticky already in S3. file_urls in particular is what lets a removed
// sticky's files be deleted.
func TestStoredFormat(t *testing.T) {
	const stored = `{"channel_id":"100","guild_id":"200","content":"text","file_urls":["sticky_files/200/a.png"],"created_by":"300","active":true,"last_message_id":"500","min_idle_mins":0,"max_idle_mins":30,"msg_threshold":0,"time_threshold_mins":10,"buffer":0}`
	want := stickyMessage{
		ChannelID: 100, GuildID: 200, Content: "text", FileURLs: []string{"sticky_files/200/a.png"},
		CreatedBy: 300, Active: true, LastMessageID: 500, MaxIdleMins: 30, TimeThresholdMins: 10,
	}

	var got stickyMessage
	if err := json.Unmarshal([]byte(stored), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded %+v, want %+v", got, want)
	}
	data, err := json.Marshal(&want)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != stored {
		t.Errorf("encoded %s, want %s", data, stored)
	}
}

// Zero is a valid setting for min idle, the message threshold and the buffer
// (the modal accepts it), so loading must keep it rather than swap in a
// default.
func TestLoadStickiesKeepsZeroSettings(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()

	fake.Objects["/test-bucket/stickies/1013566342345019512.json"] = []byte(`{"111": {
		"channel_id": "111", "guild_id": "1013566342345019512", "content": "x", "active": false,
		"min_idle_mins": 0, "max_idle_mins": 10, "msg_threshold": 0, "time_threshold_mins": 5, "buffer": 0}}`)

	b := newTestBot(t, testutil.NewTestS3Client(t, server))
	b.loadStickies()

	s := b.stickies[1013566342345019512][111]
	if s == nil {
		t.Fatal("sticky not loaded")
	}
	if s.MinIdleMins != 0 || s.MsgThreshold != 0 || s.Buffer != 0 {
		t.Errorf("loaded min idle %d, msg threshold %d, buffer %d; want all 0", s.MinIdleMins, s.MsgThreshold, s.Buffer)
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

// The periodic save retries failed saves and writes nothing else, so it never
// overwrites a guild's stored stickies with an unchanged, possibly stale copy.
func TestSaveUnsavedStickiesRetriesOnlyFailedSaves(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	b := newTestBot(t, testutil.NewTestS3Client(t, server))

	const failedPath = "/test-bucket/stickies/1013566342345019512.json"
	const untouchedPath = "/test-bucket/stickies/726985544038612993.json"
	b.stickies[1013566342345019512] = map[snowflake.ID]*stickyMessage{100: {ChannelID: 100, GuildID: 1013566342345019512, Content: "changed"}}
	b.stickies[726985544038612993] = map[snowflake.ID]*stickyMessage{200: {ChannelID: 200, GuildID: 726985544038612993, Content: "loaded"}}
	fake.Mu.Lock()
	fake.Objects[untouchedPath] = []byte(`{"written": "elsewhere"}`)
	fake.Fail[failedPath] = http.StatusForbidden
	fake.Mu.Unlock()

	b.saveStickiesForGuild(1013566342345019512)
	fake.Mu.Lock()
	delete(fake.Fail, failedPath)
	fake.Mu.Unlock()
	b.saveUnsavedStickies()

	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	if !strings.Contains(string(fake.Objects[failedPath]), "changed") {
		t.Errorf("failed save not retried: stored %q", fake.Objects[failedPath])
	}
	if got := string(fake.Objects[untouchedPath]); got != `{"written": "elsewhere"}` {
		t.Errorf("unchanged guild overwritten with %q", got)
	}
}

// After a load fails, nothing may be saved over the stored stickies; the next
// use retries the load and then goes ahead.
func TestLoadFailureBlocksChangesUntilRetry(t *testing.T) {
	f := &fakeDiscord{}
	b, fake := newFakeBot(t, f)
	delete(b.stickies, testGuildID) // not loaded yet
	const path = "/test-bucket/stickies/1013566342345019512.json"
	stored := `{"111": {"channel_id": "111", "guild_id": "1013566342345019512", "content": "existing", "max_idle_mins": 10, "msg_threshold": 1}}`
	fake.Mu.Lock()
	fake.Objects[path] = []byte(stored)
	fake.Fail[path] = http.StatusForbidden
	fake.Mu.Unlock()

	b.loadStickies()
	newSticky := func() *stickyMessage {
		return &stickyMessage{ChannelID: 500, GuildID: testGuildID, Active: true, MaxIdleMins: 10, MsgThreshold: 1}
	}
	if reply := b.createSticky(newSticky(), discord.Message{Content: "new"}); reply != notLoadedReply {
		t.Errorf("create while not loaded replied %q, want %q", reply, notLoadedReply)
	}
	b.saveStickiesForGuild(testGuildID)
	fake.Mu.Lock()
	got := string(fake.Objects[path])
	delete(fake.Fail, path)
	fake.Mu.Unlock()
	if got != stored {
		t.Errorf("stored stickies overwritten with %q", got)
	}
	if posts := f.postsMade(); len(posts) != 0 {
		t.Errorf("posted %d stickies while not loaded", len(posts))
	}

	if reply := b.createSticky(newSticky(), discord.Message{Content: "new"}); !strings.HasPrefix(reply, "Sticky created") {
		t.Fatalf("create after S3 recovered replied %q", reply)
	}
	fake.Mu.Lock()
	var saved map[string]*stickyMessage
	err := json.Unmarshal(fake.Objects[path], &saved)
	fake.Mu.Unlock()
	if err != nil || saved["111"] == nil || saved["500"] == nil {
		t.Errorf("stored channels %v (%v), want the existing 111 and the new 500", keysOf(saved), err)
	}
}

// A guild whose stickies failed to load at boot is loaded by the periodic
// retry, so its loops start without waiting for a moderator's command.
func TestRetryFailedLoadsUnloadedGuild(t *testing.T) {
	f := &fakeDiscord{}
	b, fake := newFakeBot(t, f)
	delete(b.stickies, testGuildID) // not loaded yet
	const path = "/test-bucket/stickies/1013566342345019512.json"
	fake.Mu.Lock()
	fake.Objects[path] = []byte(`{"111": {"channel_id": "111", "guild_id": "1013566342345019512", "content": "x", "active": true, "max_idle_mins": 10, "msg_threshold": 1}}`)
	fake.Fail[path] = http.StatusForbidden
	fake.Mu.Unlock()

	b.loadStickies()
	if _, ok := b.stickies[testGuildID]; ok {
		t.Fatal("guild loaded although its fetch failed")
	}
	fake.Mu.Lock()
	delete(fake.Fail, path)
	fake.Mu.Unlock()
	b.retryFailed()

	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.stickies[testGuildID][111]; s == nil || s.handle == nil {
		t.Errorf("after the retry, sticky = %+v, want it loaded with its loop started", s)
	}
}

// A retried load runs alongside the event handlers, so it must take b.mu.
// Under -race this fails if the loader writes b.stickies without it.
func TestLoadGuildStickiesConcurrentWithMessages(t *testing.T) {
	f := &fakeDiscord{}
	b, fake := newFakeBot(t, f)
	delete(b.stickies, testGuildID) // not loaded yet
	fake.Mu.Lock()
	fake.Objects["/test-bucket/stickies/1013566342345019512.json"] = []byte(`{"111": {"channel_id": "111", "guild_id": "1013566342345019512", "content": "x"}}`)
	fake.Mu.Unlock()
	guildID := testGuildID
	e := &events.MessageCreate{GenericMessage: &events.GenericMessage{
		ChannelID: 111, GuildID: &guildID, Message: discord.Message{Author: discord.User{ID: 9}},
	}}

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			b.onMessage(e)
		}
	})
	wg.Go(func() { b.loadGuildStickies(context.Background(), testGuildID) })
	wg.Wait()

	if b.stickies[testGuildID][111] == nil {
		t.Error("sticky not loaded")
	}
}

// A save that hangs gives up rather than holding saveMu, and with it every
// other save and every repost, indefinitely. It is retried later.
func TestSaveStickiesForGuildTimesOut(t *testing.T) {
	hang := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-hang:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(hang) }) // first, so Close doesn't wait on the handler
	orig := saveTimeout
	saveTimeout = 50 * time.Millisecond
	t.Cleanup(func() { saveTimeout = orig })
	b := newTestBot(t, testutil.NewTestS3Client(t, server))
	b.stickies[testGuildID] = map[snowflake.ID]*stickyMessage{100: {ChannelID: 100, GuildID: testGuildID}}

	done := make(chan struct{})
	go func() {
		b.saveStickiesForGuild(testGuildID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("save still waiting on S3")
	}
	if !b.unsaved[testGuildID] {
		t.Error("timed-out save not marked for retry")
	}
}

// A retried load that loses a race with another keeps the stickies already
// loaded, whose loops are running, rather than replacing them.
func TestLoadGuildStickiesKeepsLoadedGuild(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	fake.Objects["/test-bucket/stickies/1013566342345019512.json"] = []byte(`{"111": {"channel_id": "111", "guild_id": "1013566342345019512"}}`)
	b := newTestBot(t, testutil.NewTestS3Client(t, server))

	ctx := context.Background()
	if !b.loadGuildStickies(ctx, 1013566342345019512) {
		t.Fatal("first load failed")
	}
	first := b.stickies[1013566342345019512][111]
	if !b.loadGuildStickies(ctx, 1013566342345019512) {
		t.Fatal("second load failed")
	}
	if b.stickies[1013566342345019512][111] != first {
		t.Error("second load replaced the loaded stickies")
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
	b := newTestBot(t, nil)
	s := &stickyMessage{
		ChannelID:    100,
		GuildID:      1,
		Active:       true,
		MinIdleMins:  15,
		MaxIdleMins:  30,
		MsgThreshold: 4,
	}
	b.stickies[1] = map[snowflake.ID]*stickyMessage{100: s}

	b.mu.Lock()
	b.startStickyGoroutine(s)
	b.mu.Unlock()
	if s.handle == nil {
		t.Fatal("handle should be set after start")
	}

	b.stopStickyGoroutine(s)
	// A second stop, as when a command and shutdown stop the same sticky,
	// returns too.
	b.stopStickyGoroutine(s)
}

// A loop only starts for the sticky its channel currently has, and only while
// it is active: a start that loses a race with /sticky stop, remove, a
// replacement or shutdown must not leave a loop nothing can stop.
func TestStartStickyGoroutineOnlyForCurrentActiveSticky(t *testing.T) {
	tests := []struct {
		name  string
		setup func(b *Bot, s *stickyMessage)
		want  bool
	}{
		{"current and active", func(*Bot, *stickyMessage) {}, true},
		{"paused", func(_ *Bot, s *stickyMessage) { s.Active = false }, false},
		{"removed", func(b *Bot, _ *stickyMessage) { delete(b.stickies[1], 100) }, false},
		{"replaced", func(b *Bot, _ *stickyMessage) { b.stickies[1][100] = &stickyMessage{ChannelID: 100, GuildID: 1} }, false},
		{"shutting down", func(b *Bot, _ *stickyMessage) { close(b.stop) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newTestBot(t, nil)
			b.stop = make(chan struct{})
			s := &stickyMessage{ChannelID: 100, GuildID: 1, Active: true, MinIdleMins: 15, MaxIdleMins: 30, MsgThreshold: 4}
			b.stickies[1] = map[snowflake.ID]*stickyMessage{100: s}
			tt.setup(b, s)

			b.mu.Lock()
			b.startStickyGoroutine(s)
			started := s.handle != nil
			b.mu.Unlock()
			s.handle.Stop()
			if started != tt.want {
				t.Errorf("loop started = %v, want %v", started, tt.want)
			}
		})
	}
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
	b.loadStickies()

	// Not loaded, so nothing saves over the stored stickies; another allowed
	// guild with none stored still loads.
	if _, ok := b.stickies[1013566342345019512]; ok {
		t.Error("guild with unreadable stickies counted as loaded")
	}
	if _, ok := b.stickies[726985544038612993]; !ok {
		t.Error("guild with no stored stickies not loaded")
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
