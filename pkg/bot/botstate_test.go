package bot

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/testutil"
)

func newStateBot(t *testing.T) (*Bot, *hookedS3) {
	t.Helper()
	h, srv := newHookedS3(t)
	b := &Bot{
		BaseBot: &botutil.BaseBot{
			S3:                   testutil.NewTestS3Client(t, srv),
			Log:                  testutil.DiscardLogger(),
			TestGuildIDsOverride: botutil.AllowedGuildIDs, // in every allowed guild
		},
		stop: make(chan struct{}),
	}
	b.initStateStores()
	return b, h
}

// storeFields returns every guildStateStore field of b by name, so these
// tests notice a store added without being wired up.
func storeFields(b *Bot) map[string]reflect.Value {
	stores := make(map[string]reflect.Value)
	v := reflect.ValueOf(b).Elem()
	for i := range v.NumField() {
		f := v.Type().Field(i)
		if strings.HasPrefix(f.Type.String(), "*bot.guildStateStore[") {
			stores[f.Name] = v.Field(i)
		}
	}
	return stores
}

func TestStateStoresListsEveryStore(t *testing.T) {
	b, _ := newStateBot(t)
	listed := make(map[uintptr]bool)
	for _, s := range b.stateStores() {
		listed[reflect.ValueOf(s).Pointer()] = true
	}
	fields := storeFields(b)
	if len(fields) == 0 {
		t.Fatal("found no store fields")
	}
	for name, f := range fields {
		if f.IsNil() {
			t.Errorf("%s not created by initStateStores", name)
		} else if !listed[f.Pointer()] {
			t.Errorf("%s missing from stateStores, so it is never loaded or retried", name)
		}
	}
}

// loadState must give every store state for every allowed guild without a
// gateway, and saveState must then write all of it.
func TestLoadStateThenSaveStateCoversEveryStoreAndGuild(t *testing.T) {
	b, h := newStateBot(t)
	b.loadState()
	b.saveState()

	for name, f := range storeFields(b) {
		key := f.Elem().FieldByName("s3Key").String()
		for _, guildID := range botutil.AllowedGuildIDs {
			if _, ok := h.object(fmt.Sprintf("/test-bucket/%s/%d.json", key, guildID)); !ok {
				t.Errorf("%s: no state saved for guild %d", name, guildID)
			}
		}
	}
}

const (
	prodGuild snowflake.ID = 726985544038612993
	devGuild  snowflake.ID = 1013566342345019512
)

// Every allowed guild loads at startup, but saveState writes only the guilds
// the bot is in: a dev instance sharing the bucket must not write its startup
// copy of prod's state over prod's changes.
func TestSaveStateSkipsGuildsBotIsNotIn(t *testing.T) {
	b, h := newStateBot(t)
	b.TestGuildIDsOverride = []snowflake.ID{devGuild}
	prodTickets := fmt.Sprintf("/test-bucket/tickets/%d.json", prodGuild)
	h.put(prodTickets, `{"counter":5}`)
	b.loadState()
	// Meanwhile the prod instance opens a ticket.
	h.put(prodTickets, `{"counter":6}`)

	b.saveState()

	for name, f := range storeFields(b) {
		key := f.Elem().FieldByName("s3Key").String()
		prodKey := fmt.Sprintf("/test-bucket/%s/%d.json", key, prodGuild)
		if got, ok := h.object(prodKey); ok && prodKey != prodTickets {
			t.Errorf("%s: prod state written by a bot not in prod: %s", name, got)
		}
		if _, ok := h.object(fmt.Sprintf("/test-bucket/%s/%d.json", key, devGuild)); !ok {
			t.Errorf("%s: dev state not saved", name)
		}
	}
	if got, _ := h.object(prodTickets); got != `{"counter":6}` {
		t.Errorf("prod tickets = %s, want prod's own update", got)
	}
}

func TestServedGuildsIncludesUnreadyAndUnavailable(t *testing.T) {
	const foreign snowflake.ID = 555
	caches := cache.New(cache.WithCaches(cache.FlagGuilds))
	caches.AddGuild(discord.Guild{ID: devGuild})
	caches.SetGuildUnready(devGuild, true)
	caches.SetGuildUnavailable(prodGuild, true) // in an outage
	caches.AddGuild(discord.Guild{ID: foreign})
	caches.SetGuildUnready(foreign, true)
	b := &Bot{BaseBot: &botutil.BaseBot{Client: &bot.Client{Caches: caches}}}

	got := b.servedGuilds()
	slices.Sort(got)
	if want := []snowflake.ID{prodGuild, devGuild}; !slices.Equal(got, want) {
		t.Errorf("servedGuilds = %v, want %v", got, want)
	}
}

// A loop that is mid-run at shutdown must finish before the final saves, or
// its change is lost.
func TestStopLoopsWaitsForRunningLoop(t *testing.T) {
	b, h := newStateBot(t)
	b.loadState()
	b.Ready.Store(true)
	guildID := botutil.AllowedGuildIDs[0]

	started := make(chan struct{})
	release := make(chan struct{})
	runs := 0
	b.startLoop(time.Millisecond, func() {
		runs++
		if runs > 1 {
			return
		}
		close(started)
		<-release
		st := b.tickets.get(guildID)
		st.mu.Lock()
		st.Counter = 42
		st.mu.Unlock()
	})
	<-started

	done := make(chan struct{})
	go func() {
		b.stopLoops()
		b.saveState()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("final saves ran while a loop was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-done

	body, _ := h.object(fmt.Sprintf("/test-bucket/tickets/%d.json", guildID))
	var saved ticketState
	if err := json.Unmarshal([]byte(body), &saved); err != nil {
		t.Fatalf("decoding saved tickets %q: %v", body, err)
	}
	if saved.Counter != 42 {
		t.Errorf("saved counter = %d, want the loop's 42", saved.Counter)
	}
}
