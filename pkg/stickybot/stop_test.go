package stickybot

import (
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

// startRepostInFlight starts a sticky whose first repost is stuck in Create
// Message until the returned release func is called.
func startRepostInFlight(t *testing.T) (*Bot, *stickyMessage, *fakeDiscord, *testutil.FakeS3, func()) {
	t.Helper()
	api := &fakeDiscord{created: make(chan struct{}, 1), hold: make(chan struct{})}
	b, fakeS3 := newFakeBot(t, api)
	// Cleanups run last-in first-out, so a test that fails with the repost
	// still parked releases it before the loops are stopped and the server
	// closed, both of which wait for it.
	var once sync.Once
	release := func() { once.Do(func() { close(api.hold) }) }
	t.Cleanup(release)

	s := &stickyMessage{
		ChannelID: 500, GuildID: testGuildID, Content: "sticky", Active: true,
		LastMessageID: 100, MinIdleMins: 0, MaxIdleMins: 10, MsgThreshold: 1,
	}
	b.stickies[testGuildID] = map[snowflake.ID]*stickyMessage{500: s}
	b.mu.Lock()
	b.startStickyGoroutine(s)
	h := s.handle
	b.mu.Unlock()
	h.Signal()
	select {
	case <-api.created:
	case <-time.After(5 * time.Second):
		t.Fatal("repost did not reach Create Message")
	}
	return b, s, api, fakeS3, release
}

// The stop functions must wait for an in-flight repost without holding b.mu,
// which the repost takes to record the new message ID. Callers then read
// LastMessageID to delete the sticky, so it must be the new message's.
func TestStopWaitsForInFlightRepost(t *testing.T) {
	stops := map[string]func(*Bot, *stickyMessage){
		"stopStickyGoroutine":     func(b *Bot, s *stickyMessage) { b.stopStickyGoroutine(s) },
		"stopAllStickyGoroutines": func(b *Bot, _ *stickyMessage) { b.stopAllStickyGoroutines() },
		// A command stopping a sticky during shutdown must wait for the
		// same repost as shutdown does, not find the loop already gone.
		"stopSticky after stopAll": func(b *Bot, s *stickyMessage) {
			go b.stopAllStickyGoroutines()
			time.Sleep(20 * time.Millisecond)
			b.stopStickyGoroutine(s)
		},
	}
	for name, stop := range stops {
		t.Run(name, func(t *testing.T) {
			b, s, _, _, release := startRepostInFlight(t)

			done := make(chan struct{})
			go func() {
				stop(b, s)
				close(done)
			}()
			select {
			case <-done:
				t.Fatal("stop returned while a repost was in flight")
			case <-time.After(100 * time.Millisecond):
			}

			release()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("stop did not return after the repost finished (deadlock on b.mu?)")
			}
			if s.LastMessageID != 777 {
				t.Errorf("LastMessageID after stop = %d, want the reposted message 777", s.LastMessageID)
			}
		})
	}
}
