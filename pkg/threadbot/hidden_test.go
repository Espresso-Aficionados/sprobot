package threadbot

import (
	"net/http/httptest"
	"strings"
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
