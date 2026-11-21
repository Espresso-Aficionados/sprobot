package statever

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestOf(t *testing.T) {
	for _, tc := range []struct {
		data    string
		want    int
		newer   bool
		corrupt bool
		bad     bool
	}{
		{data: `{"value":"x"}`, want: 1},                // from before versions
		{data: `{"version":1,"value":"x"}`, want: 1},    // stamped
		{data: `{"Version":5,"value":"x"}`, want: 1},    // only "version" itself counts
		{data: `{"123":{"channel_id":"123"}}`, want: 1}, // a map of IDs
		{data: `[{"id":"1"}]`, want: 1},                 // an array
		{data: `null`, want: 1},
		{data: `{"version":2}`, want: 2, newer: true},
		{data: `{"version":0}`, bad: true},
		{data: `{"version":"1"}`, bad: true},
		{data: `{"version":1.5}`, bad: true},
		{data: `{"version":null}`, bad: true},
		{data: `{"version":1`, corrupt: true},
		{data: `[1,`, corrupt: true},
		{data: ``, corrupt: true},
	} {
		v, err := Of("doc.json", []byte(tc.data))
		switch {
		case tc.newer:
			var n *NewerError
			if !errors.As(err, &n) || n.Version != tc.want || v != tc.want || !strings.Contains(err.Error(), "doc.json") {
				t.Errorf("Of(%s) = %d, %v; want a NewerError for %d", tc.data, v, err, tc.want)
			}
		case tc.corrupt:
			if !errors.Is(err, ErrCorrupt) {
				t.Errorf("Of(%s) = %d, %v; want ErrCorrupt", tc.data, v, err)
			}
		case tc.bad:
			if err == nil || IsNewer(err) || errors.Is(err, ErrCorrupt) {
				t.Errorf("Of(%s) = %d, %v; want an invalid-version error", tc.data, v, err)
			}
		default:
			if err != nil || v != tc.want {
				t.Errorf("Of(%s) = %d, %v; want %d", tc.data, v, err, tc.want)
			}
		}
	}
}

func TestStamp(t *testing.T) {
	for in, want := range map[string]string{
		`{"value":"x"}`:      `{"version":1,"value":"x"}`,
		`{}`:                 `{"version":1}`,
		" { \"a\":1}\n":      `{"version":1,"a":1}`,
		`{"value":{"b":[]}}`: `{"version":1,"value":{"b":[]}}`,
	} {
		got, err := Stamp([]byte(in))
		if err != nil || string(got) != want {
			t.Errorf("Stamp(%q) = %s, %v; want %s", in, got, err, want)
		}
		if v, err := Of("doc", got); err != nil || v != Current {
			t.Errorf("Of(Stamp(%q)) = %d, %v", in, v, err)
		}
	}
	for _, in := range []string{`[]`, `null`, ``, `"x"`} {
		if _, err := Stamp([]byte(in)); err == nil {
			t.Errorf("Stamp(%q) succeeded; only objects can carry a version", in)
		}
	}
}

// A stamped object still decodes in a build from before versions: it
// ignores the unknown key.
func TestStampedObjectDecodesWithoutVersions(t *testing.T) {
	type old struct {
		Value string `json:"value"`
	}
	data, _ := Stamp([]byte(`{"value":"x"}`))
	var o old
	if err := json.Unmarshal(data, &o); err != nil || o.Value != "x" {
		t.Fatalf("old decoder: %+v, %v", o, err)
	}
}

// A map keyed by ID can't be stamped yet: a build from before versions
// decodes every key as an entry and fails on "version". That's why stickies
// and thread reminders are still written without one.
func TestStampedMapBreaksDecodersWithoutVersions(t *testing.T) {
	type entry struct {
		ChannelID string `json:"channel_id"`
	}
	data := []byte(`{"version":1,"123":{"channel_id":"123"}}`)
	var old map[string]*entry
	if err := json.Unmarshal(data, &old); err == nil {
		t.Fatal("a pre-version decoder read a stamped map; stickies and reminders could be stamped now")
	}

	got, err := UnmarshalMap[*entry](data)
	if err != nil || len(got) != 1 || got["123"].ChannelID != "123" {
		t.Fatalf("UnmarshalMap = %v, %v", got, err)
	}
	got, err = UnmarshalMap[*entry]([]byte(`{"123":{"channel_id":"123"}}`))
	if err != nil || len(got) != 1 {
		t.Fatalf("UnmarshalMap(unversioned) = %v, %v", got, err)
	}
}

func TestReportPrint(t *testing.T) {
	newer := func(r *Report) { r.Add("b.json", []byte(`{"version":7}`), nil) }
	unreadable := func(r *Report) { r.Add("c.json", nil, errors.New("AccessDenied")) }
	ok := func(r *Report) {
		r.Missing("a.json")
		r.Add("d.json", []byte(`{"x":1}`), nil)
	}
	for _, tc := range []struct {
		name  string
		fill  []func(*Report)
		code  int
		check string
	}{
		{"nothing", nil, 0, `{"store_version":0,"build_version":1,"pending":[],"unknown":[],"compatible":true,"unreadable":[],"documents":[]}`},
		{"ok", []func(*Report){ok}, 0, `{"store_version":1,"build_version":1,"pending":[],"unknown":[],"compatible":true,"unreadable":[],"documents":[{"name":"a.json","exists":false},{"name":"d.json","exists":true,"version":1}]}`},
		{"unreadable", []func(*Report){ok, unreadable}, 1, ""},
		{"newer", []func(*Report){ok, newer}, 3, ""},
		// A newer document decides it: this build would refuse to start
		// whatever else couldn't be read.
		{"newer and unreadable", []func(*Report){unreadable, newer}, 3, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Report{Program: "testbot"}
			for _, f := range tc.fill {
				f(&r)
			}
			var out, errOut bytes.Buffer
			if code := r.Print(&out, &errOut, false); code != tc.code {
				t.Errorf("text: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, tc.code, out.String(), errOut.String())
			}
			out.Reset()
			errOut.Reset()
			if code := r.Print(&out, &errOut, true); code != tc.code {
				t.Errorf("json: exit %d, want %d", code, tc.code)
			}
			var c check
			if err := json.Unmarshal(out.Bytes(), &c); err != nil {
				t.Fatalf("stdout isn't one JSON object: %v\n%s", err, out.String())
			}
			if c.Compatible != (tc.code != 3) {
				t.Errorf("compatible = %v with exit %d", c.Compatible, tc.code)
			}
			if tc.check != "" && strings.TrimSpace(out.String()) != tc.check {
				t.Errorf("json:\n%s\nwant\n%s", out.String(), tc.check)
			}
			if tc.code == 3 && (len(c.Unknown) != 1 || c.Unknown[0] != 7 || !strings.Contains(errOut.String(), "newer than this build's 1")) {
				t.Errorf("newer: unknown %v, stderr:\n%s", c.Unknown, errOut.String())
			}
			if tc.code == 1 && (len(c.Unreadable) != 1 || !strings.Contains(errOut.String(), "c.json: AccessDenied")) {
				t.Errorf("unreadable: %v, stderr:\n%s", c.Unreadable, errOut.String())
			}
		})
	}
}
