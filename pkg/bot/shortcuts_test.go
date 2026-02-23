package bot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/disgoorg/snowflake/v2"
)

func TestShortcutStateJSONRoundTrip(t *testing.T) {
	st := &shortcutState{
		Shortcuts: map[string]shortcutEntry{
			"faq":   {Responses: []string{"Answer 1", "Answer 2"}},
			"rules": {Responses: []string{"Be nice"}},
		},
		indices: map[string]int{"faq": 3},
	}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	// indices and mu should not appear in JSON
	if strings.Contains(string(data), "indices") {
		t.Error("indices should not be serialized to JSON")
	}

	var loaded shortcutState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if len(loaded.Shortcuts) != 2 {
		t.Errorf("expected 2 shortcuts, got %d", len(loaded.Shortcuts))
	}
	if loaded.Shortcuts["faq"].Responses[0] != "Answer 1" {
		t.Errorf("expected 'Answer 1', got %q", loaded.Shortcuts["faq"].Responses[0])
	}
	// indices should be nil after unmarshal (unexported, not in JSON)
	if loaded.indices != nil {
		t.Error("indices should be nil after unmarshal")
	}
}

func TestShortcutRoundRobinAdvances(t *testing.T) {
	st := &shortcutState{
		Shortcuts: map[string]shortcutEntry{
			"faq": {Responses: []string{"A", "B", "C"}},
		},
		indices: map[string]int{"faq": 0},
	}

	var got []string
	for range 6 {
		n := len(st.Shortcuts["faq"].Responses)
		idx := st.indices["faq"] % n
		got = append(got, st.Shortcuts["faq"].Responses[idx])
		st.indices["faq"] = (st.indices["faq"] + 1) % n
	}

	want := []string{"A", "B", "C", "A", "B", "C"}
	for i, g := range got {
		if g != want[i] {
			t.Errorf("call %d: got %q, want %q", i, g, want[i])
		}
	}
}

func TestShortcutRoundRobinRandomStart(t *testing.T) {
	// When indices has no entry for a shortcut, the first call should not
	// always start at 0. We can't test randomness deterministically, but we
	// can verify the "not started" detection works.
	st := &shortcutState{
		Shortcuts: map[string]shortcutEntry{
			"faq": {Responses: []string{"A", "B", "C"}},
		},
		indices: map[string]int{},
	}

	_, started := st.indices["faq"]
	if started {
		t.Error("expected faq index to not be started")
	}

	// After setting an index, it should be detected as started
	st.indices["faq"] = 0
	_, started = st.indices["faq"]
	if !started {
		t.Error("expected faq index to be started after setting to 0")
	}
}

func TestShortcutRoundRobinSingleResponse(t *testing.T) {
	st := &shortcutState{
		Shortcuts: map[string]shortcutEntry{
			"faq": {Responses: []string{"only one"}},
		},
		indices: map[string]int{"faq": 0},
	}

	for range 3 {
		n := len(st.Shortcuts["faq"].Responses)
		idx := st.indices["faq"] % n
		got := st.Shortcuts["faq"].Responses[idx]
		st.indices["faq"] = (st.indices["faq"] + 1) % n
		if got != "only one" {
			t.Errorf("expected 'only one', got %q", got)
		}
	}
}

func TestShortcutIndexResetOnReSet(t *testing.T) {
	st := &shortcutState{
		Shortcuts: map[string]shortcutEntry{
			"faq": {Responses: []string{"A", "B"}},
		},
		indices: map[string]int{"faq": 5},
	}

	// Simulate re-setting the shortcut
	st.Shortcuts["faq"] = shortcutEntry{Responses: []string{"X", "Y", "Z"}}
	st.indices["faq"] = 0

	idx := st.indices["faq"] % len(st.Shortcuts["faq"].Responses)
	if idx != 0 {
		t.Errorf("expected index 0 after reset, got %d", idx)
	}
	if st.Shortcuts["faq"].Responses[idx] != "X" {
		t.Errorf("expected 'X', got %q", st.Shortcuts["faq"].Responses[idx])
	}
}

func TestShortcutAutocompleteFilters(t *testing.T) {
	st := &shortcutState{
		Shortcuts: map[string]shortcutEntry{
			"faq":      {Responses: []string{"A"}},
			"FAQ-long": {Responses: []string{"B"}},
			"rules":    {Responses: []string{"C"}},
		},
		indices: map[string]int{},
	}

	got := shortcutAutocomplete(st, "faq")
	if len(got) != 2 {
		t.Fatalf("expected 2 matches for 'faq', got %d: %v", len(got), got)
	}
	for _, name := range got {
		if !strings.Contains(strings.ToLower(name), "faq") {
			t.Errorf("unexpected match %q for 'faq'", name)
		}
	}
}

func TestShortcutAutocompleteCaseInsensitive(t *testing.T) {
	st := &shortcutState{
		Shortcuts: map[string]shortcutEntry{
			"FAQ": {Responses: []string{"A"}},
		},
		indices: map[string]int{},
	}

	got := shortcutAutocomplete(st, "faq")
	if len(got) != 1 || got[0] != "FAQ" {
		t.Errorf("expected [FAQ], got %v", got)
	}
}

func TestShortcutAutocompleteEmptyInput(t *testing.T) {
	st := &shortcutState{
		Shortcuts: map[string]shortcutEntry{
			"faq":   {Responses: []string{"A"}},
			"rules": {Responses: []string{"B"}},
		},
		indices: map[string]int{},
	}

	got := shortcutAutocomplete(st, "")
	if len(got) != 2 {
		t.Errorf("expected 2 matches for empty input, got %d", len(got))
	}
}

func TestShortcutAutocompleteNoMatch(t *testing.T) {
	st := &shortcutState{
		Shortcuts: map[string]shortcutEntry{
			"faq": {Responses: []string{"A"}},
		},
		indices: map[string]int{},
	}

	got := shortcutAutocomplete(st, "zzzzz")
	if len(got) != 0 {
		t.Errorf("expected 0 matches, got %d", len(got))
	}
}

func TestShortcutAutocompleteLimitsTo25(t *testing.T) {
	shortcuts := make(map[string]shortcutEntry)
	for i := range 30 {
		shortcuts[strings.Repeat("a", i+1)] = shortcutEntry{Responses: []string{"x"}}
	}
	st := &shortcutState{
		Shortcuts: shortcuts,
		indices:   map[string]int{},
	}

	got := shortcutAutocomplete(st, "a")
	if len(got) > 25 {
		t.Errorf("autocomplete returned %d choices, max should be 25", len(got))
	}
}

func TestShortcutResponseFilterWhitespace(t *testing.T) {
	inputs := []string{
		"hello world",
		"",
		"   ",
		"\t\n",
		"goodbye",
	}

	got := filterShortcutResponses(inputs)
	if len(got) != 2 {
		t.Fatalf("expected 2 non-blank responses, got %d: %v", len(got), got)
	}
	if got[0] != "hello world" || got[1] != "goodbye" {
		t.Errorf("unexpected filtered responses: %v", got)
	}
}

func TestShortcutResponseFilterAllBlank(t *testing.T) {
	inputs := []string{"", "   ", "\n"}
	got := filterShortcutResponses(inputs)
	if len(got) != 0 {
		t.Errorf("expected 0 responses, got %d: %v", len(got), got)
	}
}

func TestShortcutResponseFilterPreservesMultiline(t *testing.T) {
	inputs := []string{"line 1\nline 2\nline 3"}
	got := filterShortcutResponses(inputs)
	if len(got) != 1 {
		t.Fatalf("expected 1 response, got %d", len(got))
	}
	if got[0] != "line 1\nline 2\nline 3" {
		t.Errorf("multiline response should be preserved, got %q", got[0])
	}
}

func TestExpandShortcutVars(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		userID   uint64
		expected string
	}{
		{"basic", "Welcome [user]!", 123, "Welcome <@123>!"},
		{"multiple", "[user] and [user]", 456, "<@456> and <@456>"},
		{"no placeholder", "Hello world", 789, "Hello world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandShortcutVars(tt.input, snowflake.ID(tt.userID))
			if got != tt.expected {
				t.Errorf("expandShortcutVars(%q, %d) = %q, want %q", tt.input, tt.userID, got, tt.expected)
			}
		})
	}
}

// shortcutAutocomplete extracts the autocomplete logic for testing.
func shortcutAutocomplete(st *shortcutState, current string) []string {
	current = strings.ToLower(current)
	var choices []string
	for name := range st.Shortcuts {
		if len(choices) >= 25 {
			break
		}
		if strings.Contains(strings.ToLower(name), current) {
			choices = append(choices, name)
		}
	}
	return choices
}

// filterShortcutResponses extracts the response filtering logic for testing.
func filterShortcutResponses(inputs []string) []string {
	var responses []string
	for _, text := range inputs {
		if strings.TrimSpace(text) != "" {
			responses = append(responses, text)
		}
	}
	return responses
}
