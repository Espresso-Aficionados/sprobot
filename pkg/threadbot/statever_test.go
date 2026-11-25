package threadbot

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Espresso-Aficionados/sprobot/pkg/statever"
	"github.com/Espresso-Aficionados/sprobot/pkg/testutil"
)

const testGuild = 1013566342345019512

var (
	remindersObject    = "/test-bucket/threadreminders/1013566342345019512.json"
	memberCountsObject = "/test-bucket/threadmembercounts/1013566342345019512.json"
	hiddenObject       = "/test-bucket/threadhidden/1013566342345019512.json"
)

// State a newer build wrote is refused for startup to exit on, and its
// guild stays unloaded, so nothing saves over it.
func TestLoadRefusesNewer(t *testing.T) {
	for _, obj := range []string{remindersObject, memberCountsObject, hiddenObject} {
		t.Run(obj, func(t *testing.T) {
			fake := testutil.NewFakeS3()
			server := httptest.NewServer(fake)
			defer server.Close()
			const newer = `{"version":2,"renamed":{}}`
			fake.Objects[obj] = []byte(newer)
			b := newTestBot(t, testutil.NewTestS3Client(t, server))

			err := errors.Join(b.loadReminders(t.Context()), b.loadMemberCounts(t.Context()), b.loadHiddenThreads(t.Context()))
			if !statever.IsNewer(err) || !strings.Contains(err.Error(), "is state version 2") {
				t.Fatalf("loads = %v, want one refusal", err)
			}
			b.saveAllReminders(t.Context())
			b.saveMemberCounts(t.Context())
			if err := b.resetMemberCounts(t.Context(), testGuild, func() {}); obj == memberCountsObject && !statever.IsNewer(err) {
				t.Errorf("/threadbot refreshcounts: %v, want a refusal", err)
			}
			if _, err := b.setHidden(t.Context(), testGuild, 5, true); obj == hiddenObject && err == nil {
				t.Error("setHidden saved over a newer build's list")
			}
			if got := string(fake.Objects[obj]); got != newer {
				t.Fatalf("%s changed: %s", obj, got)
			}
		})
	}
}

// Member counts and hidden threads are stamped (older builds ignore the
// key); reminders, a map of channel IDs, stay unversioned. All three load
// with or without one.
func TestSavedVersions(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	fake.Objects[remindersObject] = []byte(`{"version":1,"111":{"channel_id":"111","guild_id":"1013566342345019512","min_idle_mins":30,"max_idle_mins":720,"time_threshold_mins":60}}`)
	fake.Objects[memberCountsObject] = []byte(`{"counts":{"5":3}}`)
	b := newTestBot(t, testutil.NewTestS3Client(t, server))
	if err := errors.Join(b.loadReminders(t.Context()), b.loadMemberCounts(t.Context()), b.loadHiddenThreads(t.Context())); err != nil {
		t.Fatal(err)
	}
	if len(b.reminders[testGuild]) != 1 || b.memberCounts[testGuild].Counts[5] != 3 {
		t.Fatalf("loaded reminders %v, counts %v", b.reminders[testGuild], b.memberCounts[testGuild])
	}
	b.saveAllReminders(t.Context())
	b.saveMemberCounts(t.Context())
	if _, err := b.setHidden(t.Context(), testGuild, 7, true); err != nil {
		t.Fatal(err)
	}
	if got := string(fake.Objects[remindersObject]); strings.Contains(got, "version") {
		t.Errorf("reminders saved as %s: they must stay unversioned until older builds read a version", got)
	}
	for _, obj := range []string{memberCountsObject, hiddenObject} {
		if got := string(fake.Objects[obj]); !strings.HasPrefix(got, `{"version":1,`) {
			t.Errorf("%s saved as %s, want version 1 first", obj, got)
		}
	}
}

func TestCheckState(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	s3 := testutil.NewTestS3Client(t, server)
	r := CheckState(t.Context(), s3)
	if code := r.Print(new(bytes.Buffer), new(bytes.Buffer), false); code != 0 || len(r.Docs) != 6 {
		t.Fatalf("no state: exit %d, %d documents", code, len(r.Docs))
	}
	fake.Objects[hiddenObject] = []byte(`{"version":2}`)
	if code := CheckState(t.Context(), s3).Print(new(bytes.Buffer), new(bytes.Buffer), false); code != 3 {
		t.Fatalf("newer: exit %d", code)
	}
	for _, req := range fake.Requests {
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("-check-state sent %s", req)
		}
	}
}
