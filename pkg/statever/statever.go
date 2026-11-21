// Package statever versions the state the bots persist: the JSON objects in
// S3 and sprobot's cache files. A build reads state up to Current and
// refuses anything newer, unread and untouched, because it can't know what
// a newer build changed: decoding newer state as its own could drop fields,
// and saving it back would destroy them.
//
// A document's version is its top-level "version" key, which comes first
// when present. One without it is version 1: every document from before
// versions, and those still written without one (see Stamp).
package statever

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Current is the state layout this build reads and writes.
//
// Bump it for any change an older build would misread, or would destroy by
// rewriting the document: a field removed, renamed or given another type,
// or a key's or a value's meaning changed. encoding/json drops unknown
// fields on load, so an older build's first save deletes every field it
// doesn't know; a new field needs no bump only if losing it costs no more
// than starting without it. A build that bumps it must also write the
// version into the documents still written without one (stickies, thread
// reminders and the cache files, whose shape can't carry it yet without
// breaking the builds from before versions).
const Current = 1

// NewerError is state written by a newer build. It is never treated as
// corrupt: nothing backs it up, replaces it or saves over it.
type NewerError struct {
	Name    string
	Version int
}

func (e *NewerError) Error() string {
	return fmt.Sprintf("%s is state version %d, newer than this build's %d: a newer build wrote it, so this one won't read or change it. Deploy that build (or a later one), or restore %s from before it", e.Name, e.Version, Current, e.Name)
}

// ErrCorrupt is a document that isn't JSON, so has no version to read.
var ErrCorrupt = errors.New("not valid JSON")

// IsNewer reports whether err is or wraps a *NewerError.
func IsNewer(err error) bool {
	var n *NewerError
	return errors.As(err, &n)
}

// Of returns the version of the JSON document data, named name in errors:
// its top-level "version", or 1 if it has none or isn't an object. It
// returns a *NewerError for a version above Current, ErrCorrupt (wrapped)
// for a document that isn't JSON, and another error for a version that isn't
// a positive integer.
func Of(name string, data []byte) (int, error) {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		if !json.Valid(data) {
			return 0, fmt.Errorf("%s: %w", name, ErrCorrupt)
		}
		return 1, nil // an array, or null: no version
	}
	// A map, not a struct: encoding/json matches struct fields regardless
	// of case, and only "version" itself is the version.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return 0, fmt.Errorf("%s: %w: %w", name, ErrCorrupt, err)
	}
	raw, ok := keys["version"]
	if !ok {
		return 1, nil
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil || v < 1 {
		return 0, fmt.Errorf("%s has version %s, not a positive integer", name, raw)
	}
	if v > Current {
		return v, &NewerError{Name: name, Version: v}
	}
	return v, nil
}

// Stamp returns the JSON object data with "version": Current as its first
// key. Builds from before versions ignore the key, so only documents that
// are JSON objects of fields (not maps keyed by ID, nor arrays) are stamped.
func Stamp(data []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return nil, errors.New("statever: only a JSON object can carry a version")
	}
	rest := bytes.TrimLeft(trimmed[1:], " \t\r\n")
	out := fmt.Appendf(nil, `{"version":%d`, Current)
	if rest[0] != '}' {
		out = append(out, ',')
	}
	return append(out, rest...), nil
}

// UnmarshalMap decodes a JSON object of IDs to values, leaving out its
// "version" key. Check the version with Of first.
func UnmarshalMap[V any](data []byte) (map[string]V, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	delete(raw, "version")
	out := make(map[string]V, len(raw))
	for k, v := range raw {
		var val V
		if err := json.Unmarshal(v, &val); err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = val
	}
	return out, nil
}
