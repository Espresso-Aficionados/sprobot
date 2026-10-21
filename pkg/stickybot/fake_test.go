package stickybot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
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

const testGuildID = snowflake.ID(1013566342345019512)

// fakeDiscord serves the message endpoints stickybot uses and records the
// messages it is asked to create and delete. Created messages get ID 777.
type fakeDiscord struct {
	mu        sync.Mutex
	posts     []discord.MessageCreate
	deleted   []snowflake.ID
	failPosts bool // answer Create Message with a 400

	// If hold is set, Create Message signals created and then waits for hold
	// to close, so a test can keep a post in flight.
	created chan struct{}
	hold    chan struct{}
}

func (f *fakeDiscord) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/messages") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		_, _ = w.Write([]byte(`[]`))
	case http.MethodDelete:
		id, _ := snowflake.Parse(path.Base(r.URL.Path))
		f.mu.Lock()
		f.deleted = append(f.deleted, id)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		var msg discord.MessageCreate
		_ = json.NewDecoder(r.Body).Decode(&msg)
		f.mu.Lock()
		f.posts = append(f.posts, msg)
		fail := f.failPosts
		f.mu.Unlock()
		if f.hold != nil {
			f.created <- struct{}{}
			<-f.hold
		}
		if fail {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code": 50035, "message": "Invalid Form Body"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "777", "channel_id": "500", "content": msg.Content,
			"author":    map[string]any{"id": "1", "username": "stickybot", "bot": true},
			"timestamp": time.Now().Format(time.RFC3339),
		})
	}
}

func (f *fakeDiscord) postsMade() []discord.MessageCreate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.posts)
}

func (f *fakeDiscord) deletedIDs() []snowflake.ID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deleted)
}

// newFakeBot returns a test bot, with the test guild loaded, whose REST calls
// go to f and whose S3 calls go to the returned fake. Its repost loops are
// stopped when the test ends.
func newFakeBot(t *testing.T, f *fakeDiscord) (*Bot, *testutil.FakeS3) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	fakeS3 := testutil.NewFakeS3()
	s3srv := httptest.NewServer(fakeS3)
	t.Cleanup(s3srv.Close)

	b := newTestBot(t, testutil.NewTestS3Client(t, s3srv))
	b.Client = &bot.Client{Rest: rest.New(rest.NewClient("test", rest.WithURL(srv.URL)))}
	b.stickies[testGuildID] = make(map[snowflake.ID]*stickyMessage) // loaded, none stored
	// Cleanups run last-in first-out: the loops stop before the servers close.
	t.Cleanup(b.stopAllStickyGoroutines)
	return b, fakeS3
}

// testFileKey is a valid sticky file key in the test guild, named by letter
// repeated.
func testFileKey(letter string) string {
	return "sticky_files/1013566342345019512/" + strings.Repeat(letter, 30) + ".png"
}
