package threadbot

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

func TestHiddenThreadsRoundTrip(t *testing.T) {
	set := map[snowflake.ID]struct{}{30: {}, 10: {}, 20: {}}
	data, err := encodeHiddenThreads(set)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"thread_ids":["10","20","30"]}`; got != want {
		t.Errorf("encoded %s, want %s (sorted, stable)", got, want)
	}
	back, err := decodeHiddenThreads(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 3 {
		t.Errorf("decoded %d ids, want 3", len(back))
	}
}

func TestHiddenThreadsExcludedPersistedAndPruned(t *testing.T) {
	api := &fakeThreadAPI{members: map[string]int{}, fetches: map[string]int{}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	s3srv := httptest.NewServer(testutil.NewFakeS3())
	defer s3srv.Close()
	s3c := testutil.NewTestS3Client(t, s3srv)

	b := newTestBot(t, s3c)
	b.Client = &bot.Client{Rest: rest.New(rest.NewClient("test", rest.WithURL(srv.URL)))}
	guildID := snowflake.ID(1013566342345019512)

	api.addThread("201", 5, 5)
	api.addThread("202", 5, 5)

	b.loadHiddenThreads()

	if changed, err := b.setHidden(guildID, 202, true); !changed || err != nil {
		t.Fatalf("hide: changed=%v err=%v", changed, err)
	}
	if changed, _ := b.setHidden(guildID, 202, true); changed {
		t.Error("hiding an already hidden thread reported a change")
	}

	d := b.buildAllThreadsEmbed(guildID).Description
	if !strings.Contains(d, "thread-201") || strings.Contains(d, "thread-202") {
		t.Errorf("hidden thread shown, or visible thread missing:\n%s", d)
	}
	if e := b.buildThreadEmbed(guildID, 500); e == nil || strings.Contains(e.Description, "thread-202") {
		t.Errorf("hidden thread shown in channel reminder: %+v", e)
	}

	// A restart reloads the hidden list from S3.
	b2 := newTestBot(t, s3c)
	b2.loadHiddenThreads()
	if _, ok := b2.hiddenSet(guildID)[202]; !ok {
		t.Fatal("hidden thread not reloaded from S3")
	}

	// Deleting the thread drops it from the list, and that is persisted too.
	b2.onThreadDelete(&events.ThreadDelete{GenericThread: &events.GenericThread{GuildID: guildID, ThreadID: 202}})
	if len(b2.hiddenSet(guildID)) != 0 {
		t.Error("deleted thread still hidden")
	}
	b3 := newTestBot(t, s3c)
	b3.loadHiddenThreads()
	if len(b3.hiddenSet(guildID)) != 0 {
		t.Error("thread delete was not persisted")
	}

	// Unhiding something that isn't hidden is a no-op.
	if changed, _ := b3.setHidden(guildID, 201, false); changed {
		t.Error("unhiding a visible thread reported a change")
	}
}

// flakyS3 wraps the fake S3 and fails reads or writes on demand. It answers
// 403, which the AWS SDK does not retry.
type flakyS3 struct {
	*testutil.FakeS3
	failGet, failPut atomic.Bool
}

func (f *flakyS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if (r.Method == http.MethodGet && f.failGet.Load()) || (r.Method == http.MethodPut && f.failPut.Load()) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>denied</Message></Error>`))
		return
	}
	f.FakeS3.ServeHTTP(w, r)
}

func TestSetHiddenRollsBackFailedSave(t *testing.T) {
	fs := &flakyS3{FakeS3: testutil.NewFakeS3()}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	b := newTestBot(t, testutil.NewTestS3Client(t, srv))
	guildID := snowflake.ID(1013566342345019512)
	b.loadHiddenThreads()

	fs.failPut.Store(true)
	if changed, err := b.setHidden(guildID, 300, true); changed || err == nil {
		t.Fatalf("failed save: changed=%v err=%v, want rollback with error", changed, err)
	}
	if len(b.hiddenSet(guildID)) != 0 {
		t.Fatal("failed save left the thread hidden in memory")
	}

	// The retry must actually save, not report "already hidden".
	fs.failPut.Store(false)
	if changed, err := b.setHidden(guildID, 300, true); !changed || err != nil {
		t.Fatalf("retry: changed=%v err=%v", changed, err)
	}
	b2 := newTestBot(t, testutil.NewTestS3Client(t, srv))
	b2.loadHiddenThreads()
	if _, ok := b2.hiddenSet(guildID)[300]; !ok {
		t.Error("retried hide was not persisted")
	}
}

func TestSetHiddenNeverOverwritesUnloadedList(t *testing.T) {
	fs := &flakyS3{FakeS3: testutil.NewFakeS3()}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	s3c := testutil.NewTestS3Client(t, srv)
	seed := newTestBot(t, s3c)
	for _, g := range []snowflake.ID{devGuild, prodGuild} {
		if _, err := seed.setHidden(g, 10, true); err != nil {
			t.Fatal(err)
		}
	}
	stored := func(g snowflake.ID) map[snowflake.ID]struct{} {
		t.Helper()
		check := newTestBot(t, s3c)
		return check.hiddenSet(g)
	}

	// S3 is unreachable at startup: changes are refused, not saved over the
	// stored list.
	fs.failGet.Store(true)
	b := newTestBot(t, s3c)
	b.loadHiddenThreads()
	if _, err := b.setHidden(devGuild, 20, true); !errors.Is(err, errHiddenNotLoaded) {
		t.Fatalf("hide while unloaded: err=%v, want errHiddenNotLoaded", err)
	}

	// Once S3 is back the load is retried and the change merges with it.
	fs.failGet.Store(false)
	if changed, err := b.setHidden(devGuild, 20, true); !changed || err != nil {
		t.Fatalf("hide after recovery: changed=%v err=%v", changed, err)
	}
	if set := stored(devGuild); len(set) != 2 {
		t.Errorf("dev guild stored list = %v, want threads 10 and 20", set)
	}

	// So does every other guild whose startup load failed.
	if changed, err := b.setHidden(prodGuild, 30, true); !changed || err != nil {
		t.Fatalf("hide in late guild: changed=%v err=%v", changed, err)
	}
	if set := stored(prodGuild); len(set) != 2 {
		t.Errorf("prod guild stored list = %v, want threads 10 and 30", set)
	}
}
