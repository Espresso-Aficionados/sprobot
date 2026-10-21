package threadbot

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
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
	api := newFakeThreadAPI()
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

	all, err := b.buildThreadsEmbed(guildID, 0)
	if err != nil || !strings.Contains(all.Description, "thread-201") || strings.Contains(all.Description, "thread-202") {
		t.Errorf("hidden thread shown, or visible thread missing: %+v, %v", all, err)
	}
	if e, err := b.buildThreadsEmbed(guildID, 500); e == nil || err != nil || strings.Contains(e.Description, "thread-202") {
		t.Errorf("hidden thread shown in channel reminder: %+v, %v", e, err)
	}

	// A restart reloads the hidden list from S3.
	b2 := newTestBot(t, s3c)
	b2.loadHiddenThreads()
	if _, ok := mustHiddenSet(t, b2, guildID)[202]; !ok {
		t.Fatal("hidden thread not reloaded from S3")
	}

	// Deleting the thread drops it from the list, and that is persisted too.
	b2.onThreadDelete(&events.ThreadDelete{GenericThread: &events.GenericThread{GuildID: guildID, ThreadID: 202}})
	if len(mustHiddenSet(t, b2, guildID)) != 0 {
		t.Error("deleted thread still hidden")
	}
	b3 := newTestBot(t, s3c)
	b3.loadHiddenThreads()
	if len(mustHiddenSet(t, b3, guildID)) != 0 {
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
	if len(mustHiddenSet(t, b, guildID)) != 0 {
		t.Fatal("failed save left the thread hidden in memory")
	}

	// The retry must actually save, not report "already hidden".
	fs.failPut.Store(false)
	if changed, err := b.setHidden(guildID, 300, true); !changed || err != nil {
		t.Fatalf("retry: changed=%v err=%v", changed, err)
	}
	b2 := newTestBot(t, testutil.NewTestS3Client(t, srv))
	b2.loadHiddenThreads()
	if _, ok := mustHiddenSet(t, b2, guildID)[300]; !ok {
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
		return mustHiddenSet(t, check, g)
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

// slowS3 holds every S3 request with the given method, reporting it on
// started, until it receives from release or release is closed.
type slowS3 struct {
	*testutil.FakeS3
	method  string
	started chan struct{}
	release chan struct{}
}

func newSlowS3(method string) *slowS3 {
	return &slowS3{FakeS3: testutil.NewFakeS3(), method: method, started: make(chan struct{}, 2), release: make(chan struct{})}
}

func (f *slowS3) object(key string) []byte {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.Objects[key]
}

func (f *slowS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == f.method {
		f.started <- struct{}{}
		<-f.release
	}
	f.FakeS3.ServeHTTP(w, r)
}

func mustHiddenSet(t *testing.T, b *Bot, guildID snowflake.ID) map[snowflake.ID]struct{} {
	t.Helper()
	set, err := b.hiddenSet(guildID)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// A hidden list that can't be loaded fails the thread lists rather than
// counting as empty, which would promote the threads mods hid.
func TestUnloadedHiddenListFailsClosed(t *testing.T) {
	api := newFakeThreadAPI()
	api.addThread("101", 5, 5)
	b := newThreadListBot(t, api)
	fs := &flakyS3{FakeS3: testutil.NewFakeS3()}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	b.S3 = testutil.NewTestS3Client(t, srv)
	fs.failGet.Store(true)

	if e, err := b.buildThreadsEmbed(devGuild, 500); e != nil || !errors.Is(err, errHiddenNotLoaded) {
		t.Errorf("embed = %+v, %v; want errHiddenNotLoaded", e, err)
	}
	r := &threadReminder{ChannelID: 500, GuildID: devGuild, Enabled: true}
	if b.repostReminder(r) || r.LastMessageID != 0 {
		t.Error("reminder reposted without the hidden list")
	}

	for _, tt := range []struct{ data, want string }{
		{threadsData, "Couldn't fetch the active threads"},
		{`{"id":"3","name":"threadbot","type":1,"options":[{"name":"hidden","type":1}]}`, "Couldn't load the hidden thread list"},
	} {
		e, responses := commandEvent(t, devGuild, tt.data)
		b.onCommand(e)
		if m := <-responses; m.Content != "" {
			t.Errorf("%s: first response %q, want a deferral", tt.data, m.Content)
		}
		if sent := api.sentFollowups(); len(sent) == 0 || !strings.Contains(sent[len(sent)-1].Content, tt.want) {
			t.Errorf("%s: follow-ups = %+v, want one saying %q", tt.data, sent, tt.want)
		}
	}
}

// /threadbot hide acknowledges the interaction before touching S3, so a slow
// save can't make it miss Discord's 3-second deadline, then reports the
// outcome in a follow-up.
func TestHideDefersBeforeSaving(t *testing.T) {
	s3 := newSlowS3(http.MethodPut)
	s3srv := httptest.NewServer(s3)
	defer s3srv.Close()
	defer close(s3.release)
	api := newFakeThreadAPI()
	b := newThreadListBot(t, api)
	b.S3 = testutil.NewTestS3Client(t, s3srv)

	e, responses := commandEvent(t, devGuild, `{"id":"3","name":"threadbot","type":1,
		"options":[{"name":"hide","type":1,"options":[{"name":"thread","type":7,"value":"101"}]}],
		"resolved":{"channels":{"101":{"id":"101","name":"t","type":11,"permissions":"0","parent_id":"500"}}}}`)
	done := make(chan struct{})
	go func() {
		b.onCommand(e)
		close(done)
	}()

	select {
	case <-s3.started:
	case <-time.After(5 * time.Second):
		t.Fatal("hide never saved")
	}
	select {
	case m := <-responses:
		if m.Flags != discord.MessageFlagEphemeral || m.Content != "" {
			t.Errorf("first response = %+v, want an ephemeral deferral", m)
		}
	default:
		t.Fatal("hide did not answer the interaction before saving")
	}

	s3.release <- struct{}{}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hide did not finish after the save")
	}
	if sent := api.sentFollowups(); len(sent) != 1 || !strings.Contains(sent[0].Content, "is now hidden") {
		t.Errorf("follow-ups = %+v, want the outcome", sent)
	}
}
