package botutil

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

func validSession(closed time.Time) GatewaySession {
	return GatewaySession{
		Version:       sessionVersion,
		ApplicationID: 123,
		SessionID:     "abc",
		Sequence:      42,
		ResumeURL:     "wss://gateway-us-east1-b.discord.gg",
		StartedAt:     closed.Add(-time.Hour),
		ClosedAt:      closed,
		Guilds:        []snowflake.ID{AllowedGuildIDs[0]},
	}
}

func TestWriteThenTakeSession(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	want := validSession(now.Add(-10 * time.Second))
	if err := writeSession(dir, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, sessionFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session file: %v, mode %v; want 0600", err, info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("dir holds %d entries, want the session file alone (no temp file left)", len(entries))
	}
	got, err := takeSession(dir, now)
	if err != nil || got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("takeSession = %+v, %v; want %+v", got, err, want)
	}
	// Taken once: the file is gone, and a second take finds nothing.
	if got, err := takeSession(dir, now); got != nil || err != nil {
		t.Fatalf("second takeSession = %+v, %v; want nil, nil", got, err)
	}
}

func TestTakeSessionNone(t *testing.T) {
	if got, err := takeSession(t.TempDir(), time.Now()); got != nil || err != nil {
		t.Fatalf("takeSession = %+v, %v; want nil, nil", got, err)
	}
}

// Every unusable file is refused with a reason, and removed all the same.
func TestTakeSessionRefuses(t *testing.T) {
	now := time.Now().UTC()
	for name, c := range map[string]struct {
		edit func(*GatewaySession)
		raw  string
		want string
	}{
		"expired":      {edit: func(s *GatewaySession) { s.ClosedAt = now.Add(-sessionMaxAge - time.Second) }, want: "resume window"},
		"future":       {edit: func(s *GatewaySession) { s.ClosedAt = now.Add(time.Minute) }, want: "future"},
		"version":      {edit: func(s *GatewaySession) { s.Version = 2 }, want: "version 2"},
		"no session":   {edit: func(s *GatewaySession) { s.SessionID = "" }, want: "incomplete"},
		"no app":       {edit: func(s *GatewaySession) { s.ApplicationID = 0 }, want: "incomplete"},
		"bad url":      {edit: func(s *GatewaySession) { s.ResumeURL = "https://example.com" }, want: "incomplete"},
		"never closed": {edit: func(s *GatewaySession) { s.ClosedAt = time.Time{} }, want: "incomplete"},
		"garbage":      {raw: "{not json", want: "decoding"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if c.raw != "" {
				if err := os.WriteFile(filepath.Join(dir, sessionFile), []byte(c.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				s := validSession(now.Add(-time.Second))
				c.edit(&s)
				if err := writeSession(dir, s); err != nil {
					t.Fatal(err)
				}
			}
			got, err := takeSession(dir, now)
			if got != nil || err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("takeSession = %+v, %v; want an error with %q", got, err, c.want)
			}
			if _, err := os.Stat(filepath.Join(dir, sessionFile)); !os.IsNotExist(err) {
				t.Fatalf("the refused file is still there (%v)", err)
			}
		})
	}
}

func TestUnionAllowed(t *testing.T) {
	a, b := AllowedGuildIDs[0], AllowedGuildIDs[1]
	other := snowflake.ID(1)
	got := unionAllowed([]snowflake.ID{a}, []snowflake.ID{a, other, b, b})
	if want := []snowflake.ID{a, b}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unionAllowed = %v, want %v (each allowed guild once, others dropped)", got, want)
	}
}

func TestWaitForHandlers(t *testing.T) {
	if !waitForHandlers(10 * time.Millisecond) {
		t.Fatal("no handler running: want true")
	}
	handlersRunning.Add(1)
	if waitForHandlers(30 * time.Millisecond) {
		t.Fatal("a handler still running: want false")
	}
	go func() { time.Sleep(20 * time.Millisecond); handlersRunning.Add(-1) }()
	if !waitForHandlers(time.Second) {
		t.Fatal("the handler finished: want true")
	}
}

// Without GATEWAY_SESSION_DIR, nothing is handed off: the bot shuts down as
// it always has.
func TestHandOffGatewayOff(t *testing.T) {
	b := &BaseBot{Log: testLogger()}
	if s := b.HandOffGateway(t.Context(), nil); s != nil {
		t.Fatalf("HandOffGateway = %+v, want nil with handoffs off", s)
	}
	b.SaveHandoff(t.Context(), nil, true) // nothing to write, no panic
}

// SaveHandoff writes only a session whose saves succeeded, within the window.
func TestSaveHandoff(t *testing.T) {
	dir := t.TempDir()
	b := &BaseBot{Log: testLogger(), sessionDir: dir}
	s := validSession(time.Now())
	b.SaveHandoff(t.Context(), &s, false)
	if _, err := os.Stat(filepath.Join(dir, sessionFile)); !os.IsNotExist(err) {
		t.Fatal("wrote the session though its saves failed")
	}
	old := validSession(time.Now().Add(-sessionMaxAge - time.Second))
	b.SaveHandoff(t.Context(), &old, true)
	if _, err := os.Stat(filepath.Join(dir, sessionFile)); !os.IsNotExist(err) {
		t.Fatal("wrote a session already past the resume window")
	}
	b.SaveHandoff(t.Context(), &s, true)
	if got, err := takeSession(dir, time.Now()); err != nil || got == nil || got.SessionID != s.SessionID {
		t.Fatalf("takeSession after SaveHandoff = %+v, %v", got, err)
	}
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// An interaction replayed by a resume is skipped only once it is past
// Discord's answer deadline; live ones, and replayed ones still answerable,
// are handled.
func TestStaleReplay(t *testing.T) {
	defer replayedBefore.Store(nil)
	now := time.Now()
	if staleReplay(now.Add(-time.Minute), now) {
		t.Fatal("no resume yet: nothing is a replay")
	}
	resumed := now.Add(-time.Second)
	replayedBefore.Store(&resumed)
	for _, c := range []struct {
		name    string
		created time.Time
		want    bool
	}{
		{"replayed, 10s old", now.Add(-10 * time.Second), true},
		{"replayed, 2s old", now.Add(-2 * time.Second), false},
		{"after the resume", now.Add(-500 * time.Millisecond), false},
	} {
		if got := staleReplay(c.created, now); got != c.want {
			t.Errorf("%s: staleReplay = %t, want %t", c.name, got, c.want)
		}
	}
}
