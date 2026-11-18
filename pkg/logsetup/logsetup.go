// Package logsetup gives every program in this repository one log format:
// log/slog's text format, a line per record,
//
//	time=2026-11-18T12:58:47.123Z level=INFO msg="Saved thread reminders" guild_id=42 count=11
//
// so a level, a message and its fields can be picked out of any program's
// output the same way. Without it slog's default writes the date and level
// as bare words ("2026/11/18 12:58:47 INFO Saved ...") and the standard log
// package writes no level at all.
package logsetup

import (
	"io"
	"log/slog"
	"os"
)

// Init makes that format the program's default, on stderr. Call it first in
// main: a logger taken from slog.Default() before it keeps the old format.
// Lines written with the standard log package follow too, as level=INFO.
func Init() {
	slog.SetDefault(New(os.Stderr))
}

// New is a logger in that format writing to w.
func New(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, nil))
}

// Fatal logs at error level and exits 1: log.Fatal's job, with a level.
func Fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}
