package stickybot

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

// A deleted channel's sticky stops reposting, loses its files and leaves the
// stored stickies, so it isn't started again on the next boot.
func TestChannelDeleteDropsSticky(t *testing.T) {
	deletes := map[string]func(*Bot){
		"channel": func(b *Bot) {
			b.onChannelDelete(&events.GuildChannelDelete{GenericGuildChannel: &events.GenericGuildChannel{GuildID: testGuildID, ChannelID: 500}})
		},
		"thread": func(b *Bot) {
			b.onThreadDelete(&events.ThreadDelete{GenericThread: &events.GenericThread{GuildID: testGuildID, ThreadID: 500}})
		},
	}
	for name, deleteChannel := range deletes {
		t.Run(name, func(t *testing.T) {
			f := &fakeDiscord{}
			b, fakeS3 := newFakeBot(t, f)
			key := testFileKey("a")
			stored := seedFiles(fakeS3, key)
			// Min idle 0 and a threshold of 1: one message makes a live loop repost.
			s := &stickyMessage{ChannelID: 500, GuildID: testGuildID, Active: true, FileURLs: []string{key}, MinIdleMins: 0, MaxIdleMins: 10, MsgThreshold: 1}
			other := &stickyMessage{ChannelID: 600, GuildID: testGuildID, Content: "other channel"}
			b.stickies[testGuildID] = map[snowflake.ID]*stickyMessage{500: s, 600: other}
			b.mu.Lock()
			b.startStickyGoroutine(s)
			h := s.handle
			b.mu.Unlock()

			deleteChannel(b)

			h.Signal()
			time.Sleep(100 * time.Millisecond)
			if posts := f.postsMade(); len(posts) != 0 {
				t.Errorf("deleted channel's sticky reposted %d times", len(posts))
			}
			if b.stickies[testGuildID][500] != nil {
				t.Error("deleted channel's sticky still in memory")
			}
			if left := stored(); len(left) != 0 {
				t.Errorf("files left %q, want none", left)
			}
			fakeS3.Mu.Lock()
			data := fakeS3.Objects["/test-bucket/stickies/1013566342345019512.json"]
			fakeS3.Mu.Unlock()
			var saved map[string]*stickyMessage
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatalf("stored stickies: %v", err)
			}
			if _, ok := saved["500"]; ok || saved["600"] == nil {
				t.Errorf("stored channels %v, want only 600", keysOf(saved))
			}
		})
	}
}

// A channel deleted without an event the bot saw, such as while it was
// offline or as the parent of a sticky's thread, is noticed by the next
// repost, which drops the sticky the same way.
func TestRepostToUnknownChannelDropsSticky(t *testing.T) {
	f := &fakeDiscord{gone: true}
	b, fakeS3 := newFakeBot(t, f)
	key := testFileKey("a")
	stored := seedFiles(fakeS3, key)
	s := &stickyMessage{ChannelID: 500, GuildID: testGuildID, Active: true, FileURLs: []string{key}, MinIdleMins: 0, MaxIdleMins: 10, MsgThreshold: 1}
	other := &stickyMessage{ChannelID: 600, GuildID: testGuildID, Content: "other channel"}
	b.stickies[testGuildID] = map[snowflake.ID]*stickyMessage{500: s, 600: other}
	b.mu.Lock()
	b.startStickyGoroutine(s)
	h := s.handle
	b.mu.Unlock()

	h.Signal() // the repost fails with Unknown Channel
	waitFor(t, "the sticky to be dropped", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.stickies[testGuildID][500] == nil
	})
	// The files go only once the sticky's loop has stopped, so this also
	// shows that dropping it from its own repost doesn't deadlock.
	waitFor(t, "the files to be deleted", func() bool { return len(stored()) == 0 })
	if saved := storedStickies(t, fakeS3); saved["500"] != nil || saved["600"] == nil {
		t.Errorf("stored channels %v, want only 600", keysOf(saved))
	}
}

// seedStoredStickies stores an active sticky, with one saved file, for each
// of the test guild's channels, and returns their file keys by channel.
func seedStoredStickies(t *testing.T, fake *testutil.FakeS3, channelIDs ...snowflake.ID) map[snowflake.ID]string {
	t.Helper()
	keys := make(map[snowflake.ID]string)
	stored := make(map[string]*stickyMessage)
	for i, channelID := range channelIDs {
		key := testFileKey(string(rune('a' + i)))
		keys[channelID] = key
		stored[channelID.String()] = &stickyMessage{ChannelID: channelID, GuildID: testGuildID, Active: true, FileURLs: []string{key}, MinIdleMins: 0, MaxIdleMins: 10, MsgThreshold: 1}
	}
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	fake.Mu.Lock()
	fake.Objects["/test-bucket/stickies/1013566342345019512.json"] = data
	for _, key := range keys {
		fake.Objects["/test-bucket/"+key] = []byte("file")
	}
	fake.Mu.Unlock()
	return keys
}

func guildReady(guildID snowflake.ID) *events.GuildReady {
	return &events.GuildReady{GenericGuild: &events.GenericGuild{GuildID: guildID}}
}

// Discord never replays a channel delete, so once the gateway says the bot is
// in a guild, a sticky whose channel was deleted meanwhile is dropped: its
// loop stops, its files go and the drop is saved. One whose channel the bot
// can't see, or that Get Channel fails on, is kept.
func TestGuildReadyDropsStickiesOfDeletedChannels(t *testing.T) {
	f := &fakeDiscord{channelErrs: map[snowflake.ID]int{
		500: int(rest.JSONErrorCodeUnknownChannel),
		700: int(rest.JSONErrorCodeMissingAccess),
		900: 0, // 500 Internal Server Error
	}}
	b, fakeS3 := newFakeBot(t, f)
	delete(b.stickies, testGuildID)
	keys := seedStoredStickies(t, fakeS3, 500, 600, 700, 900)
	b.loadStickies()
	b.mu.Lock()
	h := b.stickies[testGuildID][500].handle
	b.mu.Unlock()

	b.onGuildReady(guildReady(testGuildID))

	h.Signal()
	time.Sleep(100 * time.Millisecond)
	if posts := f.postsMade(); len(posts) != 0 {
		t.Errorf("deleted channel's sticky reposted %d times", len(posts))
	}
	b.mu.Lock()
	inMemory := slices.Sorted(maps.Keys(b.stickies[testGuildID]))
	b.mu.Unlock()
	if !slices.Equal(inMemory, []snowflake.ID{600, 700, 900}) {
		t.Errorf("stickies in memory %v, want 600, 700 and 900", inMemory)
	}
	fakeS3.Mu.Lock()
	for channelID, key := range keys {
		if _, kept := fakeS3.Objects["/test-bucket/"+key]; kept != (channelID != 500) {
			t.Errorf("channel %d's file kept = %v", channelID, kept)
		}
	}
	fakeS3.Mu.Unlock()
	if saved := slices.Sorted(maps.Keys(storedStickies(t, fakeS3))); !slices.Equal(saved, []string{"600", "700", "900"}) {
		t.Errorf("stored channels %v, want 600, 700 and 900", saved)
	}
}

// A guild whose stickies load only after its GuildReady, because the startup
// load failed, drops the stickies of deleted channels on that load.
func TestLateLoadDropsStickiesOfDeletedChannels(t *testing.T) {
	f := &fakeDiscord{channelErrs: map[snowflake.ID]int{500: int(rest.JSONErrorCodeUnknownChannel)}}
	b, fakeS3 := newFakeBot(t, f)
	delete(b.stickies, testGuildID)
	keys := seedStoredStickies(t, fakeS3, 500, 600)
	const storedKey = "/test-bucket/stickies/1013566342345019512.json"
	fakeS3.Mu.Lock()
	fakeS3.Fail[storedKey] = http.StatusForbidden
	fakeS3.Mu.Unlock()

	b.loadStickies()
	b.onGuildReady(guildReady(testGuildID))
	fakeS3.Mu.Lock()
	delete(fakeS3.Fail, storedKey)
	fakeS3.Mu.Unlock()
	b.loadStickies()

	waitFor(t, "the sticky to be dropped", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.stickies[testGuildID][500] == nil
	})
	waitFor(t, "its file to be deleted", func() bool {
		fakeS3.Mu.Lock()
		defer fakeS3.Mu.Unlock()
		_, ok := fakeS3.Objects["/test-bucket/"+keys[500]]
		return !ok
	})
	if saved := storedStickies(t, fakeS3); saved["500"] != nil || saved["600"] == nil {
		t.Errorf("stored channels %v, want only 600", keysOf(saved))
	}
	b.mu.Lock()
	kept := b.stickies[testGuildID][600] != nil
	b.mu.Unlock()
	if !kept {
		t.Error("existing channel's sticky dropped")
	}
}

func keysOf[V any](m map[string]V) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
