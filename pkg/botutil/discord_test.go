package botutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"

	"github.com/sadbox/sprobot/pkg/testutil"
)

// fakeCreateMessageAPI answers Create Message with the queued status codes in
// order, then 200, recording the body of every request.
type fakeCreateMessageAPI struct {
	mu       sync.Mutex
	statuses []int
	requests []discord.MessageCreate
}

func (f *fakeCreateMessageAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/messages") {
		http.NotFound(w, r)
		return
	}
	var body discord.MessageCreate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.requests = append(f.requests, body)

	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	if len(f.statuses) > 0 {
		status, f.statuses = f.statuses[0], f.statuses[1:]
	}
	w.WriteHeader(status)
	if status != http.StatusOK {
		_, _ = w.Write([]byte(`{"message":"failed","code":0}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "999", "channel_id": "500", "content": body.Content, "nonce": body.Nonce,
		"author":    map[string]any{"id": "1", "username": "bot", "bot": true},
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

func (f *fakeCreateMessageAPI) sent() []discord.MessageCreate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]discord.MessageCreate(nil), f.requests...)
}

func newFakeRest(t *testing.T, api http.Handler) rest.Rest {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	orig := postRetryDelay
	postRetryDelay = time.Millisecond
	t.Cleanup(func() { postRetryDelay = orig })
	return rest.New(rest.NewClient("test", rest.WithURL(srv.URL)))
}

// A 5xx may mean Discord created the message anyway, so every retry must
// carry the first attempt's nonce for Discord to dedupe on.
func TestPostWithRetrySendsSameNonceOnEveryAttempt(t *testing.T) {
	api := &fakeCreateMessageAPI{statuses: []int{http.StatusInternalServerError, http.StatusBadGateway}}
	r := newFakeRest(t, api)

	msg, err := PostWithRetry(r, 500, discord.MessageCreate{Content: "sticky"}, testutil.DiscardLogger())
	if err != nil {
		t.Fatalf("PostWithRetry: %v", err)
	}
	if msg.ID != 999 {
		t.Errorf("message ID = %d, want 999", msg.ID)
	}

	reqs := api.sent()
	if len(reqs) != 3 {
		t.Fatalf("attempts = %d, want 3", len(reqs))
	}
	nonce := reqs[0].Nonce
	if nonce == "" || len(nonce) > 25 {
		t.Fatalf("nonce %q: want 1-25 characters", nonce)
	}
	for i, req := range reqs {
		if req.Nonce != nonce {
			t.Errorf("attempt %d nonce = %q, want %q", i+1, req.Nonce, nonce)
		}
		if !req.EnforceNonce {
			t.Errorf("attempt %d did not set enforce_nonce", i+1)
		}
		if req.Content != "sticky" {
			t.Errorf("attempt %d content = %q", i+1, req.Content)
		}
	}

	// A new post is a new message, so it gets its own nonce.
	if _, err := PostWithRetry(r, 500, discord.MessageCreate{Content: "sticky"}, testutil.DiscardLogger()); err != nil {
		t.Fatalf("second PostWithRetry: %v", err)
	}
	if reqs := api.sent(); reqs[len(reqs)-1].Nonce == nonce {
		t.Error("second post reused the first post's nonce")
	}
}

func TestPostWithRetryDoesNotRetryClientErrors(t *testing.T) {
	api := &fakeCreateMessageAPI{statuses: []int{http.StatusBadRequest}}
	r := newFakeRest(t, api)

	if _, err := PostWithRetry(r, 500, discord.MessageCreate{Content: "x"}, testutil.DiscardLogger()); err == nil {
		t.Fatal("PostWithRetry after a 400 = nil error, want the error")
	}
	if n := len(api.sent()); n != 1 {
		t.Errorf("attempts after a 400 = %d, want 1", n)
	}
}

func TestFollowupEphemeral(t *testing.T) {
	var mu sync.Mutex
	var route string
	var body discord.MessageCreate
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		route = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "1", "channel_id": "2", "content": body.Content,
			"author":    map[string]any{"id": "3", "username": "bot", "bot": true},
			"timestamp": time.Now().Format(time.RFC3339),
		})
	}))
	defer srv.Close()
	client := &bot.Client{ApplicationID: 42, Rest: rest.New(rest.NewClient("test", rest.WithURL(srv.URL)))}

	FollowupEphemeral(client, "tok", "Saved.")

	mu.Lock()
	defer mu.Unlock()
	if route != "POST /webhooks/42/tok" {
		t.Errorf("request = %q, want the follow-up webhook for the interaction token", route)
	}
	if body.Content != "Saved." || body.Flags != discord.MessageFlagEphemeral {
		t.Errorf("follow-up = %+v, want ephemeral %q", body, "Saved.")
	}
}

func TestPostWithRetryGivesUpAfterThreeAttempts(t *testing.T) {
	api := &fakeCreateMessageAPI{statuses: []int{500, 500, 500, 500}}
	r := newFakeRest(t, api)

	if _, err := PostWithRetry(r, 500, discord.MessageCreate{Content: "x"}, testutil.DiscardLogger()); err == nil {
		t.Fatal("PostWithRetry after three 500s = nil error, want the error")
	}
	if n := len(api.sent()); n != 3 {
		t.Errorf("attempts = %d, want 3", n)
	}
}
