package logsetup

import (
	"bytes"
	"log"
	"log/slog"
	"regexp"
	"testing"
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
