package bot

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

type testState struct {
	mu    sync.Mutex
	Value string `json:"value"`
}

// hookedS3 serves requests from a FakeS3, first passing each to hook. A
// request the hook rejects fails with a 403. Unlike FakeS3.Fail, that can fail
// reads while letting writes through, so tests can see what gets written.
type hookedS3 struct {
	fake *testutil.FakeS3
	mu   sync.Mutex
	hook func(*http.Request) bool
}

func (h *hookedS3) setHook(hook func(*http.Request) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hook = hook
}

func (h *hookedS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	hook := h.hook
	h.mu.Unlock()
	if hook != nil && !hook(r) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Forbidden</Message></Error>`)
		return
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		h.list(w, r)
		return
	}
	h.fake.ServeHTTP(w, r)
}

// list answers a ListObjectsV2 request, which FakeS3 doesn't, in one page.
func (h *hookedS3) list(w http.ResponseWriter, r *http.Request) {
	bucketPath := "/" + strings.Trim(r.URL.Path, "/") + "/"
	prefix := r.URL.Query().Get("prefix")
	var contents strings.Builder
	h.fake.Mu.Lock()
	for path := range h.fake.Objects {
		if key, ok := strings.CutPrefix(path, bucketPath); ok && strings.HasPrefix(key, prefix) {
			fmt.Fprintf(&contents, "<Contents><Key>%s</Key></Contents>", key)
		}
	}
	h.fake.Mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Prefix>%s</Prefix><IsTruncated>false</IsTruncated>%s</ListBucketResult>`,
		prefix, contents.String())
}

// newHookedS3 starts a fake S3 server behind a hookedS3.
func newHookedS3(t *testing.T) (*hookedS3, *httptest.Server) {
	t.Helper()
	h := &hookedS3{fake: testutil.NewFakeS3()}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, srv
}

// failGets rejects GET requests for path.
func failGets(path string) func(*http.Request) bool {
	return func(r *http.Request) bool { return r.Method != http.MethodGet || r.URL.Path != path }
}

const testGuild snowflake.ID = 123

func testObject(guildID snowflake.ID) string {
	return fmt.Sprintf("/test-bucket/test/%d.json", guildID)
}

func newTestStore(t *testing.T) (*guildStateStore[testState], *hookedS3) {
	t.Helper()
	h, srv := newHookedS3(t)
	store := newGuildStateStore(testutil.NewTestS3Client(t, srv), testutil.DiscardLogger(), "test",
		func() *testState { return &testState{} },
		func(st *testState) *sync.Mutex { return &st.mu })
	return store, h
}

func (h *hookedS3) put(path, body string) {
	h.fake.Mu.Lock()
	defer h.fake.Mu.Unlock()
	h.fake.Objects[path] = []byte(body)
}

func (h *hookedS3) object(path string) (string, bool) {
	h.fake.Mu.Lock()
	defer h.fake.Mu.Unlock()
	body, ok := h.fake.Objects[path]
	return string(body), ok
}

func value(st *testState) string {
	if st == nil {
		return "<nil>"
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.Value
}

func TestGuildStateStoreGetSet(t *testing.T) {
	store, _ := newTestStore(t)

	if got := store.get(testGuild); got != nil {
		t.Error("expected nil for missing guild")
	}

	store.set(testGuild, &testState{Value: "hello"})
	if got := value(store.get(testGuild)); got != "hello" {
		t.Errorf("Value = %q, want %q", got, "hello")
	}

	store.set(testGuild, &testState{Value: "second"})
	if got := value(store.get(testGuild)); got != "second" {
		t.Errorf("Value after overwrite = %q, want %q", got, "second")
	}
}

func TestGuildStateStoreGetOrCreate(t *testing.T) {
	store, _ := newTestStore(t)
	store.newState = func() *testState { return &testState{Value: "new"} }

	st := store.getOrCreate(testGuild)
	if value(st) != "new" {
		t.Fatalf("created state = %q, want newState's", value(st))
	}
	if again := store.getOrCreate(testGuild); again != st {
		t.Error("second getOrCreate made another state")
	}
	if store.get(testGuild) != st {
		t.Error("created state not stored")
	}
}

func TestGuildStateStoreEach(t *testing.T) {
	store, _ := newTestStore(t)
	store.set(100, &testState{Value: "a"})
	store.set(200, &testState{Value: "b"})

	seen := make(map[snowflake.ID]string)
	store.each(func(guildID snowflake.ID, st *testState) {
		seen[guildID] = st.Value
		store.set(guildID, st) // each holds no store lock while calling fn
	})

	if len(seen) != 2 || seen[100] != "a" || seen[200] != "b" {
		t.Errorf("each visited %v, want 100=a and 200=b", seen)
	}
}

func TestGuildStateStoreLoadMissingUsesOnMissing(t *testing.T) {
	store, _ := newTestStore(t)
	store.onMissing = func(guildID snowflake.ID, st *testState) { st.Value = fmt.Sprintf("default %d", guildID) }
	store.postLoad = func(*testState) { t.Error("postLoad called without data") }

	store.load([]snowflake.ID{testGuild})

	if got := value(store.get(testGuild)); got != "default 123" {
		t.Errorf("Value = %q, want onMissing's default", got)
	}
}

func TestGuildStateStoreLoadDecodesAndRunsPostLoad(t *testing.T) {
	store, h := newTestStore(t)
	h.put(testObject(testGuild), `{"value":"saved"}`)
	store.onMissing = func(snowflake.ID, *testState) { t.Error("onMissing called for existing data") }
	store.postLoad = func(st *testState) { st.Value += " +postLoad" }

	store.load([]snowflake.ID{testGuild})

	if got := value(store.get(testGuild)); got != "saved +postLoad" {
		t.Errorf("Value = %q, want the saved value after postLoad", got)
	}
}

// A fetch error other than not-found must not install fresh state: it would
// be saved over the real data, e.g. resetting the ticket counter.
func TestGuildStateStoreFetchErrorRefusesWritesUntilRetry(t *testing.T) {
	store, h := newTestStore(t)
	const other snowflake.ID = 456
	h.put(testObject(testGuild), `{"value":"real"}`)
	h.setHook(failGets(testObject(testGuild)))
	store.onMissing = func(_ snowflake.ID, st *testState) { st.Value = "default" }

	store.load([]snowflake.ID{testGuild, other})

	if st := store.get(testGuild); st != nil {
		t.Fatalf("failed guild has state %q, want none", value(st))
	}
	store.set(testGuild, &testState{Value: "fresh"})
	if st := store.get(testGuild); st != nil {
		t.Errorf("set installed state %q for a failed guild", value(st))
	}
	if st := store.getOrCreate(testGuild); st != nil {
		t.Errorf("getOrCreate created state %q for a failed guild", value(st))
	}
	if err := store.persist(testGuild); !errors.Is(err, errNotLoaded) {
		t.Errorf("persist = %v, want errNotLoaded", err)
	}
	store.save()
	if got, _ := h.object(testObject(testGuild)); got != `{"value":"real"}` {
		t.Errorf("stored data = %q, want it untouched", got)
	}
	if got, ok := h.object(testObject(other)); !ok || !strings.Contains(got, "default") {
		t.Errorf("other guild's state = %q, %v, want it saved", got, ok)
	}

	// Still failing: the retry changes nothing.
	store.retryFailedLoads()
	if store.get(testGuild) != nil {
		t.Fatal("retry installed state while the fetch still fails")
	}

	h.setHook(nil)
	store.retryFailedLoads()
	st := store.get(testGuild)
	if value(st) != "real" {
		t.Fatalf("Value after retry = %q, want the stored data", value(st))
	}
	st.mu.Lock()
	st.Value = "updated"
	st.mu.Unlock()
	if err := store.persist(testGuild); err != nil {
		t.Fatalf("persist after retry: %v", err)
	}
	if got, _ := h.object(testObject(testGuild)); got != `{"value":"updated"}` {
		t.Errorf("stored data after retry = %q, want the update", got)
	}
}

func corruptBackups(h *hookedS3) map[string]string {
	h.fake.Mu.Lock()
	defer h.fake.Mu.Unlock()
	backups := make(map[string]string)
	for key, body := range h.fake.Objects {
		if strings.HasPrefix(key, "/test-bucket/corrupt/test/123-") {
			backups[key] = string(body)
		}
	}
	return backups
}

func TestGuildStateStoreDecodeErrorBacksUpThenUsesDefaults(t *testing.T) {
	store, h := newTestStore(t)
	h.put(testObject(testGuild), `{"value":`)
	store.onMissing = func(_ snowflake.ID, st *testState) { st.Value = "default" }

	store.load([]snowflake.ID{testGuild})

	backups := corruptBackups(h)
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one", backups)
	}
	for key, body := range backups {
		if body != `{"value":` {
			t.Errorf("backup %s = %q, want the raw data", key, body)
		}
	}
	if got := value(store.get(testGuild)); got != "default" {
		t.Errorf("Value = %q, want onMissing's default", got)
	}
}

// Without a backup, installing defaults would lose the only copy on the next
// save, so a failed backup counts as a failed load.
func TestGuildStateStoreDecodeErrorBackupFailureIsALoadFailure(t *testing.T) {
	store, h := newTestStore(t)
	h.put(testObject(testGuild), `{"value":`)
	h.setHook(func(r *http.Request) bool { return !strings.HasPrefix(r.URL.Path, "/test-bucket/corrupt/") })

	store.load([]snowflake.ID{testGuild})

	if st := store.get(testGuild); st != nil {
		t.Fatalf("state %q installed without a backup", value(st))
	}
	if err := store.persist(testGuild); !errors.Is(err, errNotLoaded) {
		t.Errorf("persist = %v, want errNotLoaded", err)
	}

	h.setHook(nil)
	store.retryFailedLoads()
	if store.get(testGuild) == nil {
		t.Error("no state after the retry backed up the data")
	}
	if len(corruptBackups(h)) != 1 {
		t.Error("retry wrote no backup")
	}
}

// Backups hold user data, so they are deleted once corruptRetention is up.
func TestGuildStateStoreCorruptBackupsExpire(t *testing.T) {
	store, h := newTestStore(t)
	h.put(testObject(testGuild), `{"value":`)
	const undated = "/test-bucket/corrupt/test/notes.json"
	h.put(undated, "{}")
	store.load([]snowflake.ID{testGuild})
	if len(corruptBackups(h)) != 1 {
		t.Fatal("no backup written")
	}

	pruneCorruptBackups(store.s3, store.log, time.Now())
	if len(corruptBackups(h)) != 1 {
		t.Fatal("backup deleted before its retention ended")
	}

	pruneCorruptBackups(store.s3, store.log, time.Now().Add(corruptRetention+time.Minute))
	if backups := corruptBackups(h); len(backups) != 0 {
		t.Errorf("backups kept past their retention: %v", backups)
	}
	for _, path := range []string{undated, testObject(testGuild)} {
		if _, ok := h.object(path); !ok {
			t.Errorf("%s deleted; only dated backups expire", path)
		}
	}
}

// save and each skip guilds the instance isn't in: another deployment
// sharing the bucket may be the one changing them.
func TestGuildStateStoreSkipsGuildsNotServed(t *testing.T) {
	store, h := newTestStore(t)
	const served, notServed snowflake.ID = 100, 200
	store.setGuilds(func() []snowflake.ID { return []snowflake.ID{served} })
	store.set(served, &testState{Value: "a"})
	store.set(notServed, &testState{Value: "b"})

	var seen []snowflake.ID
	store.each(func(guildID snowflake.ID, _ *testState) { seen = append(seen, guildID) })
	if !slices.Equal(seen, []snowflake.ID{served}) {
		t.Errorf("each visited %v, want only %d", seen, served)
	}

	store.save()
	if _, ok := h.object(testObject(served)); !ok {
		t.Error("served guild not saved")
	}
	if got, ok := h.object(testObject(notServed)); ok {
		t.Errorf("guild the instance isn't in was saved: %s", got)
	}
}

// An S3 call that never returns must not hold a guild's save lock, a save
// loop, shutdown or startup forever.
func TestGuildStateStoreS3CallsTimeOut(t *testing.T) {
	store, h := newTestStore(t)
	old := storeS3Timeout
	storeS3Timeout = 50 * time.Millisecond
	t.Cleanup(func() { storeS3Timeout = old })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // before the server closes, which waits for handlers
	h.setHook(func(*http.Request) bool { <-release; return true })

	store.set(testGuild, &testState{Value: "x"})
	const other snowflake.ID = 456
	done := make(chan error, 1)
	go func() {
		_ = store.persist(testGuild)
		store.load([]snowflake.ID{other})
		done <- store.persist(testGuild) // waits for the first persist's lock
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("persist succeeded without an answer from S3")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("store still blocked on S3 calls that never return")
	}
	if st := store.get(other); st != nil {
		t.Errorf("timed-out load installed state %q", value(st))
	}
}

// Two persists of one guild must land in the order their snapshots were
// taken. Here the first PUT is held up while the state changes and a second
// persist starts; the newer state must win.
func TestGuildStateStorePersistLandsInOrder(t *testing.T) {
	store, h := newTestStore(t)
	st := &testState{Value: "old"}
	store.set(testGuild, st)

	var puts atomic.Int32
	firstArrived := make(chan struct{})
	secondArrived := make(chan struct{})
	release := make(chan struct{})
	h.setHook(func(r *http.Request) bool {
		if r.Method != http.MethodPut {
			return true
		}
		switch puts.Add(1) {
		case 1:
			close(firstArrived)
			<-release
		case 2:
			close(secondArrived)
		}
		return true
	})

	errs := make(chan error, 2)
	go func() { errs <- store.persist(testGuild) }()
	<-firstArrived

	st.mu.Lock()
	st.Value = "new"
	st.mu.Unlock()
	go func() { errs <- store.persist(testGuild) }()

	// Unfixed, the second PUT overtakes the first; fixed, it waits for it.
	select {
	case <-secondArrived:
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("persist: %v", err)
		}
	}

	if got, _ := h.object(testObject(testGuild)); got != `{"value":"new"}` {
		t.Errorf("stored data = %q, want the newer state", got)
	}
}

// Run with -race: handlers, save loops and retries use a store concurrently.
func TestGuildStateStoreConcurrentUse(t *testing.T) {
	store, h := newTestStore(t)
	h.setHook(failGets(testObject(7)))
	store.load([]snowflake.ID{7})

	var wg sync.WaitGroup
	for i := range 8 {
		guildID := snowflake.ID(100 + i%3)
		wg.Go(func() {
			for range 20 {
				store.getOrCreate(guildID)
				store.set(guildID, &testState{Value: "x"})
				if st := store.get(guildID); st != nil {
					st.mu.Lock()
					st.Value = "y"
					st.mu.Unlock()
				}
				_ = store.persist(guildID)
			}
		})
		wg.Go(func() {
			for range 5 {
				store.each(func(snowflake.ID, *testState) {})
				store.save()
				store.retryFailedLoads()
			}
		})
	}
	wg.Wait()
}
