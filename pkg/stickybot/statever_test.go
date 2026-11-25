package stickybot

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Espresso-Aficionados/sprobot/pkg/statever"
	"github.com/Espresso-Aficionados/sprobot/pkg/testutil"
)

const testStickiesObject = "/test-bucket/stickies/1013566342345019512.json"

// Stickies a newer build wrote are refused for startup to exit on, and the
// guild stays unloaded, so nothing saves over them.
func TestLoadStickiesRefusesNewer(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	const newer = `{"version":2,"stickies":{"111":{"channel_id":"111"}}}`
	fake.Objects[testStickiesObject] = []byte(newer)

	b := newTestBot(t, testutil.NewTestS3Client(t, server))
	err := b.loadStickies(t.Context())
	if !statever.IsNewer(err) || !strings.Contains(err.Error(), "stickies/1013566342345019512.json is state version 2") {
		t.Fatalf("loadStickies = %v, want a refusal", err)
	}
	if b.guildLoaded(t.Context(), testGuildID) {
		t.Fatal("guild loaded from a newer build's stickies")
	}
	b.saveUnsavedStickies(t.Context())
	if got := string(fake.Objects[testStickiesObject]); got != newer {
		t.Fatalf("stickies changed: %s", got)
	}
}

// Stickies are a map of channel IDs, written without a version (old builds
// would fail on one), but read with or without it.
func TestLoadStickiesVersionedOrNot(t *testing.T) {
	for _, stored := range []string{
		`{"111":{"channel_id":"111","guild_id":"1013566342345019512","max_idle_mins":10,"time_threshold_mins":5}}`,
		`{"version":1,"111":{"channel_id":"111","guild_id":"1013566342345019512","max_idle_mins":10,"time_threshold_mins":5}}`,
	} {
		fake := testutil.NewFakeS3()
		server := httptest.NewServer(fake)
		fake.Objects[testStickiesObject] = []byte(stored)
		b := newTestBot(t, testutil.NewTestS3Client(t, server))
		if err := b.loadStickies(t.Context()); err != nil {
			t.Fatalf("%s: %v", stored, err)
		}
		b.mu.Lock()
		n := len(b.stickies[testGuildID])
		b.mu.Unlock()
		if n != 1 {
			t.Errorf("%s: loaded %d stickies, want 1", stored, n)
		}
		b.stopAllStickyGoroutines()
		b.saveStickiesForGuild(t.Context(), testGuildID)
		if got := string(fake.Objects[testStickiesObject]); strings.Contains(got, "version") {
			t.Errorf("saved %s: stickies must stay unversioned until older builds read a version", got)
		}
		server.Close()
	}
}

func TestCheckState(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	s3 := testutil.NewTestS3Client(t, server)

	if code := CheckState(t.Context(), s3).Print(new(bytes.Buffer), new(bytes.Buffer), false); code != 0 {
		t.Fatalf("no stickies: exit %d", code)
	}
	fake.Objects[testStickiesObject] = []byte(`{"111":{"channel_id":"111"}}`)
	if code := CheckState(t.Context(), s3).Print(new(bytes.Buffer), new(bytes.Buffer), false); code != 0 {
		t.Fatalf("unversioned stickies: exit %d", code)
	}
	fake.Objects[testStickiesObject] = []byte(`{"version":3}`)
	if code := CheckState(t.Context(), s3).Print(new(bytes.Buffer), new(bytes.Buffer), false); code != 3 {
		t.Fatalf("newer stickies: exit %d", code)
	}
	fake.Fail = map[string]int{testStickiesObject: 403}
	if code := CheckState(t.Context(), s3).Print(new(bytes.Buffer), new(bytes.Buffer), false); code != 1 {
		t.Fatalf("unreadable stickies: exit %d", code)
	}
	for _, req := range fake.Requests {
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("-check-state sent %s", req)
		}
	}
}
