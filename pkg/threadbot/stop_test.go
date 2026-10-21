package threadbot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

// fakeRepostAPI adds the message endpoints repostReminder uses to
// fakeThreadAPI. Create Message waits for release, so a test can hold a
// repost in flight.
type fakeRepostAPI struct {
	*fakeThreadAPI
	created chan struct{}
	release chan struct{}
}

func (f *fakeRepostAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/messages") {
		f.fakeThreadAPI.ServeHTTP(w, r)
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
			"id": "777", "channel_id": "500",
			"author":    map[string]any{"id": "1", "username": "threadbot", "bot": true},
			"timestamp": time.Now().Format(time.RFC3339),
		})
	}
}

// startRepostInFlight starts a reminder whose first repost is stuck in Create
// Message until the returned release func is called.
func startRepostInFlight(t *testing.T) (*Bot, *threadReminder, func()) {
	t.Helper()
	api := &fakeRepostAPI{
		fakeThreadAPI: &fakeThreadAPI{members: map[string]int{}, fetches: map[string]int{}},
		created:       make(chan struct{}, 1),
		release:       make(chan struct{}),
	}
	api.addThread("101", 5, 5)
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	s3srv := httptest.NewServer(testutil.NewFakeS3())
	t.Cleanup(s3srv.Close)

	b := newTestBot(t, testutil.NewTestS3Client(t, s3srv))
	b.Client = &bot.Client{Rest: rest.New(rest.NewClient("test", rest.WithURL(srv.URL)))}
	guildID := snowflake.ID(1013566342345019512)
	r := &threadReminder{
		ChannelID: 500, GuildID: guildID, Enabled: true,
		LastMessageID: 100, MinIdleMins: 0, MaxIdleMins: 10, MsgThreshold: 1,
	}
	b.reminders[guildID] = map[snowflake.ID]*threadReminder{500: r}
	b.startReminderGoroutine(r)

	b.mu.Lock()
	h := r.handle
	b.mu.Unlock()
	h.Signal()
	select {
	case <-api.created:
	case <-time.After(5 * time.Second):
		t.Fatal("repost did not reach Create Message")
	}
	return b, r, func() { close(api.release) }
}

// The stop functions must wait for an in-flight repost without holding b.mu,
// which the repost takes to record the new message ID. /threadreminder
// disable then reads LastMessageID to delete the reminder, so it must be the
// new message's.
func TestStopWaitsForInFlightRepost(t *testing.T) {
	stops := map[string]func(*Bot, *threadReminder){
		"stopReminderGoroutine":     func(b *Bot, r *threadReminder) { b.stopReminderGoroutine(r) },
		"stopAllReminderGoroutines": func(b *Bot, _ *threadReminder) { b.stopAllReminderGoroutines() },
	}
	for name, stop := range stops {
		t.Run(name, func(t *testing.T) {
			b, r, release := startRepostInFlight(t)

			done := make(chan struct{})
			go func() {
				stop(b, r)
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
			if r.LastMessageID != 777 {
				t.Errorf("LastMessageID after stop = %d, want the reposted message 777", r.LastMessageID)
			}
		})
	}
}
