package threadbot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
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
		fakeThreadAPI: newFakeThreadAPI(),
		created:       make(chan struct{}, 1),
		release:       make(chan struct{}),
	}
	api.addThread("101", 5, 5)
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
	r := &threadReminder{
		ChannelID: 500, GuildID: guildID, Enabled: true,
		LastMessageID: 100, MinIdleMins: 0, MaxIdleMins: 10, MsgThreshold: 1,
	}
	b.reminders[guildID] = map[snowflake.ID]*threadReminder{500: r}
	b.mu.Lock()
	b.startReminderGoroutine(r)
	h := r.handle
	b.mu.Unlock()
	h.Signal()
	select {
	case <-api.created:
	case <-time.After(5 * time.Second):
		t.Fatal("repost did not reach Create Message")
	}
	return b, r, release
}

// The stop functions must wait for an in-flight repost without holding b.mu,
// which the repost takes to record the new message ID. /threadreminder
// disable then reads LastMessageID to delete the reminder, so it must be the
// new message's.
func TestStopWaitsForInFlightRepost(t *testing.T) {
	stops := map[string]func(*Bot, *threadReminder){
		"stopReminderGoroutine":     func(b *Bot, r *threadReminder) { b.stopReminderGoroutine(r) },
		"stopAllReminderGoroutines": func(b *Bot, _ *threadReminder) { b.stopAllReminderGoroutines() },
		// A command stopping a reminder during shutdown must wait for the
		// same repost as shutdown does, not find the loop already gone.
		"stopReminder after stopAll": func(b *Bot, r *threadReminder) {
			go b.stopAllReminderGoroutines()
			time.Sleep(20 * time.Millisecond)
			b.stopReminderGoroutine(r)
		},
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

// Re-enabling a channel's reminder replaces it. The new reminder must take
// over the message the old one shows, including one its in-flight repost
// posts while being stopped, or that message stays in the channel for good.
func TestEnableTakesOverReplacedReminderMessage(t *testing.T) {
	b, old, release := startRepostInFlight(t)
	e, responses := commandEvent(t, old.GuildID, enableData)

	done := make(chan struct{})
	go func() {
		b.onCommand(e)
		close(done)
	}()
	select {
	case <-responses:
	case <-time.After(5 * time.Second):
		t.Fatal("enable did not respond while the old reminder's repost was in flight")
	}
	select {
	case <-done:
		t.Fatal("enable returned while the old reminder's repost was in flight")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enable did not return after the old reminder's repost finished")
	}

	b.mu.Lock()
	r := b.reminders[old.GuildID][old.ChannelID]
	lastID, running := r.LastMessageID, r.handle != nil
	b.mu.Unlock()
	if r == old {
		t.Fatal("reminder was not replaced")
	}
	if lastID != 777 {
		t.Errorf("new reminder's LastMessageID = %d, want 777, the message the old reminder's repost posted", lastID)
	}
	if !running {
		t.Error("new reminder's loop was not started")
	}
	b.stopAllReminderGoroutines()
}

func channelDelete(guildID, channelID snowflake.ID) *events.GuildChannelDelete {
	return &events.GuildChannelDelete{GenericGuildChannel: &events.GenericGuildChannel{GuildID: guildID, ChannelID: channelID}}
}

// A deleted channel's reminder is stopped, waiting out a repost in flight,
// so no loop keeps running for it.
func TestChannelDeleteStopsReminderLoop(t *testing.T) {
	b, r, release := startRepostInFlight(t)

	done := make(chan struct{})
	go func() {
		b.onChannelDelete(channelDelete(r.GuildID, r.ChannelID))
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("channel delete returned while the reminder's repost was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("channel delete did not return after the repost finished")
	}
}

// A deleted channel's reminder is dropped from S3 too, so it doesn't
// restart on the next boot; other channels' reminders stay.
func TestChannelDeleteDropsStoredReminder(t *testing.T) {
	s3srv := httptest.NewServer(testutil.NewFakeS3())
	defer s3srv.Close()
	s3c := testutil.NewTestS3Client(t, s3srv)
	guildID := snowflake.ID(1013566342345019512)

	b := newTestBot(t, s3c)
	b.reminders[guildID] = map[snowflake.ID]*threadReminder{
		500: {ChannelID: 500, GuildID: guildID, Enabled: true, MinIdleMins: 30, MaxIdleMins: 720, MsgThreshold: 500},
		600: {ChannelID: 600, GuildID: guildID, Enabled: true, MinIdleMins: 30, MaxIdleMins: 720, MsgThreshold: 500},
	}
	b.mu.Lock()
	for _, r := range b.reminders[guildID] {
		b.startReminderGoroutine(r)
	}
	b.mu.Unlock()
	defer b.stopAllReminderGoroutines()
	b.saveRemindersForGuild(guildID)

	b.onChannelDelete(channelDelete(guildID, 500))
	b.onChannelDelete(channelDelete(guildID, 700))            // no reminder there
	b.onChannelDelete(channelDelete(726985544038612993, 500)) // guild not loaded

	restarted := newTestBot(t, s3c)
	restarted.loadReminders()
	defer restarted.stopAllReminderGoroutines()
	restarted.mu.Lock()
	defer restarted.mu.Unlock()
	if _, ok := restarted.reminders[guildID][500]; ok {
		t.Error("deleted channel's reminder reloaded after restart")
	}
	if _, ok := restarted.reminders[guildID][600]; !ok {
		t.Error("other channel's reminder lost")
	}
}
