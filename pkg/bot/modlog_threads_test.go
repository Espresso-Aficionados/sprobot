package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	disgobot "github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/testutil"
)

const (
	testModLogGuild snowflake.ID = 1013566342345019512
	testModLogForum snowflake.ID = 600
)

type fakeModLogThread struct {
	id         snowflake.ID
	name       string
	archivedAt time.Time // zero while active
}

// fakeModLogAPI serves the Discord endpoints the mod log uses: the forum
// channel, active and archived thread listings, forum post creation and
// message posts.
type fakeModLogAPI struct {
	mu           sync.Mutex
	threads      []fakeModLogThread
	nextID       snowflake.ID
	failActive   int                  // status for the active thread listing; 0 = OK
	failArchived int                  // status for the archived thread listing; 0 = OK
	postStatus   map[snowflake.ID]int // thread ID -> status for message posts
	createDelay  time.Duration
	onArchived   func()         // called once, before the next archived listing is served
	calls        map[string]int // "channel", "active", "archived", "create"
	posts        map[snowflake.ID]int
}

func newFakeModLogAPI() *fakeModLogAPI {
	return &fakeModLogAPI{nextID: 5000, postStatus: map[snowflake.ID]int{}, calls: map[string]int{}, posts: map[snowflake.ID]int{}}
}

func (f *fakeModLogAPI) addThread(id snowflake.ID, name string, archivedAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threads = append(f.threads, fakeModLogThread{id: id, name: name, archivedAt: archivedAt})
}

// deleteThread removes a thread without any gateway event, as if it was
// deleted while the bot was offline. Posts to it answer Unknown Channel.
func (f *fakeModLogAPI) deleteThread(id snowflake.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threads = slices.DeleteFunc(f.threads, func(t fakeModLogThread) bool { return t.id == id })
	f.postStatus[id] = http.StatusNotFound
}

func (f *fakeModLogAPI) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeModLogAPI) postCount(threadID snowflake.ID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts[threadID]
}

func threadJSON(t fakeModLogThread) map[string]any {
	meta := map[string]any{"archived": !t.archivedAt.IsZero(), "auto_archive_duration": 10080}
	if !t.archivedAt.IsZero() {
		meta["archive_timestamp"] = t.archivedAt.Format(time.RFC3339Nano)
	}
	return map[string]any{
		"id": t.id.String(), "type": 11, "guild_id": testModLogGuild.String(), "parent_id": testModLogForum.String(),
		"name": t.name, "thread_metadata": meta,
	}
}

func writeDiscordError(w http.ResponseWriter, status int) {
	w.WriteHeader(status)
	code := 0
	if status == http.StatusNotFound {
		code = 10003 // Unknown Channel
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": http.StatusText(status)})
}

func (f *fakeModLogAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	forum := testModLogForum.String()

	switch {
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "channels" && parts[1] == forum:
		f.mu.Lock()
		f.calls["channel"]++
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"id": forum, "type": 15, "guild_id": testModLogGuild.String(), "name": "mod-log"})

	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/threads/active"):
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls["active"]++
		if f.failActive != 0 {
			writeDiscordError(w, f.failActive)
			return
		}
		threads := []any{}
		for _, t := range f.threads {
			if t.archivedAt.IsZero() {
				threads = append(threads, threadJSON(t))
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"threads": threads, "members": []any{}})

	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/threads/archived/public"):
		f.mu.Lock()
		hook := f.onArchived
		f.onArchived = nil
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls["archived"]++
		if f.failArchived != 0 {
			writeDiscordError(w, f.failArchived)
			return
		}
		var archived []fakeModLogThread
		for _, t := range f.threads {
			if !t.archivedAt.IsZero() {
				archived = append(archived, t)
			}
		}
		slices.SortFunc(archived, func(a, b fakeModLogThread) int { return b.archivedAt.Compare(a.archivedAt) })
		if before, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("before")); err == nil {
			archived = slices.DeleteFunc(archived, func(t fakeModLogThread) bool { return !t.archivedAt.Before(before) })
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit == 0 {
			limit = 50
		}
		hasMore := len(archived) > limit
		if hasMore {
			archived = archived[:limit]
		}
		threads := []any{}
		for _, t := range archived {
			threads = append(threads, threadJSON(t))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"threads": threads, "members": []any{}, "has_more": hasMore})

	case r.Method == http.MethodPost && r.URL.Path == "/channels/"+forum+"/threads":
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		delay := f.createDelay
		f.mu.Unlock()
		time.Sleep(delay)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls["create"]++
		f.nextID++
		thread := fakeModLogThread{id: f.nextID, name: body.Name}
		f.threads = append(f.threads, thread)
		resp := threadJSON(thread)
		resp["message"] = map[string]any{"id": thread.id.String(), "channel_id": thread.id.String(), "content": "topic"}
		_ = json.NewEncoder(w).Encode(resp)

	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "channels" && parts[2] == "messages":
		id, _ := snowflake.Parse(parts[1])
		f.mu.Lock()
		defer f.mu.Unlock()
		f.posts[id]++
		if status := f.postStatus[id]; status != 0 {
			writeDiscordError(w, status)
			return
		}
		f.nextID++
		_ = json.NewEncoder(w).Encode(map[string]any{"id": f.nextID.String(), "channel_id": id.String()})

	default:
		http.NotFound(w, r)
	}
}

// newModLogTestBot returns a bot talking to api, with a fake S3 and an empty
// channel cache.
func newModLogTestBot(t *testing.T, api *fakeModLogAPI) (*Bot, *testutil.FakeS3) {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	fakeS3 := testutil.NewFakeS3()
	s3srv := httptest.NewServer(fakeS3)
	t.Cleanup(s3srv.Close)

	b := &Bot{BaseBot: &botutil.BaseBot{S3: testutil.NewTestS3Client(t, s3srv), Log: testutil.DiscardLogger()}}
	b.Client = &disgobot.Client{
		Rest:   rest.New(rest.NewClient("test", rest.WithURL(srv.URL), rest.WithLogger(testutil.DiscardLogger()))),
		Caches: cache.New(cache.WithCaches(cache.FlagChannels)),
	}
	return b, fakeS3
}

// cacheForum puts the mod log forum in the bot's channel cache.
func cacheForum(t *testing.T, b *Bot) {
	t.Helper()
	var forum discord.GuildForumChannel
	data := fmt.Sprintf(`{"id":"%d","type":15,"guild_id":"%d","name":"mod-log"}`, testModLogForum, testModLogGuild)
	if err := json.Unmarshal([]byte(data), &forum); err != nil {
		t.Fatal(err)
	}
	b.Client.Caches.AddChannel(forum)
}

func TestFindOrCreateModLogThreadScansOnce(t *testing.T) {
	api := newFakeModLogAPI()
	b, _ := newModLogTestBot(t, api)
	cacheForum(t, b)

	// 120 archived threads need three pages of 50, or two of 100.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 120 {
		api.addThread(snowflake.ID(100+i), fmt.Sprintf("user%d - %d", i, 10000+i), start.Add(time.Duration(i)*time.Hour))
	}
	api.addThread(300, "active - 42", time.Time{})

	find := func(user discord.User) snowflake.ID {
		t.Helper()
		id, err := b.findOrCreateModLogThread(testModLogForum, user)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	// The oldest archived thread is on the last page.
	if got := find(discord.User{ID: 10000, Username: "user0"}); got != 100 {
		t.Errorf("found thread %d, want 100", got)
	}
	if got := find(discord.User{ID: 42, Username: "active"}); got != 300 {
		t.Errorf("found thread %d, want 300", got)
	}
	newThread := find(discord.User{ID: 77, Username: "al"})
	if api.count("create") != 1 {
		t.Fatalf("%d threads created, want 1", api.count("create"))
	}
	if got := find(discord.User{ID: 77, Username: "al"}); got != newThread {
		t.Errorf("second lookup found %d, want the created thread %d", got, newThread)
	}

	// One scan served every lookup, and the cached forum needed no fetch.
	if api.count("active") != 1 || api.count("archived") != 2 {
		t.Errorf("listings: %d active, %d archived; want 1 and 2", api.count("active"), api.count("archived"))
	}
	if api.count("channel") != 0 {
		t.Errorf("forum fetched %d times despite being cached", api.count("channel"))
	}
	if api.count("create") != 1 {
		t.Errorf("%d threads created, want 1", api.count("create"))
	}
}

func TestFindOrCreateModLogThreadFetchesUncachedForum(t *testing.T) {
	api := newFakeModLogAPI()
	b, _ := newModLogTestBot(t, api)
	api.addThread(300, "alice - 1", time.Time{})

	if id, err := b.findOrCreateModLogThread(testModLogForum, discord.User{ID: 1, Username: "alice"}); err != nil || id != 300 {
		t.Fatalf("got %d, %v; want 300", id, err)
	}
	if api.count("channel") != 1 {
		t.Errorf("forum fetched %d times, want 1", api.count("channel"))
	}
}

func TestFindOrCreateModLogThreadListingErrors(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		failActive, failArchived int
	}{
		{"active", http.StatusForbidden, 0},
		{"archived", 0, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeModLogAPI()
			api.failActive, api.failArchived = tc.failActive, tc.failArchived
			b, _ := newModLogTestBot(t, api)
			cacheForum(t, b)

			if _, err := b.findOrCreateModLogThread(testModLogForum, discord.User{ID: 1, Username: "alice"}); err == nil {
				t.Fatal("expected an error when the threads can't be listed")
			}
			if api.count("create") != 0 {
				t.Error("a thread was created without knowing whether one exists")
			}

			// The failed scan isn't remembered: once listing works, it's retried.
			api.mu.Lock()
			api.failActive, api.failArchived = 0, 0
			api.mu.Unlock()
			if _, err := b.findOrCreateModLogThread(testModLogForum, discord.User{ID: 1, Username: "alice"}); err != nil {
				t.Fatal(err)
			}
			if api.count("create") != 1 {
				t.Errorf("%d threads created, want 1", api.count("create"))
			}
		})
	}
}

func TestListModLogThreadsReturnsPartialResults(t *testing.T) {
	api := newFakeModLogAPI()
	api.failArchived = http.StatusForbidden
	b, _ := newModLogTestBot(t, api)
	cacheForum(t, b)
	api.addThread(300, "alice - 1", time.Time{})

	forum, err := b.modLogForumChannel(testModLogForum)
	if err != nil {
		t.Fatal(err)
	}
	threads, err := b.listModLogThreads(forum)
	if err == nil || len(threads) != 1 || threads[0].ID() != 300 {
		t.Errorf("got %d threads, err %v; want the active thread and an error", len(threads), err)
	}
}

func TestFindOrCreateModLogThreadSerializesPerUser(t *testing.T) {
	// Unindexed, the forum scan lock also lines the calls up. Once the forum
	// is indexed, lookups skip that lock and only the per-user lock does.
	for _, indexed := range []bool{false, true} {
		t.Run(fmt.Sprintf("indexed=%v", indexed), func(t *testing.T) {
			api := newFakeModLogAPI()
			api.createDelay = 50 * time.Millisecond
			b, _ := newModLogTestBot(t, api)
			cacheForum(t, b)
			api.addThread(300, "bob - 2", time.Time{})
			if indexed {
				if _, err := b.findOrCreateModLogThread(testModLogForum, discord.User{ID: 2, Username: "bob"}); err != nil {
					t.Fatal(err)
				}
			}

			// Posts about the same user (say, a timeout and a ban) at once.
			user := discord.User{ID: 1, Username: "alice"}
			ids := make([]snowflake.ID, 4)
			var wg sync.WaitGroup
			for i := range ids {
				wg.Go(func() {
					id, err := b.findOrCreateModLogThread(testModLogForum, user)
					if err != nil {
						t.Error(err)
					}
					ids[i] = id
				})
			}
			wg.Wait()

			if api.count("create") != 1 {
				t.Errorf("%d threads created for one user, want 1", api.count("create"))
			}
			for i, id := range ids {
				if id != ids[0] {
					t.Errorf("call %d got thread %d, want %d", i, id, ids[0])
				}
			}
		})
	}
}

func TestListModLogThreadsPagesBySubSecondTimestamps(t *testing.T) {
	api := newFakeModLogAPI()
	b, _ := newModLogTestBot(t, api)
	cacheForum(t, b)

	// The first page of 100 ends with a thread archived at .9 of a second. The
	// victim's thread was archived earlier in that same second.
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := range 99 {
		api.addThread(snowflake.ID(1000+i), fmt.Sprintf("user%d - %d", i, 10000+i), base.Add(time.Duration(i+2)*time.Second))
	}
	api.addThread(2000, "x - 900", base.Add(900*time.Millisecond))
	api.addThread(2001, "victim - 777", base.Add(500*time.Millisecond))

	id, err := b.findOrCreateModLogThread(testModLogForum, discord.User{ID: 777, Username: "victim"})
	if err != nil {
		t.Fatal(err)
	}
	if id != 2001 || api.count("create") != 0 {
		t.Errorf("got thread %d with %d created; want the victim's thread 2001", id, api.count("create"))
	}
}

func TestModLogScanKeepsThreadEventsFromDuringIt(t *testing.T) {
	api := newFakeModLogAPI()
	b, _ := newModLogTestBot(t, api)
	cacheForum(t, b)
	api.addThread(300, "alice - 1", time.Time{})

	// A mod creates bob's thread after the active threads were listed.
	api.onArchived = func() {
		api.addThread(400, "bob - 456", time.Time{})
		b.onModLogThreadCreate(&events.ThreadCreate{GenericThread: &events.GenericThread{
			ThreadID: 400, GuildID: testModLogGuild, Thread: testThread(t, 400, "bob - 456"),
		}})
	}
	if _, err := b.findOrCreateModLogThread(testModLogForum, discord.User{ID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}
	id, err := b.findOrCreateModLogThread(testModLogForum, discord.User{ID: 456, Username: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if id != 400 || api.count("create") != 0 || api.count("active") != 1 {
		t.Errorf("bob -> %d with %d created, %d scans; want 400 from the event, 0 created, 1 scan",
			id, api.count("create"), api.count("active"))
	}
}

func TestModLogScanDiscardedByDeleteDuringIt(t *testing.T) {
	api := newFakeModLogAPI()
	b, _ := newModLogTestBot(t, api)
	cacheForum(t, b)
	api.addThread(300, "alice - 1", time.Time{})
	api.addThread(301, "alice - 1", time.Time{})

	// Alice's first thread is deleted after it was listed, so the scan that
	// listed it must not be used.
	api.onArchived = func() {
		api.deleteThread(300)
		b.onModLogThreadDelete(&events.ThreadDelete{GenericThread: &events.GenericThread{ThreadID: 300, ParentID: testModLogForum}})
	}
	id, err := b.findOrCreateModLogThread(testModLogForum, discord.User{ID: 1, Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if id != 301 || api.count("active") != 2 {
		t.Errorf("alice -> %d after %d scans; want her other thread 301 after a rescan", id, api.count("active"))
	}
}

func TestModLogReadyForgetsIndex(t *testing.T) {
	api := newFakeModLogAPI()
	b, _ := newModLogTestBot(t, api)
	cacheForum(t, b)
	api.addThread(300, "alice - 1", time.Time{})
	if _, err := b.findOrCreateModLogThread(testModLogForum, discord.User{ID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	// Carol's thread was created while the gateway was down. A new session
	// doesn't replay its event.
	api.addThread(301, "carol - 3", time.Time{})
	b.onModLogReady(&events.Ready{})
	id, err := b.findOrCreateModLogThread(testModLogForum, discord.User{ID: 3, Username: "carol"})
	if err != nil {
		t.Fatal(err)
	}
	if id != 301 || api.count("create") != 0 || api.count("active") != 2 {
		t.Errorf("carol -> %d with %d created, %d scans; want 301 from a rescan", id, api.count("create"), api.count("active"))
	}
}

func TestPostToModLogThreadRecreatesDeletedThread(t *testing.T) {
	api := newFakeModLogAPI()
	b, _ := newModLogTestBot(t, api)
	cacheForum(t, b)
	api.addThread(300, "alice - 1", time.Time{})
	user := discord.User{ID: 1, Username: "alice"}

	if _, err := b.findOrCreateModLogThread(testModLogForum, user); err != nil {
		t.Fatal(err)
	}
	api.deleteThread(300) // no ThreadDelete event reaches the bot

	threadID, sent, err := b.postToModLogThread(testModLogForum, user, discord.MessageCreate{Content: "x"})
	if err != nil || sent == nil {
		t.Fatalf("post failed: %v", err)
	}
	if threadID == 300 || api.count("create") != 1 {
		t.Errorf("posted to %d with %d threads created; want a new thread", threadID, api.count("create"))
	}
	if api.postCount(300) != 1 || api.postCount(threadID) != 1 {
		t.Errorf("posts: %d to the deleted thread, %d to the new one; want 1 each", api.postCount(300), api.postCount(threadID))
	}
	if api.count("active") != 2 {
		t.Errorf("%d active listings, want 2 (the rescan)", api.count("active"))
	}

	// Other errors are returned without a rescan.
	api.mu.Lock()
	api.postStatus[threadID] = http.StatusForbidden
	api.mu.Unlock()
	if _, _, err := b.postToModLogThread(testModLogForum, user, discord.MessageCreate{Content: "x"}); err == nil {
		t.Error("expected the 403 to be returned")
	}
	if api.count("active") != 2 {
		t.Error("a 403 triggered a rescan")
	}
}

func TestModLogThreadEventsKeepIndexCurrent(t *testing.T) {
	api := newFakeModLogAPI()
	b, _ := newModLogTestBot(t, api)
	cacheForum(t, b)
	api.addThread(300, "alice - 1", time.Time{})
	api.addThread(301, "bob - 2", time.Time{})

	find := func(user discord.User) snowflake.ID {
		t.Helper()
		id, err := b.findOrCreateModLogThread(testModLogForum, user)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	find(discord.User{ID: 1, Username: "alice"}) // seeds the index

	// A thread created by someone else is picked up from its event. Like
	// disgo's, the event carries the parent only on the thread.
	b.onModLogThreadCreate(&events.ThreadCreate{GenericThread: &events.GenericThread{
		ThreadID: 302, GuildID: testModLogGuild, Thread: testThread(t, 302, "carol - 3"),
	}})
	if got := find(discord.User{ID: 3, Username: "carol"}); got != 302 {
		t.Errorf("carol's thread = %d, want 302", got)
	}

	// A renamed thread moves to its new user.
	b.onModLogThreadUpdate(&events.ThreadUpdate{GenericThread: &events.GenericThread{
		ThreadID: 301, GuildID: testModLogGuild, ParentID: testModLogForum, Thread: testThread(t, 301, "bob - 4"),
	}})
	if got := find(discord.User{ID: 4, Username: "bob"}); got != 301 {
		t.Errorf("renamed thread = %d, want 301", got)
	}
	if api.count("active") != 1 || api.count("create") != 0 {
		t.Fatalf("events should avoid scans and creates: %d scans, %d creates", api.count("active"), api.count("create"))
	}

	// Threads in other channels are ignored.
	b.onModLogThreadDelete(&events.ThreadDelete{GenericThread: &events.GenericThread{ThreadID: 300, ParentID: 999}})
	if got := find(discord.User{ID: 1, Username: "alice"}); got != 300 || api.count("active") != 1 {
		t.Errorf("delete in another forum changed the index: alice -> %d", got)
	}

	// Deleting a user's thread rescans on next use, in case they have another.
	api.deleteThread(300)
	api.addThread(303, "alice - 1", time.Time{})
	b.onModLogThreadDelete(&events.ThreadDelete{GenericThread: &events.GenericThread{ThreadID: 300, ParentID: testModLogForum}})
	if got := find(discord.User{ID: 1, Username: "alice"}); got != 303 {
		t.Errorf("after delete, alice -> %d, want her other thread 303", got)
	}
	if api.count("active") != 2 || api.count("create") != 0 {
		t.Errorf("%d scans, %d creates; want 2 and 0", api.count("active"), api.count("create"))
	}
}

func TestPostModLogEntryDeletesFilesOnlyWhenRejected(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		postStatus, listStatus int
		wantDeleted            bool
	}{
		{"rejected", http.StatusBadRequest, 0, true},               // e.g. an embed over Discord's limits
		{"server error", http.StatusInternalServerError, 0, false}, // the entry may have been posted anyway
		{"thread not found", 0, http.StatusBadGateway, true},       // nothing was sent
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeModLogAPI()
			b, fakeS3 := newModLogTestBot(t, api)
			cacheForum(t, b)
			api.addThread(300, "alice - 1", time.Time{})
			api.postStatus[300] = tc.postStatus
			api.failActive = tc.listStatus
			keys := []string{"mod_files/1/a.png", "mod_files/1/b.mov"}
			for _, k := range keys {
				fakeS3.Objects["/test-bucket/"+k] = []byte("data")
			}

			_, _, err := b.postModLogEntry(testModLogForum, discord.User{ID: 1, Username: "alice"}, discord.Embed{Title: "x"}, keys)
			if err == nil {
				t.Fatal("expected the post to fail")
			}
			fakeS3.Mu.Lock()
			defer fakeS3.Mu.Unlock()
			if got := len(fakeS3.Deleted) == len(keys); got != tc.wantDeleted {
				t.Errorf("deleted %v, want deleted=%v", fakeS3.Deleted, tc.wantDeleted)
			}
		})
	}

	t.Run("success keeps files", func(t *testing.T) {
		api := newFakeModLogAPI()
		b, fakeS3 := newModLogTestBot(t, api)
		cacheForum(t, b)
		if _, _, err := b.postModLogEntry(testModLogForum, discord.User{ID: 1, Username: "alice"}, discord.Embed{Title: "x"}, []string{"mod_files/1/a.png"}); err != nil {
			t.Fatal(err)
		}
		if len(fakeS3.Deleted) != 0 {
			t.Errorf("deleted %v after a successful post", fakeS3.Deleted)
		}
	})
}

// slashInteraction builds a /config modlog set event for channel 700 of the
// given type, recording the response text.
func slashInteraction(t *testing.T, channelType discord.ChannelType, response *string) *events.ApplicationCommandInteractionCreate {
	t.Helper()
	data := fmt.Sprintf(`{
		"id":"1","application_id":"2","type":2,"token":"tok","version":1,"guild_id":"%d",
		"user":{"id":"4","username":"mod"},
		"data":{"id":"5","name":"config","type":1,
			"options":[{"name":"modlog","type":2,"options":[{"name":"set","type":1,"options":[{"name":"channel","type":7,"value":"700"}]}]}],
			"resolved":{"channels":{"700":{"id":"700","name":"chan","type":%d,"permissions":"0"}}}}
	}`, testModLogGuild, channelType)
	var interaction discord.ApplicationCommandInteraction
	if err := json.Unmarshal([]byte(data), &interaction); err != nil {
		t.Fatal(err)
	}
	return &events.ApplicationCommandInteractionCreate{
		ApplicationCommandInteraction: interaction,
		Respond: func(_ discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			*response = data.(discord.MessageCreate).Content
			return nil
		},
	}
}

func TestModLogConfigSetRequiresForum(t *testing.T) {
	api := newFakeModLogAPI()
	b, _ := newModLogTestBot(t, api)
	b.modLog = newGuildStateStore(b.S3, b.Log, "modlog",
		func() *modLogState { return &modLogState{} },
		func(st *modLogState) *sync.Mutex { return &st.mu })
	st := &modLogState{ChannelID: 600}
	b.modLog.set(testModLogGuild, st)

	var response string
	b.handleModLogConfig(slashInteraction(t, discord.ChannelTypeGuildText, &response))
	if st.ChannelID != 600 || !strings.Contains(response, "isn't a forum") {
		t.Errorf("text channel: channel = %d, response %q; want it rejected", st.ChannelID, response)
	}

	b.handleModLogConfig(slashInteraction(t, discord.ChannelTypeGuildForum, &response))
	if st.ChannelID != 700 || !strings.Contains(response, "set to <#700>") {
		t.Errorf("forum: channel = %d, response %q; want it set", st.ChannelID, response)
	}
}
