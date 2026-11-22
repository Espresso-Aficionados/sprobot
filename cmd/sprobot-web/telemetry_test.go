package main

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Espresso-Aficionados/sprobot/pkg/logsetup"
	"github.com/Espresso-Aficionados/sprobot/pkg/telemetrytest"
	"github.com/Espresso-Aficionados/sprobot/pkg/testutil"
)

// Through the whole handler chain, requests are recorded by the mux pattern
// they matched, never by their IDs, /healthz isn't recorded, and the access
// log line carries the request's trace.
func TestHandlerTelemetry(t *testing.T) {
	rec := telemetrytest.Install(t)
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	h := handler(testutil.NewTestS3Client(t, server))

	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(logsetup.New(&logs))
	t.Cleanup(func() { slog.SetDefault(old) })

	const guildID, userID = "726985544038612993", "123456789012345678"
	for _, target := range []string{
		"/profiles/" + guildID + "/Coffee%20Setup/" + userID,
		"/files/shortcut/" + guildID + "/" + testFileName + ".png/view",
		"/healthz",
		"/no/such/" + userID,
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", target, nil))
	}

	var routes []string
	for _, p := range rec.Points(t, "http.server.request.duration") {
		routes = append(routes, p.Get("http.route"))
		for _, kv := range p.Attrs.ToSlice() {
			if v := kv.Value.String(); strings.Contains(v, guildID) || strings.Contains(v, userID) {
				t.Errorf("metric attribute %s=%s carries an ID", kv.Key, v)
			}
		}
	}
	slices.Sort(routes)
	want := []string{"/", "/files/{kind}/{guildID}/{name}/view", "/profiles/{guildID}/{templateName}/{userID}"}
	if !slices.Equal(routes, want) {
		t.Errorf("routes = %q, want %q", routes, want)
	}

	for _, s := range rec.Spans() {
		if strings.Contains(s.Name, "healthz") {
			t.Errorf("span %q: /healthz is traced", s.Name)
		}
		for _, kv := range telemetrytest.Exported(s.Attributes) {
			if v := kv.Value.String(); strings.Contains(v, guildID) || strings.Contains(v, userID) {
				t.Errorf("span %q attribute %s=%s carries an ID", s.Name, kv.Key, v)
			}
		}
	}

	logged := 0
	for line := range strings.Lines(logs.String()) {
		if strings.Contains(line, `msg=request`) {
			logged++
			if !strings.Contains(line, "trace_id=") {
				t.Errorf("access log line without trace_id: %s", line)
			}
		}
	}
	if logged != 3 {
		t.Errorf("%d access log lines, want 3:\n%s", logged, logs.String())
	}
}
