package botutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
)

// Gateway session handoff: a bot stopping for a restart closes its gateway
// with 1012 (Service Restart), which leaves the session resumable, and
// writes it to GATEWAY_SESSION_DIR; the next process takes the file and
// resumes, so Discord replays the events sent in between instead of losing
// them. Without the variable (development) nothing here runs: the gateway
// closes with 1000 as always and every start identifies.
//
// The file holds no user data: the session's ID, sequence number and resume
// URL, the guild IDs the bot was in, and two times.

// sessionFile is the handoff's file name in the session directory.
const sessionFile = "session.json"

// sessionVersion is the file's format; any other is ignored.
const sessionVersion = 1

// sessionMaxAge is how long after the close a session is still taken.
// Discord's window is undocumented: a dev-bot session resumed 60s after a
// 1012 close and was refused at 120s (2026-11-26). A restart takes ~10s;
// an older file would only cost an invalid-session round trip (~2.5s)
// before the identify.
const sessionMaxAge = 60 * time.Second

// handlerWait bounds how long a handoff waits for event handlers still
// running (events are dispatched asynchronously) before giving up on it.
const handlerWait = 5 * time.Second

// GatewaySession is a closed gateway session, handed to the next process.
type GatewaySession struct {
	Version       int          `json:"version"`
	ApplicationID snowflake.ID `json:"application_id"`
	SessionID     string       `json:"session_id"`
	Sequence      int          `json:"sequence"`
	ResumeURL     string       `json:"resume_url"`
	// StartedAt is when the session was identified: a resume continues it.
	StartedAt time.Time `json:"started_at"`
	// ClosedAt is the 1012 close, which Discord's resume window counts from.
	ClosedAt time.Time `json:"closed_at"`
	// Guilds are the allowed guilds the bot was in. A resume sends no
	// GUILD_CREATE, so a bot without persisted caches knows them only from
	// here.
	Guilds []snowflake.ID `json:"guilds"`
}

// takeSession reads dir's session file and removes it, so it is used at
// most once whatever it holds. It returns nil, nil when there is none, and an
// error saying why when the file can't be used.
func takeSession(dir string, now time.Time) (*GatewaySession, error) {
	path := filepath.Join(dir, sessionFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return nil, fmt.Errorf("removing it: %w", rmErr)
	}
	if err != nil {
		return nil, err
	}
	var s GatewaySession
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decoding it: %w", err)
	}
	switch age := now.Sub(s.ClosedAt); {
	case s.Version != sessionVersion:
		return nil, fmt.Errorf("version %d, not %d", s.Version, sessionVersion)
	case s.SessionID == "" || s.ApplicationID == 0 || !strings.HasPrefix(s.ResumeURL, "wss://") || s.ClosedAt.IsZero():
		return nil, errors.New("incomplete")
	case age < 0:
		return nil, fmt.Errorf("closed in the future (%s)", s.ClosedAt.Format(time.RFC3339))
	case age > sessionMaxAge:
		return nil, fmt.Errorf("closed %s ago, past Discord's resume window", age.Round(time.Second))
	}
	return &s, nil
}

// writeSession writes s to dir's session file atomically (temp file, fsync,
// rename), readable by the bot alone.
func writeSession(dir string, s GatewaySession) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, sessionFile+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // after a successful rename there is nothing left to remove
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, sessionFile))
}

// takeResumableSession takes the session a previous process handed off, if
// handoffs are on and it is usable; b.resumed is nil otherwise.
func (b *BaseBot) takeResumableSession() {
	if b.sessionDir == "" {
		return
	}
	s, err := takeSession(b.sessionDir, time.Now())
	switch {
	case err != nil:
		b.Log.Warn("Not resuming the previous gateway session", "reason", err)
	case s != nil:
		b.resumed = s
		b.Log.Info("Resuming the previous gateway session", "closed_ago", time.Since(s.ClosedAt).Round(time.Millisecond), "sequence", s.Sequence, "guilds", len(s.Guilds))
	}
}

// GatewayResumeOpts are the gateway options that resume the session taken at
// startup; none without one. Pass them to bot.WithGatewayConfigOpts.
func (b *BaseBot) GatewayResumeOpts() []gateway.ConfigOpt {
	if b.resumed == nil {
		return nil
	}
	return []gateway.ConfigOpt{
		gateway.WithSessionID(b.resumed.SessionID),
		gateway.WithSequence(b.resumed.Sequence),
		gateway.WithResumeURL(b.resumed.ResumeURL),
	}
}

// Resumed is the session this process is resuming, nil when it identifies.
// A bot seeds from it, before the gateway opens, what GuildReady would have
// set: a resume replays the missed events before RESUMED, and sends no
// READY or GUILD_CREATE. If Discord refuses the resume, READY and GuildReady
// follow as on any start, so the seeding must be safe to repeat.
func (b *BaseBot) Resumed() *GatewaySession {
	return b.resumed
}

// SetResumed sets the session being resumed, as takeResumableSession would:
// for tests of a bot's resume seeding, which have no session directory.
func (b *BaseBot) SetResumed(s *GatewaySession) {
	b.resumed = s
}

// DropResume abandons the session taken at startup: call it before the
// client is built when what the bot would resume with didn't load (sprobot's
// caches). It identifies instead.
func (b *BaseBot) DropResume(reason string) {
	if b.resumed != nil {
		b.Log.Warn("Not resuming the previous gateway session", "reason", reason)
		b.resumed = nil
	}
}

// checkResumedApp drops a session taken for another application. The
// directory is per bot, so this is a guard, not an expected case; the
// gateway's options are already set, and Discord refuses the resume.
func (b *BaseBot) checkResumedApp() {
	if b.resumed != nil && b.Client != nil && b.resumed.ApplicationID != b.Client.ApplicationID {
		b.Log.Warn("The handed-off gateway session is another application's; identifying")
		b.resumed = nil
	}
}

// InResumedSession reports whether the gateway's session is the one taken at
// startup: false when Discord refused the resume (disgo has the new
// session's ID by the time Open returns) or there was none to resume.
func (b *BaseBot) InResumedSession() bool {
	if b.resumed == nil || b.Client == nil || b.Client.Gateway == nil {
		return false
	}
	id := b.Client.Gateway.SessionID()
	return id != nil && *id == b.resumed.SessionID
}

// OnResumed is the shared resumed handler for all bots: the session
// continues, so the bot is ready as after a READY. Interactions created
// before now were replayed from the gap (see traceInteraction).
func (b *BaseBot) OnResumed(*events.Resumed) {
	now := time.Now()
	replayedBefore.Store(&now)
	b.Log.Info("Resumed the gateway session")
	b.Ready.Store(true)
}

// replayedBefore is when the last resume completed: interactions created
// before it were replayed (nil until a resume).
var replayedBefore atomic.Pointer[time.Time]

// interactionDeadline is how long Discord waits for an interaction's first
// response; one older can no longer be answered.
const interactionDeadline = 3 * time.Second

// staleReplay reports whether an interaction created at created was
// replayed by a resume and is past answering: its handler would act, then
// fail to respond, and the user, told the bot didn't respond, would likely
// try again. Live interactions, and replayed ones still answerable (a resume
// after a brief disconnect), are handled.
func staleReplay(created, now time.Time) bool {
	cutoff := replayedBefore.Load()
	return cutoff != nil && created.Before(*cutoff) && now.Sub(created) > interactionDeadline
}

// HandOffGateway closes the gateway for a restart: with 1012, so Discord
// keeps the session resumable, then waits for event handlers still running.
// It returns the session to write once the bot's state is saved (see
// SaveHandoff), or nil when there is nothing to hand off: handoffs are off,
// the bot is stopping because the gateway failed (shutdownErr), it has no
// session, or handlers didn't finish. With nil the bot shuts down as it
// always has.
func (b *BaseBot) HandOffGateway(ctx context.Context, shutdownErr error) *GatewaySession {
	if b.sessionDir == "" || shutdownErr != nil || b.Client == nil || b.Client.Gateway == nil {
		return nil
	}
	g := b.Client.Gateway
	g.CloseWithCode(ctx, 1012, "restarting")
	closed := time.Now()
	id, seq, url := g.SessionID(), g.LastSequenceReceived(), g.ResumeURL()
	if id == nil || seq == nil || url == nil {
		b.Log.InfoContext(ctx, "No gateway session to hand off")
		return nil
	}
	if !waitForHandlers(handlerWait) {
		b.Log.WarnContext(ctx, "Event handlers still running; not handing off the gateway session", "waited", handlerWait)
		return nil
	}
	return &GatewaySession{
		Version:       sessionVersion,
		ApplicationID: b.Client.ApplicationID,
		SessionID:     *id,
		Sequence:      *seq,
		ResumeURL:     *url,
		StartedAt:     b.sessionStarted(),
		ClosedAt:      closed,
		Guilds:        b.GuildIDs(),
	}
}

// SaveHandoff writes s, from HandOffGateway, for the next process, last of
// all: after everything the session's events changed is saved. ok says the
// bot's saves that a resume depends on succeeded; without it, or without s,
// nothing is written and the next process identifies.
func (b *BaseBot) SaveHandoff(ctx context.Context, s *GatewaySession, ok bool) {
	if s == nil {
		return
	}
	if !ok {
		b.Log.WarnContext(ctx, "Not handing off the gateway session: the saves it depends on failed")
		return
	}
	if age := time.Since(s.ClosedAt); age > sessionMaxAge {
		b.Log.WarnContext(ctx, "Not handing off the gateway session: shutdown outlasted the resume window", "since_close", age.Round(time.Second))
		return
	}
	if err := writeSession(b.sessionDir, *s); err != nil {
		b.Log.ErrorContext(ctx, "Failed to hand off the gateway session", "error", err)
		return
	}
	b.Log.InfoContext(ctx, "Handed off the gateway session", "sequence", s.Sequence, "guilds", len(s.Guilds))
}

// sessionStarted is when the current gateway session was identified: this
// process's READY, or the resumed session's own start.
func (b *BaseBot) sessionStarted() time.Time {
	if t := b.sessionStart.Load(); t != nil {
		return *t
	}
	if b.resumed != nil {
		return b.resumed.StartedAt
	}
	return time.Time{}
}

// unionAllowed is ids and extra, each allowed guild once, in order.
func unionAllowed(ids, extra []snowflake.ID) []snowflake.ID {
	out := slices.Clone(ids)
	for _, id := range extra {
		if GuildAllowed(id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// handlersRunning counts the traced event and interaction handlers in
// progress (TraceEvent, the Trace* interaction wrappers): events run in
// goroutines of their own, so a closed gateway may still have some.
var handlersRunning atomic.Int64

// waitForHandlers waits up to d for handlersRunning to reach zero and reports
// whether it did.
func waitForHandlers(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for handlersRunning.Load() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}
