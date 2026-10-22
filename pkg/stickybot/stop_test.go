package stickybot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

// fakeRepostAPI serves the message endpoints repostSticky uses. Create Message
// waits for release, so a test can hold a repost in flight.
type fakeRepostAPI struct {
	created chan struct{}
	release chan struct{}
}

func (f *fakeRepostAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/messages") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		_, _ = w.Write([]byte(`[]`))
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		f.created <- struct{}{}
		<-f.release
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "777", "channel_id": "500", "content": "sticky",
			"author":    map[string]any{"id": "1", "username": "stickybot", "bot": true},
			"timestamp": time.Now().Format(time.RFC3339),
		})
	}
}

// startRepostInFlight starts a sticky whose first repost is stuck in Create
// Message until the returned release func is called.
func startRepostInFlight(t *testing.T) (*Bot, *stickyMessage, func()) {
	t.Helper()
	api := &fakeRepostAPI{created: make(chan struct{}, 1), release: make(chan struct{})}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	s3srv := httptest.NewServer(testutil.NewFakeS3())
	t.Cleanup(s3srv.Close)
	// Cleanups run last-in first-out, so a test that fails with the repost
	// still parked releases it before srv.Close waits for the handler.
	var once sync.Once
	release := func() { once.Do(func() { close(api.release) }) }
	t.Cleanup(release)

	b := newTestBot(t, testutil.NewTestS3Client(t, s3srv))
	b.Client = &bot.Client{Rest: rest.New(rest.NewClient("test", rest.WithURL(srv.URL)))}
	guildID := snowflake.ID(1013566342345019512)
	s := &stickyMessage{
		ChannelID: 500, GuildID: guildID, Content: "sticky", Active: true,
		LastMessageID: 100, MinIdleMins: 0, MaxIdleMins: 10, MsgThreshold: 1,
	}
	b.stickies[guildID] = map[snowflake.ID]*stickyMessage{500: s}
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
	return b, s, release
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
			b, s, release := startRepostInFlight(t)

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
