package stickybot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
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

func keysOf[V any](m map[string]V) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
