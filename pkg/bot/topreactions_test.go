package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
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
	sbGuild    snowflake.ID = 1013566342345019512
	sbSource   snowflake.ID = 500 // #coffee, in category 700
	sbOutput   snowflake.ID = 600 // #starboard
	sbCategory snowflake.ID = 700
)

// fakeStarboardAPI serves the Discord endpoints the starboard calls. Messages
// are raw JSON objects keyed by "channelID/messageID".
type fakeStarboardAPI struct {
	mu          sync.Mutex
	messages    map[string]map[string]any
	channels    map[string]map[string]any // served by GET /channels/{id}
	channelGets int
	creates     []starboardCreateBody
	edits       []string // footer text of each edit
	deletes     []string // "channelID/messageID" of each successful delete
	nextID      int
	// When createStarted is set, the next POST signals it and then waits for
	// createGate before creating the message. Later POSTs don't wait.
	createStarted chan struct{}
	createGate    chan struct{}
	// getStarted and getGate do the same for the next message GET, which
	// returns the message as it was when the request arrived.
	getStarted chan struct{}
	getGate    chan struct{}
	// A non-zero createStatus or deleteStatus fails POSTs or DELETEs with it.
	createStatus int
	deleteStatus int
}

type starboardCreateBody struct {
	Nonce        string          `json:"nonce"`
	EnforceNonce bool            `json:"enforce_nonce"`
	Embeds       []discord.Embed `json:"embeds"`
}

func newFakeStarboardAPI() *fakeStarboardAPI {
	return &fakeStarboardAPI{
		messages: map[string]map[string]any{},
		channels: map[string]map[string]any{},
		nextID:   9000,
	}
}

// setSource stores a message in #coffee with count ⭐ reactions.
func (f *fakeStarboardAPI) setSource(msgID snowflake.ID, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages[fmt.Sprintf("%d/%d", sbSource, msgID)] = map[string]any{
		"id": msgID.String(), "channel_id": sbSource.String(), "content": "great shot",
		"author":    map[string]any{"id": "42", "username": "poster"},
		"reactions": []map[string]any{{"count": count, "emoji": map[string]any{"name": "⭐"}}},
	}
}

// addPost stores starboard post postID in #starboard with the given footer.
func (f *fakeStarboardAPI) addPost(postID snowflake.ID, footer string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages[fmt.Sprintf("%d/%d", sbOutput, postID)] = map[string]any{
		"id": postID.String(), "channel_id": sbOutput.String(),
		"embeds": []map[string]any{{"description": "great shot", "footer": map[string]any{"text": footer}}},
	}
}

// starboardCalls is a copy of the requests the fake API has served.
type starboardCalls struct {
	creates     []starboardCreateBody
	edits       []string
	deletes     []string
	channelGets int
}

func (f *fakeStarboardAPI) calls() starboardCalls {
	f.mu.Lock()
	defer f.mu.Unlock()
	return starboardCalls{slices.Clone(f.creates), slices.Clone(f.edits), slices.Clone(f.deletes), f.channelGets}
}

func (f *fakeStarboardAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "channels" {
		http.NotFound(w, r)
		return
	}
	channelID := parts[1]

	if r.Method == http.MethodPost && len(parts) == 3 {
		var body starboardCreateBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		started, gate := f.createStarted, f.createGate
		f.createStarted, f.createGate = nil, nil // only the first POST waits
		f.mu.Unlock()
		if started != nil {
			started <- struct{}{}
			<-gate
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.createStatus != 0 {
			writeJSONError(w, f.createStatus, 0)
			return
		}
		f.creates = append(f.creates, body)
		f.nextID++
		msg := map[string]any{"id": fmt.Sprint(f.nextID), "channel_id": channelID, "embeds": body.Embeds}
		f.messages[channelID+"/"+fmt.Sprint(f.nextID)] = msg
		writeJSON(w, msg)
		return
	}

	if r.Method == http.MethodGet && len(parts) == 4 && parts[2] == "messages" {
		f.mu.Lock()
		msg, ok := f.messages[channelID+"/"+parts[3]]
		data, _ := json.Marshal(msg)
		started, gate := f.getStarted, f.getGate
		f.getStarted, f.getGate = nil, nil
		f.mu.Unlock()
		if started != nil {
			started <- struct{}{}
			<-gate
		}
		if !ok {
			writeJSONError(w, http.StatusNotFound, rest.JSONErrorCodeUnknownMessage)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && len(parts) == 2:
		f.channelGets++
		ch, ok := f.channels[channelID]
		if !ok {
			writeJSONError(w, http.StatusNotFound, rest.JSONErrorCodeUnknownChannel)
			return
		}
		writeJSON(w, ch)
	case len(parts) == 4 && parts[2] == "messages":
		key := channelID + "/" + parts[3]
		msg, ok := f.messages[key]
		if !ok {
			writeJSONError(w, http.StatusNotFound, rest.JSONErrorCodeUnknownMessage)
			return
		}
		switch r.Method {
		case http.MethodPatch:
			var body struct {
				Embeds []discord.Embed `json:"embeds"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			msg["embeds"] = body.Embeds
			if len(body.Embeds) > 0 && body.Embeds[0].Footer != nil {
				f.edits = append(f.edits, body.Embeds[0].Footer.Text)
			}
			writeJSON(w, msg)
		case http.MethodDelete:
			if f.deleteStatus != 0 {
				writeJSONError(w, f.deleteStatus, 0)
				return
			}
			delete(f.messages, key)
			f.deletes = append(f.deletes, key)
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, code rest.JSONErrorCode) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": "error"})
}

// testChannel decodes a channel the way the gateway and REST deliver it.
func testChannel(t *testing.T, raw string) discord.GuildChannel {
	t.Helper()
	var u discord.UnmarshalChannel
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("decoding channel: %v", err)
	}
	return u.Channel.(discord.GuildChannel)
}

func textChannelJSON(id, parent snowflake.ID, name string) string {
	if parent == 0 {
		return fmt.Sprintf(`{"id":"%d","type":0,"guild_id":"%d","name":%q}`, id, sbGuild, name)
	}
	return fmt.Sprintf(`{"id":"%d","type":0,"guild_id":"%d","name":%q,"parent_id":"%d"}`, id, sbGuild, name, parent)
}

// newStarboardTest returns a bot whose starboard posts ⭐ messages with at
// least 2 reactions from #coffee to #starboard, talking to a fake API. Both
// channels are public and cached; the fake S3 holds what is persisted.
func newStarboardTest(t *testing.T) (*Bot, *fakeStarboardAPI, *starboardState, *testutil.FakeS3) {
	t.Helper()
	saved := starboardUpdateDelay
	starboardUpdateDelay = 0
	t.Cleanup(func() { starboardUpdateDelay = saved })

	api := newFakeStarboardAPI()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	fakeS3 := testutil.NewFakeS3()
	s3srv := httptest.NewServer(fakeS3)
	t.Cleanup(s3srv.Close)
	s3 := testutil.NewTestS3Client(t, s3srv)

	caches := cache.New(cache.WithCaches(cache.FlagChannels))
	caches.AddChannel(testChannel(t, textChannelJSON(sbSource, sbCategory, "coffee")))
	caches.AddChannel(testChannel(t, textChannelJSON(sbOutput, 0, "starboard")))

	b := &Bot{
		BaseBot: &botutil.BaseBot{S3: s3, Log: testutil.DiscardLogger()},
		starboard: newGuildStateStore(s3, testutil.DiscardLogger(), "starboard",
			func() *starboardState { return &starboardState{} },
			func(st *starboardState) *sync.Mutex { return &st.mu }),
	}
	b.Client = &disgobot.Client{Rest: rest.New(rest.NewClient("test", rest.WithURL(srv.URL))), Caches: caches}
	st := &starboardState{
		Settings: starboardSettings{OutputChannelID: sbOutput, Emoji: "⭐", Threshold: 2},
		Entries:  map[snowflake.ID]starboardEntry{},
	}
	b.starboard.set(sbGuild, st)
	return b, api, st, fakeS3
}

func reactionAdd(msgID snowflake.ID, emoji string) *events.GuildMessageReactionAdd {
	return &events.GuildMessageReactionAdd{
		GenericGuildMessageReaction: &events.GenericGuildMessageReaction{
			GuildID: sbGuild, ChannelID: sbSource, MessageID: msgID, UserID: 42,
			Emoji: discord.PartialEmoji{Name: &emoji},
		},
		Member: discord.Member{User: discord.User{ID: 42}},
	}
}

func entryOf(st *starboardState, msgID snowflake.ID) (starboardEntry, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e, ok := st.Entries[msgID]
	return e, ok
}

func workCount(st *starboardState) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.work)
}

func messageDelete(msgID snowflake.ID) *events.GuildMessageDelete {
	return &events.GuildMessageDelete{GenericGuildMessage: &events.GenericGuildMessage{
		GuildID: sbGuild, ChannelID: sbSource, MessageID: msgID,
	}}
}

// persistedHolds reports whether the starboard saved to S3 mentions msgID.
func persistedHolds(t *testing.T, fakeS3 *testutil.FakeS3, msgID snowflake.ID) bool {
	t.Helper()
	fakeS3.Mu.Lock()
	defer fakeS3.Mu.Unlock()
	if len(fakeS3.Objects) == 0 {
		t.Fatal("starboard never persisted")
	}
	for _, data := range fakeS3.Objects {
		if strings.Contains(string(data), msgID.String()) {
			return true
		}
	}
	return false
}

// recentID returns a message ID sent ago before now.
func recentID(ago time.Duration) snowflake.ID {
	return snowflake.New(time.Now().Add(-ago))
}

// A reaction that arrives while the post is being created must not post the
// message again; it gets one edit with the real count once the post exists.
func TestStarboardReactionDuringPostDoesNotPostTwice(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)
	started, gate := make(chan struct{}), make(chan struct{})
	api.mu.Lock()
	api.createStarted, api.createGate = started, gate
	api.mu.Unlock()
	msgID := recentID(time.Hour)
	api.setSource(msgID, 3)
	st.Entries[msgID] = starboardEntry{ChannelID: sbSource, Count: 1}

	done := make(chan struct{})
	go func() {
		b.onReactionAdd(reactionAdd(msgID, "⭐")) // reaches the threshold and posts
		close(done)
	}()
	<-started

	// The second reaction arrives mid-post. It should return at once; a
	// second post would queue behind the first in disgo's rate limiter.
	second := make(chan struct{})
	go func() {
		b.onReactionAdd(reactionAdd(msgID, "⭐"))
		close(second)
	}()
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Error("the second reaction waited on the first post")
	}

	close(gate)
	<-done
	<-second

	calls := api.calls()
	if len(calls.creates) != 1 {
		t.Fatalf("%d posts created, want 1", len(calls.creates))
	}
	if got := calls.creates[0]; got.Nonce != starboardNonce(msgID) || !got.EnforceNonce {
		t.Errorf("post nonce = %q enforce=%v, want %q enforced", got.Nonce, got.EnforceNonce, starboardNonce(msgID))
	}
	if !slices.Equal(calls.edits, []string{"⭐ 3 | #coffee"}) {
		t.Errorf("edits after the post = %q, want one showing the real count", calls.edits)
	}
	entry, _ := entryOf(st, msgID)
	if entry.StarboardMsgID == 0 || entry.PostChannelID != sbOutput {
		t.Errorf("entry not marked posted: %+v", entry)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.work) != 0 {
		t.Errorf("work records left behind: %v", st.work)
	}
}

// The counter only triggers a post; the message's own reactions decide
// whether it happens and what count is shown.
func TestStarboardPostUsesActualReactionCount(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)

	// The counter reaches the threshold but the message has only 1 ⭐.
	inflated := recentID(time.Hour)
	api.setSource(inflated, 1)
	st.Entries[inflated] = starboardEntry{ChannelID: sbSource, Count: 1}
	b.onReactionAdd(reactionAdd(inflated, "⭐"))
	if len(api.calls().creates) != 0 {
		t.Errorf("posted a message below the threshold")
	}
	if e, _ := entryOf(st, inflated); e.Count != 1 {
		t.Errorf("counter = %d, want it corrected to the real count 1", e.Count)
	}
	if n := workCount(st); n != 0 {
		t.Fatalf("%d work records left after the attempt; the message could never post", n)
	}
	api.setSource(inflated, 2)
	b.onReactionAdd(reactionAdd(inflated, "⭐"))
	if n := len(api.calls().creates); n != 1 {
		t.Fatalf("%d posts once the message reached the threshold, want 1", n)
	}

	// The message has more reactions than the counter saw.
	msgID := recentID(2 * time.Hour)
	api.setSource(msgID, 7)
	st.Entries[msgID] = starboardEntry{ChannelID: sbSource, Count: 1}
	b.onReactionAdd(reactionAdd(msgID, "⭐"))
	creates := api.calls().creates
	if len(creates) != 2 {
		t.Fatalf("%d posts, want 2", len(creates))
	}
	if got := creates[1].Embeds[0].Footer.Text; got != "⭐ 7 | #coffee" {
		t.Errorf("footer = %q, want the real count 7", got)
	}
	if e, _ := entryOf(st, msgID); e.Count != 7 {
		t.Errorf("counter = %d, want 7", e.Count)
	}
}

// A post that fails releases the message, so a later reaction can post it.
func TestStarboardFailedPostReleasesMessage(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)
	api.mu.Lock()
	api.createStatus = http.StatusBadGateway
	api.mu.Unlock()
	msgID := recentID(time.Hour)
	api.setSource(msgID, 2)
	st.Entries[msgID] = starboardEntry{ChannelID: sbSource, Count: 1}

	b.onReactionAdd(reactionAdd(msgID, "⭐"))
	if n := workCount(st); n != 0 {
		t.Fatalf("%d work records left after a failed post; the message could never post", n)
	}

	api.mu.Lock()
	api.createStatus = 0
	api.mu.Unlock()
	api.setSource(msgID, 3)
	b.onReactionAdd(reactionAdd(msgID, "⭐"))
	if n := len(api.calls().creates); n != 1 {
		t.Errorf("%d posts after the failure cleared, want 1", n)
	}
}

// A reaction that arrives while a post attempt reads the count, which comes
// back below the threshold, gets the count read again instead of being lost.
func TestStarboardReactionDuringCountReadIsNotLost(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)
	started, gate := make(chan struct{}), make(chan struct{})
	api.mu.Lock()
	api.getStarted, api.getGate = started, gate
	api.mu.Unlock()
	msgID := recentID(time.Hour)
	api.setSource(msgID, 1)
	// The counter has drifted one above the message's 1 ⭐.
	st.Entries[msgID] = starboardEntry{ChannelID: sbSource, Count: 1}

	done := make(chan struct{})
	go func() {
		b.onReactionAdd(reactionAdd(msgID, "⭐")) // counter 2: tries to post, reads 1
		close(done)
	}()
	<-started
	api.setSource(msgID, 2)
	b.onReactionAdd(reactionAdd(msgID, "⭐")) // a real second ⭐, during the read
	close(gate)
	<-done

	creates := api.calls().creates
	if len(creates) != 1 {
		t.Fatalf("%d posts, want the message posted once it has 2 ⭐", len(creates))
	}
	if got := creates[0].Embeds[0].Footer.Text; got != "⭐ 2 | #coffee" {
		t.Errorf("footer = %q, want the real count 2", got)
	}
	if e, _ := entryOf(st, msgID); e.Count != 2 {
		t.Errorf("counter = %d, want 2", e.Count)
	}
	if n := workCount(st); n != 0 {
		t.Errorf("%d work records left behind", n)
	}
}

// Reactions arriving while an edit is pending fold into that edit, which
// shows the count read just before it.
func TestStarboardUpdatesCoalesce(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)
	starboardUpdateDelay = 300 * time.Millisecond
	msgID := recentID(time.Hour)
	api.setSource(msgID, 9)
	api.addPost(8000, "⭐ 3 | #coffee")
	st.Entries[msgID] = starboardEntry{ChannelID: sbSource, Count: 3, StarboardMsgID: 8000, PostChannelID: sbOutput, ChannelName: "coffee"}

	done := make(chan struct{})
	go func() {
		b.onReactionAdd(reactionAdd(msgID, "⭐")) // starts the edit and waits
		close(done)
	}()
	for {
		st.mu.Lock()
		started := st.work[msgID] != nil
		st.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for range 4 {
		b.onReactionAdd(reactionAdd(msgID, "⭐"))
	}
	<-done

	if edits := api.calls().edits; !slices.Equal(edits, []string{"⭐ 9 | #coffee"}) {
		t.Errorf("edits = %q, want one for the whole burst, with the count read before it", edits)
	}
}

// The emoji and output channel are checked before any channel lookup, so
// other reactions cost no REST calls.
func TestStarboardIgnoresOtherEmojiBeforeLookups(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)
	b.Client.Caches.RemoveChannel(sbSource) // a lookup would go to REST
	msgID := recentID(time.Hour)

	b.onReactionAdd(reactionAdd(msgID, "🔥"))
	if gets := api.calls().channelGets; gets != 0 {
		t.Errorf("a non-starboard reaction made %d channel lookups", gets)
	}

	st.Settings.OutputChannelID = 0
	b.onReactionAdd(reactionAdd(msgID, "⭐"))
	if gets := api.calls().channelGets; gets != 0 {
		t.Errorf("a reaction with the starboard disabled made %d channel lookups", gets)
	}

	st.Settings.OutputChannelID = sbOutput
	b.onReactionAdd(reactionAdd(msgID, "⭐"))
	if api.calls().channelGets == 0 {
		t.Error("a starboard reaction made no lookup; the checks above prove nothing")
	}
}

// Deleting a message deletes its starboard post and entry; deleting it again
// does nothing.
func TestStarboardSourceDeleteRemovesPost(t *testing.T) {
	b, api, st, fakeS3 := newStarboardTest(t)
	msgID := recentID(time.Hour)
	postKey := fmt.Sprintf("%d/8000", sbOutput)
	api.addPost(8000, "⭐ 5 | #coffee")
	st.Entries[msgID] = starboardEntry{ChannelID: sbSource, Count: 5, StarboardMsgID: 8000, PostChannelID: sbOutput}
	// The output channel has changed since the post; the post's own channel is used.
	st.Settings.OutputChannelID = 601

	b.onStarboardSourceDelete(messageDelete(msgID))
	b.onStarboardSourceDelete(messageDelete(msgID))

	if deletes := api.calls().deletes; !slices.Equal(deletes, []string{postKey}) {
		t.Errorf("deletes = %v, want only %s", deletes, postKey)
	}
	if _, ok := entryOf(st, msgID); ok {
		t.Error("entry kept after its message was deleted")
	}
	if persistedHolds(t, fakeS3, msgID) {
		t.Error("persisted starboard still holds the deleted message")
	}
}

// A message deleted while its post is being created gets that post deleted,
// even when a reaction sent before the delete is handled after it.
func TestStarboardSourceDeleteDuringPost(t *testing.T) {
	b, api, st, fakeS3 := newStarboardTest(t)
	started, gate := make(chan struct{}), make(chan struct{})
	api.mu.Lock()
	api.createStarted, api.createGate = started, gate
	api.mu.Unlock()
	msgID := recentID(time.Hour)
	api.setSource(msgID, 2)
	st.Entries[msgID] = starboardEntry{ChannelID: sbSource, Count: 1}

	done := make(chan struct{})
	go func() {
		b.onReactionAdd(reactionAdd(msgID, "⭐"))
		close(done)
	}()
	<-started
	b.onStarboardSourceDelete(messageDelete(msgID))
	b.onReactionAdd(reactionAdd(msgID, "⭐"))
	close(gate)
	<-done

	calls := api.calls()
	if len(calls.creates) != 1 || len(calls.deletes) != 1 {
		t.Errorf("creates=%d deletes=%v, want the new post deleted", len(calls.creates), calls.deletes)
	}
	if _, ok := entryOf(st, msgID); ok {
		t.Error("entry kept for a deleted message")
	}
	if persistedHolds(t, fakeS3, msgID) {
		t.Error("persisted starboard still holds the deleted message")
	}
}

// A post delete that fails keeps the entry, flagged, until a retry deletes
// the post; meanwhile reactions don't touch it.
func TestStarboardSourceDeleteRetriesFailedPostDelete(t *testing.T) {
	b, api, st, fakeS3 := newStarboardTest(t)
	msgID := recentID(time.Hour)
	api.setSource(msgID, 5)
	api.addPost(8000, "⭐ 5 | #coffee")
	st.Entries[msgID] = starboardEntry{ChannelID: sbSource, Count: 5, StarboardMsgID: 8000, PostChannelID: sbOutput}
	api.mu.Lock()
	api.deleteStatus = http.StatusBadGateway
	api.mu.Unlock()

	b.onStarboardSourceDelete(messageDelete(msgID))
	if e, ok := entryOf(st, msgID); !ok || !e.SourceDeleted {
		t.Fatalf("entry = %+v (exists %v), want it kept and flagged", e, ok)
	}
	if !persistedHolds(t, fakeS3, msgID) {
		t.Error("flagged entry not persisted; a restart would lose the post")
	}
	b.onReactionAdd(reactionAdd(msgID, "⭐"))
	if e, _ := entryOf(st, msgID); e.Count != 5 || len(api.calls().edits) != 0 {
		t.Errorf("a reaction changed the deleted message's entry: %+v, edits %q", e, api.calls().edits)
	}

	api.mu.Lock()
	api.deleteStatus = 0
	api.mu.Unlock()
	b.retryStarboardDeletes()
	if deletes := api.calls().deletes; !slices.Equal(deletes, []string{fmt.Sprintf("%d/8000", sbOutput)}) {
		t.Errorf("deletes = %v, want the post deleted on retry", deletes)
	}
	if _, ok := entryOf(st, msgID); ok {
		t.Error("entry kept after the retry deleted its post")
	}
	if persistedHolds(t, fakeS3, msgID) {
		t.Error("persisted starboard still holds the deleted message")
	}
}

// A post from before PostChannelID was stored, with the starboard disabled,
// has no known channel: its entry stays flagged rather than dropped.
func TestStarboardSourceDeleteKeepsPostOfUnknownChannel(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)
	msgID := recentID(time.Hour)
	st.Entries[msgID] = starboardEntry{ChannelID: sbSource, Count: 5, StarboardMsgID: 8000}
	st.Settings.OutputChannelID = 0

	b.onStarboardSourceDelete(messageDelete(msgID))
	if e, ok := entryOf(st, msgID); !ok || !e.SourceDeleted {
		t.Errorf("entry = %+v (exists %v), want it kept and flagged", e, ok)
	}
	if deletes := api.calls().deletes; len(deletes) != 0 {
		t.Errorf("deletes = %v, want none without a channel", deletes)
	}
}

func TestBackfillPostChannels(t *testing.T) {
	st := &starboardState{
		Settings: starboardSettings{OutputChannelID: sbOutput},
		Entries: map[snowflake.ID]starboardEntry{
			1: {StarboardMsgID: 8000},                     // posted before PostChannelID
			2: {StarboardMsgID: 8001, PostChannelID: 601}, // already recorded
			3: {Count: 2},                                 // not posted
		},
	}
	backfillPostChannels(st)
	for id, want := range map[snowflake.ID]snowflake.ID{1: sbOutput, 2: 601, 3: 0} {
		if got := st.Entries[id].PostChannelID; got != want {
			t.Errorf("entry %d PostChannelID = %d, want %d", id, got, want)
		}
	}

	disabled := &starboardState{Entries: map[snowflake.ID]starboardEntry{1: {StarboardMsgID: 8000}}}
	backfillPostChannels(disabled)
	if got := disabled.Entries[1].PostChannelID; got != 0 {
		t.Errorf("PostChannelID = %d with no output channel, want 0", got)
	}
}

// Posted entries outlive the reaction cutoff, so deleting an old message
// still deletes its post.
func TestStarboardOldPostDeletedWithSource(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)
	old := recentID(starboardMaxAge + 10*24*time.Hour)
	api.addPost(8000, "⭐ 5 | #coffee")
	st.Entries[old] = starboardEntry{ChannelID: sbSource, Count: 5, StarboardMsgID: 8000, PostChannelID: sbOutput}

	b.saveStarboard()
	b.onStarboardSourceDelete(messageDelete(old))
	if deletes := api.calls().deletes; !slices.Equal(deletes, []string{fmt.Sprintf("%d/8000", sbOutput)}) {
		t.Errorf("deletes = %v, want the old post deleted", deletes)
	}
}

// Deleting a channel or thread deletes the posts of its messages, including
// those in the channel's cached threads; Discord sends no message deletes.
func TestStarboardChannelDeleteRemovesPosts(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)
	threadJSON := `{"id":"%d","type":11,"guild_id":"%d","name":"t","parent_id":"%d","thread_metadata":{}}`
	b.Client.Caches.AddChannel(testChannel(t, fmt.Sprintf(threadJSON, 800, sbGuild, sbSource)))
	b.Client.Caches.AddChannel(testChannel(t, fmt.Sprintf(threadJSON, 801, sbGuild, 502)))
	inChannel, inThread, elsewhere, inOtherThread := recentID(time.Hour), recentID(2*time.Hour), recentID(3*time.Hour), recentID(4*time.Hour)
	for i, e := range []struct {
		msgID, channelID snowflake.ID
	}{{inChannel, sbSource}, {inThread, 800}, {elsewhere, 502}, {inOtherThread, 801}} {
		postID := snowflake.ID(8000 + i)
		api.addPost(postID, "⭐ 5 | #coffee")
		st.Entries[e.msgID] = starboardEntry{ChannelID: e.channelID, Count: 5, StarboardMsgID: postID, PostChannelID: sbOutput}
	}

	b.onStarboardChannelDelete(&events.GuildChannelDelete{GenericGuildChannel: &events.GenericGuildChannel{
		GuildID: sbGuild, ChannelID: sbSource,
	}})
	deletes := api.calls().deletes
	slices.Sort(deletes)
	if want := []string{fmt.Sprintf("%d/8000", sbOutput), fmt.Sprintf("%d/8001", sbOutput)}; !slices.Equal(deletes, want) {
		t.Errorf("deletes = %v, want %v", deletes, want)
	}
	for msgID, want := range map[snowflake.ID]bool{inChannel: false, inThread: false, elsewhere: true, inOtherThread: true} {
		if _, ok := entryOf(st, msgID); ok != want {
			t.Errorf("entry %d kept = %v, want %v", msgID, ok, want)
		}
	}

	b.onStarboardThreadDelete(&events.ThreadDelete{GenericThread: &events.GenericThread{
		GuildID: sbGuild, ThreadID: 801, ParentID: 502,
	}})
	if _, ok := entryOf(st, inOtherThread); ok {
		t.Error("entry kept after its thread was deleted")
	}
	if _, ok := entryOf(st, elsewhere); !ok {
		t.Error("deleting a thread removed an entry from its parent channel")
	}
}

// When a channel can't be looked up, reactions are skipped: nothing is stored
// or posted.
func TestStarboardFailsClosedWhenChannelUnknown(t *testing.T) {
	tests := []struct {
		name   string
		remove func(b *Bot)
	}{
		{"source channel not cached or fetchable", func(b *Bot) { b.Client.Caches.RemoveChannel(sbSource) }},
		{"output channel not cached or fetchable", func(b *Bot) { b.Client.Caches.RemoveChannel(sbOutput) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, api, st, _ := newStarboardTest(t)
			tt.remove(b)
			msgID := recentID(time.Hour)
			api.setSource(msgID, 5)

			for range 3 {
				b.onReactionAdd(reactionAdd(msgID, "⭐"))
			}
			if _, ok := entryOf(st, msgID); ok {
				t.Error("stored a reaction whose channel is unknown")
			}
			if n := len(api.calls().creates); n != 0 {
				t.Errorf("%d posts, want none", n)
			}
		})
	}
}

// A failed channel lookup must not leave "unknown" stored as the name; a
// later edit fills it in.
func TestStarboardChannelNameNotStoredWhenUnknown(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)
	b.Client.Caches.RemoveChannel(sbSource)
	msgID := recentID(time.Hour)
	api.setSource(msgID, 2)
	st.Entries[msgID] = starboardEntry{ChannelID: sbSource, Count: 2}
	st.startWork(msgID).posting = true

	b.postStarboardEntry(sbGuild, msgID, st)
	if got := api.calls().creates[0].Embeds[0].Footer.Text; got != "⭐ 2 | #unknown" {
		t.Errorf("footer = %q, want #unknown shown", got)
	}
	entry, _ := entryOf(st, msgID)
	if entry.ChannelName != "" {
		t.Fatalf("stored channel name %q for a failed lookup", entry.ChannelName)
	}

	// Older posts stored "unknown"; it is replaced once the channel resolves.
	entry.ChannelName = "unknown"
	st.Entries[msgID] = entry
	b.Client.Caches.AddChannel(testChannel(t, textChannelJSON(sbSource, sbCategory, "coffee")))
	b.updateStarboardPost(sbGuild, msgID, st)
	if entry, _ := entryOf(st, msgID); entry.ChannelName != "coffee" {
		t.Errorf("channel name = %q, want coffee", entry.ChannelName)
	}
	if edits := api.calls().edits; !slices.Equal(edits, []string{"⭐ 2 | #coffee"}) {
		t.Errorf("edits = %q, want the resolved name", edits)
	}
}

// Parents come from the channel cache, follow category moves, and a failed
// lookup is retried rather than remembered.
func TestResolveChannelParents(t *testing.T) {
	b, api, _, _ := newStarboardTest(t)
	thread := testChannel(t, fmt.Sprintf(`{"id":"800","type":11,"guild_id":"%d","name":"t","parent_id":"%d","thread_metadata":{}}`, sbGuild, sbSource))
	b.Client.Caches.AddChannel(thread)

	check := func(channelID snowflake.ID, want ...snowflake.ID) {
		t.Helper()
		got := b.resolveChannelParents(channelID, nil)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("resolveChannelParents(%d) = %v, want %v", channelID, got, want)
		}
	}
	check(sbSource, sbSource, sbCategory)
	check(800, 800, sbSource, sbCategory)

	// Moved to another category: CHANNEL_UPDATE replaces the cached channel.
	b.Client.Caches.AddChannel(testChannel(t, textChannelJSON(sbSource, 701, "coffee")))
	check(sbSource, sbSource, 701)
	check(800, 800, sbSource, 701)

	// Not cached and REST fails, then succeeds: the failure isn't kept.
	check(501, 501)
	api.mu.Lock()
	api.channels["501"] = map[string]any{"id": "501", "type": 0, "guild_id": sbGuild.String(), "name": "new", "parent_id": "702"}
	api.mu.Unlock()
	check(501, 501, 702)
}

func TestStarboardEligible(t *testing.T) {
	view := discord.PermissionViewChannel
	public := testChannel(t, fmt.Sprintf(`{"id":"1","type":0,"guild_id":"%d","name":"c"}`, sbGuild))
	ageRestricted := testChannel(t, fmt.Sprintf(`{"id":"1","type":0,"guild_id":"%d","name":"c","nsfw":true}`, sbGuild))
	ageRestrictedForum := testChannel(t, fmt.Sprintf(`{"id":"1","type":15,"guild_id":"%d","name":"f","nsfw":true}`, sbGuild))
	// @everyone can't view it and only role 20 can; permissions aren't compared.
	staffOnly := testChannel(t, fmt.Sprintf(`{"id":"1","type":0,"guild_id":"%d","name":"c","permission_overwrites":[`+
		`{"id":"%d","type":0,"deny":"%d","allow":"0"},{"id":"20","type":0,"allow":"%d","deny":"0"}]}`, sbGuild, sbGuild, view, view))
	thread := func(typ discord.ChannelType) discord.GuildChannel {
		return testChannel(t, fmt.Sprintf(`{"id":"2","type":%d,"guild_id":"%d","name":"t","parent_id":"1","thread_metadata":{}}`, typ, sbGuild))
	}
	publicThread, privateThread := thread(discord.ChannelTypeGuildPublicThread), thread(discord.ChannelTypeGuildPrivateThread)

	tests := []struct {
		name                   string
		source, parent, output discord.GuildChannel
		want, wantErr          bool
	}{
		{name: "public to public", source: public, output: public, want: true},
		{name: "staff-only source, public output", source: staffOnly, output: public, want: true},
		{name: "age-restricted source, unrestricted output", source: ageRestricted, output: public},
		{name: "age-restricted source and output", source: ageRestricted, output: ageRestricted, want: true},
		{name: "unrestricted source, age-restricted output", source: public, output: ageRestricted, want: true},
		{name: "thread in an age-restricted channel", source: publicThread, parent: ageRestricted, output: public},
		{name: "thread in an age-restricted channel, age-restricted output", source: publicThread, parent: ageRestricted, output: ageRestricted, want: true},
		{name: "post in an age-restricted forum", source: publicThread, parent: ageRestrictedForum, output: public},
		{name: "public thread in public channel", source: publicThread, parent: public, output: public, want: true},
		{name: "public thread in staff-only channel", source: publicThread, parent: staffOnly, output: public, want: true},
		{name: "private thread", source: privateThread, parent: public, output: public},
		{name: "private thread, parent missing", source: privateThread, output: public},
		{name: "source missing", output: public, wantErr: true},
		{name: "output missing", source: public, wantErr: true},
		{name: "thread parent missing", source: publicThread, output: public, wantErr: true},
		{name: "output passed as a thread", source: public, output: publicThread, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := starboardEligible(tt.source, tt.parent, tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// Channel permissions aren't compared: a message in a channel @everyone can't
// view is posted like any other, and the filter list is what keeps a channel
// or category off the starboard.
func TestStarboardPostsFromRestrictedChannelUnlessFiltered(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)
	b.Client.Caches.AddChannel(testChannel(t, fmt.Sprintf(
		`{"id":"%d","type":0,"guild_id":"%d","name":"staff","parent_id":"%d","permission_overwrites":[{"id":"%d","type":0,"deny":"%d","allow":"0"}]}`,
		sbSource, sbGuild, sbCategory, sbGuild, discord.PermissionViewChannel)))
	posted, filtered := recentID(time.Hour), recentID(2*time.Hour)
	api.setSource(posted, 2)
	api.setSource(filtered, 2)
	st.Entries[posted] = starboardEntry{ChannelID: sbSource, Count: 1}

	b.onReactionAdd(reactionAdd(posted, "⭐"))
	if n := len(api.calls().creates); n != 1 {
		t.Fatalf("%d posts, want the restricted channel's message posted", n)
	}

	st.Settings.Blacklist = []snowflake.ID{sbCategory}
	for range 3 {
		b.onReactionAdd(reactionAdd(filtered, "⭐"))
	}
	if _, ok := entryOf(st, filtered); ok {
		t.Error("stored a reaction in a filtered category")
	}
	if n := len(api.calls().creates); n != 1 {
		t.Errorf("%d posts, want none from a filtered category", n)
	}
}

// Reactions in a private thread, or in an age-restricted channel while the
// starboard isn't age-restricted, are not counted, and nothing is stored.
func TestStarboardSkipsPrivateThreadsAndAgeRestrictedChannels(t *testing.T) {
	threadJSON := `{"id":"800","type":%d,"guild_id":"%d","name":"t","parent_id":"%d","thread_metadata":{}}`
	ageRestrictedSource := fmt.Sprintf(`{"id":"%d","type":0,"guild_id":"%d","name":"coffee","nsfw":true}`, sbSource, sbGuild)
	tests := []struct {
		name      string
		channelID snowflake.ID
		channels  []string
	}{
		{"private thread", 800, []string{fmt.Sprintf(threadJSON, discord.ChannelTypeGuildPrivateThread, sbGuild, sbSource)}},
		{"age-restricted channel", sbSource, []string{ageRestrictedSource}},
		{"thread in an age-restricted channel", 800, []string{ageRestrictedSource, fmt.Sprintf(threadJSON, discord.ChannelTypeGuildPublicThread, sbGuild, sbSource)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, api, st, _ := newStarboardTest(t)
			for _, raw := range tt.channels {
				b.Client.Caches.AddChannel(testChannel(t, raw))
			}
			msgID := recentID(time.Hour)
			api.setSource(msgID, 5)

			for range 3 {
				e := reactionAdd(msgID, "⭐")
				e.ChannelID = tt.channelID
				b.onReactionAdd(e)
			}
			if _, ok := entryOf(st, msgID); ok {
				t.Error("stored a reaction that must be skipped")
			}
			if n := len(api.calls().creates); n != 0 {
				t.Errorf("%d posts, want none", n)
			}
		})
	}
}

func TestApplyReaction(t *testing.T) {
	const msgID snowflake.ID = 1
	seed := starboardEntry{ChannelID: 100, AuthorID: 200}
	tests := []struct {
		name       string
		entry      *starboardEntry
		work       *starboardWork
		change     reactionChange
		want       starboardAction
		wantCount  int
		wantExists bool
		wantWork   starboardWork
	}{
		{name: "first add creates the entry", change: reactionAdded, wantCount: 1, wantExists: true},
		{name: "add reaching threshold posts", entry: &starboardEntry{Count: 2}, change: reactionAdded,
			want: starboardPost, wantCount: 3, wantExists: true, wantWork: starboardWork{posting: true}},
		{name: "add during a post only marks it dirty", entry: &starboardEntry{Count: 3}, work: &starboardWork{posting: true},
			change: reactionAdded, wantCount: 4, wantExists: true, wantWork: starboardWork{posting: true, dirty: true}},
		{name: "add to a posted entry edits", entry: &starboardEntry{Count: 5, StarboardMsgID: 9}, change: reactionAdded,
			want: starboardUpdate, wantCount: 6, wantExists: true, wantWork: starboardWork{updating: true}},
		{name: "add during an edit only marks it dirty", entry: &starboardEntry{Count: 5, StarboardMsgID: 9}, work: &starboardWork{updating: true},
			change: reactionAdded, wantCount: 6, wantExists: true, wantWork: starboardWork{updating: true, dirty: true}},
		{name: "remove floors at zero", entry: &starboardEntry{Count: 0}, change: reactionRemoved, wantExists: true},
		{name: "remove never posts", entry: &starboardEntry{Count: 5}, change: reactionRemoved, wantCount: 4, wantExists: true},
		{name: "remove on unknown message stores nothing", change: reactionRemoved},
		{name: "clear zeroes a posted entry and edits", entry: &starboardEntry{Count: 5, StarboardMsgID: 9}, change: reactionsCleared,
			want: starboardUpdate, wantExists: true, wantWork: starboardWork{updating: true}},
		{name: "add on a deleted message changes nothing", entry: &starboardEntry{Count: 5, StarboardMsgID: 9, SourceDeleted: true},
			change: reactionAdded, wantCount: 5, wantExists: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &starboardState{Settings: starboardSettings{Threshold: 3}, Entries: map[snowflake.ID]starboardEntry{}}
			if tt.entry != nil {
				st.Entries[msgID] = *tt.entry
			}
			if tt.work != nil {
				*st.startWork(msgID) = *tt.work
			}
			if got := st.applyReaction(msgID, tt.change, seed); got != tt.want {
				t.Errorf("action = %v, want %v", got, tt.want)
			}
			entry, ok := st.Entries[msgID]
			if ok != tt.wantExists || entry.Count != tt.wantCount {
				t.Errorf("entry = %+v (exists %v), want count %d (exists %v)", entry, ok, tt.wantCount, tt.wantExists)
			}
			if tt.entry == nil && ok && (entry.ChannelID != seed.ChannelID || entry.AuthorID != seed.AuthorID) {
				t.Errorf("new entry %+v not seeded from %+v", entry, seed)
			}
			var gotWork starboardWork
			if w := st.work[msgID]; w != nil {
				gotWork = *w
			}
			if gotWork != tt.wantWork {
				t.Errorf("work = %+v, want %+v", gotWork, tt.wantWork)
			}
		})
	}
}

func TestPruneStarboardEntries(t *testing.T) {
	now := time.Now()
	recentUnposted := snowflake.New(now.Add(-1 * time.Hour))
	oldUnposted := snowflake.New(now.Add(-31 * 24 * time.Hour))
	oldInFlight := snowflake.New(now.Add(-32 * 24 * time.Hour))
	postedRecent := snowflake.New(now.Add(-60 * 24 * time.Hour))
	postedOld := snowflake.New(now.Add(-400 * 24 * time.Hour))

	st := &starboardState{
		Entries: map[snowflake.ID]starboardEntry{
			recentUnposted: {ChannelID: 1, Count: 2},
			oldUnposted:    {ChannelID: 1, Count: 3},
			oldInFlight:    {ChannelID: 1, Count: 5},
			postedRecent:   {ChannelID: 1, Count: 5, StarboardMsgID: 777},
			postedOld:      {ChannelID: 1, Count: 5, StarboardMsgID: 778},
		},
	}
	st.startWork(oldInFlight).posting = true

	pruneStarboardEntries(st, now)

	for id, want := range map[snowflake.ID]bool{
		recentUnposted: true, oldUnposted: false, oldInFlight: true, postedRecent: true, postedOld: true,
	} {
		if _, ok := st.Entries[id]; ok != want {
			t.Errorf("entry %d kept = %v, want %v", id, ok, want)
		}
	}
}

// Reactions on messages past starboardMaxAge are ignored: they aren't posted
// and their posts aren't edited.
func TestStarboardIgnoresMessagesPastMaxAge(t *testing.T) {
	b, api, st, _ := newStarboardTest(t)
	old := recentID(starboardMaxAge + time.Hour)
	api.setSource(old, 10)

	for range 3 {
		b.onReactionAdd(reactionAdd(old, "⭐"))
	}
	if _, ok := entryOf(st, old); ok {
		t.Error("tracked reactions on a message past the max age")
	}
	if len(api.calls().creates) != 0 {
		t.Error("posted a message past the max age")
	}

	st.Entries[old] = starboardEntry{ChannelID: sbSource, Count: 10, StarboardMsgID: 8000, PostChannelID: sbOutput}
	b.recordReactionRemoval(sbGuild, old, "⭐", reactionRemoved)
	if e, _ := entryOf(st, old); e.Count != 10 {
		t.Errorf("count changed to %d on a message past the max age", e.Count)
	}
}

// The handler refuses output channels the starboard can't post in.
func TestStarboardConfigRejectsNonTextChannel(t *testing.T) {
	tests := []struct {
		typ    discord.ChannelType
		wantOK bool
	}{
		{discord.ChannelTypeGuildText, true},
		{discord.ChannelTypeGuildNews, true},
		{discord.ChannelTypeGuildVoice, false},
		{discord.ChannelTypeGuildCategory, false},
		{discord.ChannelTypeGuildForum, false},
		{discord.ChannelTypeGuildPublicThread, false},
	}
	for _, tt := range tests {
		b, _, st, _ := newStarboardTest(t)
		data := fmt.Sprintf(`{"id":"3","name":"config","type":1,
			"options":[{"name":"starboard","type":2,"options":[{"name":"config","type":1,"options":[{"name":"channel","type":7,"value":"900"}]}]}],
			"resolved":{"channels":{"900":{"id":"900","name":"c","type":%d,"permissions":"0"}}}}`, tt.typ)
		e, responses := sprobotCommandEvent(t, data)

		b.handleSBConfigSet(e, sbGuild, st)

		resp := <-responses
		gotOK := st.Settings.OutputChannelID == 900
		if gotOK != tt.wantOK {
			t.Errorf("type %d: output set = %v, want %v (response %q)", tt.typ, gotOK, tt.wantOK, resp.Content)
		}
		if !tt.wantOK && !strings.Contains(resp.Content, "text or announcement") {
			t.Errorf("type %d: response %q does not explain the rejection", tt.typ, resp.Content)
		}
	}
}

// sprobotCommandEvent builds a slash command interaction in sbGuild carrying
// data. Its responses go to the returned channel.
func sprobotCommandEvent(t *testing.T, data string) (*events.ApplicationCommandInteractionCreate, <-chan discord.MessageCreate) {
	t.Helper()
	raw := fmt.Sprintf(`{"id":"1","application_id":"2","type":2,"token":"tok","guild_id":"%d",
		"channel":{"id":"%d","type":0,"guild_id":"%d","name":"coffee"},
		"user":{"id":"9","username":"mod"},"data":%s}`, sbGuild, sbSource, sbGuild, data)
	var i discord.ApplicationCommandInteraction
	if err := json.Unmarshal([]byte(raw), &i); err != nil {
		t.Fatalf("decoding interaction: %v", err)
	}
	responses := make(chan discord.MessageCreate, 4)
	return &events.ApplicationCommandInteractionCreate{
		ApplicationCommandInteraction: i,
		Respond: func(_ discord.InteractionResponseType, d discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			m, _ := d.(discord.MessageCreate)
			responses <- m
			return nil
		},
	}, responses
}

func TestReactionCount(t *testing.T) {
	reactions := []discord.MessageReaction{
		{Count: 4, Emoji: discord.Emoji{Name: "⭐"}},
		{Count: 6, Emoji: discord.Emoji{Name: "ehhhh", ID: 786302563178774560}},
	}
	for emoji, want := range map[string]int{"⭐": 4, "ehhhh:786302563178774560": 6, "🔥": 0} {
		if got := reactionCount(reactions, emoji); got != want {
			t.Errorf("reactionCount(%q) = %d, want %d", emoji, got, want)
		}
	}
}

func TestStarboardStateJSONRoundTrip(t *testing.T) {
	st := &starboardState{
		Settings: starboardSettings{
			OutputChannelID: 123456,
			Emoji:           "⭐",
			Threshold:       3,
			Blacklist:       []snowflake.ID{111, 222},
		},
		Entries: map[snowflake.ID]starboardEntry{
			999: {ChannelID: 100, AuthorID: 200, Count: 5, StarboardMsgID: 888, PostChannelID: 123456, SourceDeleted: true},
		},
	}
	st.startWork(999).posting = true

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(data), "mu") || strings.Contains(string(data), "work") || strings.Contains(string(data), "posting") {
		t.Errorf("in-memory fields serialized: %s", data)
	}

	var loaded starboardState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.Settings.OutputChannelID != 123456 {
		t.Errorf("expected OutputChannelID 123456, got %d", loaded.Settings.OutputChannelID)
	}
	if loaded.Settings.Emoji != "⭐" {
		t.Errorf("expected Emoji ⭐, got %s", loaded.Settings.Emoji)
	}
	if loaded.Settings.Threshold != 3 {
		t.Errorf("expected Threshold 3, got %d", loaded.Settings.Threshold)
	}
	if len(loaded.Settings.Blacklist) != 2 {
		t.Errorf("expected 2 blacklist entries, got %d", len(loaded.Settings.Blacklist))
	}
	if len(loaded.Entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(loaded.Entries))
	}
	entry := loaded.Entries[999]
	if entry.Count != 5 || entry.ChannelID != 100 || entry.AuthorID != 200 || entry.StarboardMsgID != 888 || entry.PostChannelID != 123456 || !entry.SourceDeleted {
		t.Errorf("unexpected entry data: %+v", entry)
	}
}

func TestEffectiveThresholdDefault(t *testing.T) {
	s := &starboardSettings{}
	if s.effectiveThreshold() != defaultThreshold {
		t.Errorf("expected default threshold %d, got %d", defaultThreshold, s.effectiveThreshold())
	}
}

func TestEffectiveThresholdCustom(t *testing.T) {
	s := &starboardSettings{Threshold: 10}
	if s.effectiveThreshold() != 10 {
		t.Errorf("expected threshold 10, got %d", s.effectiveThreshold())
	}
}

func TestIsBlacklisted(t *testing.T) {
	s := &starboardSettings{
		Blacklist: []snowflake.ID{100, 200, 300},
	}

	if !s.isBlacklisted(200) {
		t.Error("200 should be blacklisted")
	}
	if s.isBlacklisted(999) {
		t.Error("999 should not be blacklisted")
	}
}

func TestIsBlacklistedEmpty(t *testing.T) {
	s := &starboardSettings{}
	if s.isBlacklisted(100) {
		t.Error("empty blacklist should never match")
	}
}

func TestEmojiDisplayUnicode(t *testing.T) {
	if got := emojiDisplay("⭐"); got != "⭐" {
		t.Errorf("expected ⭐, got %s", got)
	}
}

func TestEmojiDisplayCustom(t *testing.T) {
	if got := emojiDisplay("fire:123456"); got != "<:fire:123456>" {
		t.Errorf("expected <:fire:123456>, got %s", got)
	}
}

func TestParseEmojiInputUnicode(t *testing.T) {
	if got := parseEmojiInput("⭐"); got != "⭐" {
		t.Errorf("expected ⭐, got %s", got)
	}
}

func TestParseEmojiInputCustom(t *testing.T) {
	if got := parseEmojiInput("<:fire:123456>"); got != "fire:123456" {
		t.Errorf("expected fire:123456, got %s", got)
	}
}

func TestParseEmojiInputAnimated(t *testing.T) {
	if got := parseEmojiInput("<a:dance:789>"); got != "dance:789" {
		t.Errorf("expected dance:789, got %s", got)
	}
}

func TestIntPtr(t *testing.T) {
	p := intPtr(42)
	if *p != 42 {
		t.Errorf("expected 42, got %d", *p)
	}
}

func TestEmojiCDNURLUnicode(t *testing.T) {
	if got := emojiCDNURL("⭐"); got != "" {
		t.Errorf("expected empty string, got %s", got)
	}
}

func TestEmojiCDNURLCustom(t *testing.T) {
	got := emojiCDNURL("ehhhh:786302563178774560")
	want := "https://cdn.discordapp.com/emojis/786302563178774560.png"
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestStarboardFooterCustomEmoji(t *testing.T) {
	got := starboardFooter("ehhhh:786302563178774560", 7, "general")
	if got.Text != "7 | #general" {
		t.Errorf("Text = %q, want %q", got.Text, "7 | #general")
	}
	if got.IconURL == "" {
		t.Error("expected IconURL to be set for custom emoji")
	}
}

func TestStarboardFooterUnicodeEmoji(t *testing.T) {
	got := starboardFooter("⭐", 3, "art")
	if got.Text != "⭐ 3 | #art" {
		t.Errorf("Text = %q, want %q", got.Text, "⭐ 3 | #art")
	}
	if got.IconURL != "" {
		t.Errorf("expected empty IconURL for unicode emoji, got %s", got.IconURL)
	}
}

func TestEmojiDisplayEdgeCases(t *testing.T) {
	// Colon but no ID part — should be treated as unicode
	if got := emojiDisplay("foo:"); got != "foo:" {
		t.Errorf("expected foo:, got %s", got)
	}

	// Empty string
	if got := emojiDisplay(""); got != "" {
		t.Errorf("expected empty string, got %s", got)
	}
}
