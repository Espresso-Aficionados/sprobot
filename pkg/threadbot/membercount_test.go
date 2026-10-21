package threadbot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

// fakeThreadAPI serves the Discord endpoints the thread lists use: active
// guild threads, List Thread Members, and interaction follow-ups.
type fakeThreadAPI struct {
	mu         sync.Mutex
	threads    []map[string]any
	members    map[string]int          // thread ID -> exact member count; -1 fails with 403
	fetches    map[string]int          // thread ID -> List Thread Members first-page calls
	failActive bool                    // Get Active Guild Threads answers 500
	followups  []discord.MessageCreate // interaction follow-ups sent

	pages         map[string]int // thread ID -> List Thread Members calls
	withoutMember int            // List Thread Members calls without with_member=true

	channelErrs map[string]int // channel ID -> JSON error code Get Channel fails with; others exist
}

func newFakeThreadAPI() *fakeThreadAPI {
	return &fakeThreadAPI{members: map[string]int{}, fetches: map[string]int{}, pages: map[string]int{}, channelErrs: map[string]int{}}
}

func (f *fakeThreadAPI) sentFollowups() []discord.MessageCreate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]discord.MessageCreate(nil), f.followups...)
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

// setField overrides a field of an added thread, as Discord would return it.
func (f *fakeThreadAPI) setField(id, key string, value any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.threads {
		if t["id"] == id {
			t[key] = value
		}
	}
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
		if f.failActive {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"Internal Server Error","code":0}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"threads": f.threads, "members": []any{}})
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "channels":
		if code, ok := f.channelErrs[parts[1]]; ok {
			status := http.StatusForbidden // 50001 Missing Access
			if code == int(rest.JSONErrorCodeUnknownChannel) {
				status = http.StatusNotFound
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "error", "code": code})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": parts[1], "type": 0, "guild_id": "1013566342345019512", "name": "coffee"})
	case r.Method == http.MethodPost && parts[0] == "webhooks":
		var m discord.MessageCreate
		_ = json.NewDecoder(r.Body).Decode(&m)
		f.followups = append(f.followups, m)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "888", "channel_id": "500", "content": m.Content,
			"author":    map[string]any{"id": "1", "username": "threadbot", "bot": true},
			"timestamp": time.Now().Format(time.RFC3339),
		})
	case len(parts) >= 3 && parts[len(parts)-1] == "thread-members":
		// Paginated, as API v11 always is: members ordered by user ID, those
		// after the "after" user ID, at most "limit" (default 100) of them.
		id := parts[len(parts)-2]
		q := r.URL.Query()
		after, _ := strconv.Atoi(q.Get("after"))
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil {
			limit = 100
		}
		if after == 0 {
			f.fetches[id]++
		}
		f.pages[id]++
		if q.Get("with_member") != "true" {
			f.withoutMember++
		}
		n := f.members[id]
		if n < 0 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Missing Access","code":50001}`))
			return
		}
		list := []map[string]any{}
		for i := range n {
			userID := 1000 + i
			if userID <= after {
				continue
			}
			if len(list) == limit {
				break
			}
			list = append(list, map[string]any{"id": id, "user_id": fmt.Sprint(userID), "join_timestamp": time.Now().Format(time.RFC3339), "flags": 0})
		}
		_ = json.NewEncoder(w).Encode(list)
	default:
		http.NotFound(w, r)
	}
}

// TestThreadEmbedFetchesNewAndUncachedThreads drives buildThreadsEmbed
// against a fake API: threads at the member_count cap with no usable cached
// count are fetched on first display, without waiting for any refresh window.
func TestThreadEmbedFetchesNewAndUncachedThreads(t *testing.T) {
	api := newFakeThreadAPI()
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
		e, err := b.buildThreadsEmbed(guildID, 0)
		if e == nil || err != nil {
			t.Fatalf("embed = %v, %v", e, err)
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

// List Thread Members returns at most 100 members a call (always, from API
// v11), so counts past 100 come from paging through every member.
func TestCountThreadMembersPagesPast100(t *testing.T) {
	api := newFakeThreadAPI()
	b := newThreadListBot(t, api)
	tests := []struct {
		id              string
		members         int
		want, wantCalls int
	}{
		{"201", 250, 250, 3},
		{"202", 200, 200, 3}, // a full last page takes one more call to confirm the end
		{"203", 60, 60, 1},
		{"204", -1, 0, 1}, // a failed fetch counts 0, never a partial count
	}
	for _, tt := range tests {
		api.addThread(tt.id, memberCountCap, tt.members)
		id, _ := snowflake.Parse(tt.id)
		if got := b.countThreadMembers(id); got != tt.want {
			t.Errorf("thread %s with %d members: counted %d, want %d", tt.id, tt.members, got, tt.want)
		}
		api.mu.Lock()
		if api.pages[tt.id] != tt.wantCalls {
			t.Errorf("thread %s: %d calls, want %d", tt.id, api.pages[tt.id], tt.wantCalls)
		}
		api.mu.Unlock()
	}
	// Until v11, Discord pages only with with_member=true.
	if api.withoutMember != 0 {
		t.Errorf("%d List Thread Members calls without with_member=true", api.withoutMember)
	}
}
