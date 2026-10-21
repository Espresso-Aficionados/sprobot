package threadbot

import (
	"encoding/json"
	"fmt"
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

// fakeThreadAPI serves the two Discord endpoints the thread embeds use:
// active guild threads, and List Thread Members.
type fakeThreadAPI struct {
	mu      sync.Mutex
	threads []map[string]any
	members map[string]int // thread ID -> exact member count; -1 fails with 403
	fetches map[string]int // thread ID -> List Thread Members calls
}

func (f *fakeThreadAPI) addThread(id string, memberCount, exact int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threads = append(f.threads, map[string]any{
		"id": id, "type": 11, "guild_id": "1013566342345019512", "parent_id": "500",
		"name": "thread-" + id, "message_count": 10, "member_count": memberCount,
		"thread_metadata": map[string]any{"archived": false},
	})
	f.members[id] = exact
}

func (f *fakeThreadAPI) fetchCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches[id]
}

func (f *fakeThreadAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case strings.HasSuffix(r.URL.Path, "/threads/active"):
		_ = json.NewEncoder(w).Encode(map[string]any{"threads": f.threads, "members": []any{}})
	case len(parts) >= 3 && parts[len(parts)-1] == "thread-members":
		id := parts[len(parts)-2]
		f.fetches[id]++
		n := f.members[id]
		if n < 0 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Missing Access","code":50001}`))
			return
		}
		list := make([]map[string]any, n)
		for i := range list {
			list[i] = map[string]any{"id": id, "user_id": fmt.Sprint(1000 + i), "join_timestamp": time.Now().Format(time.RFC3339), "flags": 0}
		}
		_ = json.NewEncoder(w).Encode(list)
	default:
		http.NotFound(w, r)
	}
}

// TestThreadEmbedFetchesNewAndUncachedThreads drives buildAllThreadsEmbed
// against a fake API: threads at the member_count cap with no usable cached
// count are fetched on first display, without waiting for any refresh window.
func TestThreadEmbedFetchesNewAndUncachedThreads(t *testing.T) {
	api := &fakeThreadAPI{members: map[string]int{}, fetches: map[string]int{}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	s3srv := httptest.NewServer(testutil.NewFakeS3())
	defer s3srv.Close()

	b := newTestBot(t, testutil.NewTestS3Client(t, s3srv))
	b.Client = &bot.Client{Rest: rest.New(rest.NewClient("test", rest.WithURL(srv.URL)))}
	guildID := snowflake.ID(1013566342345019512)

	api.addThread("101", memberCountCap, 120) // at the cap: needs an exact count
	api.addThread("102", 12, 12)              // below the cap: member_count is exact
	api.addThread("103", memberCountCap, -1)  // at the cap, but the fetch fails

	desc := func() string {
		t.Helper()
		e := b.buildAllThreadsEmbed(guildID)
		if e == nil {
			t.Fatal("embed is nil")
		}
		return e.Description
	}
	wantFetches := func(id string, want int) {
		t.Helper()
		if got := api.fetchCount(id); got != want {
			t.Errorf("thread %s: %d fetches, want %d", id, got, want)
		}
	}

	// First display: the uncached thread at the cap is fetched immediately.
	d := desc()
	if !strings.Contains(d, "120 members") || !strings.Contains(d, "12 members") || !strings.Contains(d, "50 members") {
		t.Errorf("first display shows wrong counts:\n%s", d)
	}
	wantFetches("101", 1)
	wantFetches("102", 0)
	wantFetches("103", 1)

	// Second display: the fresh count is reused; the failed one is retried.
	desc()
	wantFetches("101", 1)
	wantFetches("103", 2)

	// A thread that appears (or reaches the cap) later is fetched on its first
	// display, while the others stay cached.
	api.addThread("104", memberCountCap, 75)
	if d := desc(); !strings.Contains(d, "75 members") {
		t.Errorf("new thread's exact count not shown:\n%s", d)
	}
	wantFetches("104", 1)
	wantFetches("101", 1)

	// A count from before the current session expires after memberCountTTL...
	b.mu.Lock()
	b.memberCounts[guildID].Fetched[101] = time.Now().Add(-memberCountTTL - time.Minute)
	b.mu.Unlock()
	desc()
	wantFetches("101", 2)

	// ...but one fetched during an unbroken session lasts memberCountLiveTTL.
	b.mu.Lock()
	b.sessionStart = time.Now().Add(-10 * 24 * time.Hour)
	b.memberCounts[guildID].Fetched[101] = time.Now().Add(-3 * 24 * time.Hour)
	b.mu.Unlock()
	desc()
	wantFetches("101", 2)

	// A count dropped by pruning is fetched again on its next display.
	b.mu.Lock()
	delete(b.memberCounts[guildID].Counts, 104)
	delete(b.memberCounts[guildID].Fetched, 104)
	b.mu.Unlock()
	desc()
	wantFetches("104", 2)
}
