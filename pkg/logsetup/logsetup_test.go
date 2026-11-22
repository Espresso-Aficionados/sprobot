package logsetup

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"regexp"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// One format for both logging packages: time, level, msg, then the fields.
func TestFormat(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	var buf bytes.Buffer
	slog.SetDefault(New(&buf))

	slog.Warn("Saved thread reminders", "guild_id", 42, "note", "two words")
	log.Printf("from the %s package", "log")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	want := []string{
		`^time=\S+ level=WARN msg="Saved thread reminders" guild_id=42 note="two words"$`,
		`^time=\S+ level=INFO msg="from the log package"$`,
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), buf.String())
	}
	for i, re := range want {
		if !regexp.MustCompile(re).Match(lines[i]) {
			t.Errorf("line %d = %s\nwant match for %s", i, lines[i], re)
		}
	}
}

// A value can't break out of its line.
func TestNewlinesStayInTheLine(t *testing.T) {
	var buf bytes.Buffer
	New(&buf).Info("request", "user_agent", "x\nlevel=ERROR msg=forged")
	if n := bytes.Count(buf.Bytes(), []byte("\n")); n != 1 {
		t.Errorf("one record wrote %d lines:\n%s", n, buf.String())
	}
}

// A record logged inside a span carries its trace, in the same format.
func TestTraceID(t *testing.T) {
	var buf bytes.Buffer
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3},
		SpanID:     trace.SpanID{4, 5, 6},
		TraceFlags: trace.FlagsSampled,
	})
	New(&buf).InfoContext(trace.ContextWithSpanContext(context.Background(), sc), "request", "status", 200)
	re := `^time=\S+ level=INFO msg=request status=200 trace_id=01020300000000000000000000000000 span_id=0405060000000000\n$`
	if !regexp.MustCompile(re).Match(buf.Bytes()) {
		t.Errorf("got %s\nwant match for %s", buf.String(), re)
	}
}
