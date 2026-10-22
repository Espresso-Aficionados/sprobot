package bot

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/sprobot"
	"github.com/sadbox/sprobot/pkg/testutil"
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

func newShortcutState(shortcuts map[string]shortcutEntry) *shortcutState {
	return &shortcutState{Shortcuts: shortcuts, indices: map[string]int{}}
}

func TestShortcutNextRotates(t *testing.T) {
	st := newShortcutState(map[string]shortcutEntry{"faq": {Responses: []string{"A", "B", "C"}}})
	st.indices["faq"] = 0

	var got []string
	for range 6 {
		r, ok := st.next("faq")
		if !ok {
			t.Fatal("next(faq) found nothing")
		}
		got = append(got, r)
	}
	if want := []string{"A", "B", "C", "A", "B", "C"}; !slices.Equal(got, want) {
		t.Errorf("responses = %v, want %v", got, want)
	}
}

func TestShortcutNextStartsAnywhereThenCycles(t *testing.T) {
	st := newShortcutState(map[string]shortcutEntry{"faq": {Responses: []string{"A", "B", "C"}}})

	var got []string
	for range 4 {
		r, _ := st.next("faq")
		got = append(got, r)
	}
	first3 := slices.Clone(got[:3])
	slices.Sort(first3)
	if !slices.Equal(first3, []string{"A", "B", "C"}) || got[3] != got[0] {
		t.Errorf("responses = %v, want each response once per cycle", got)
	}
}

func TestShortcutNextMissing(t *testing.T) {
	st := newShortcutState(map[string]shortcutEntry{"empty": {}})
	for _, name := range []string{"nope", "empty"} {
		if _, ok := st.next(name); ok {
			t.Errorf("next(%q) found a response", name)
		}
	}
}

func TestShortcutSetRestartsRotation(t *testing.T) {
	st := newShortcutState(map[string]shortcutEntry{"faq": {Responses: []string{"A", "B"}}})
	st.indices["faq"] = 1

	replaced, ok := st.set("faq", []string{"X", "Y", "Z"})
	if !ok || !slices.Equal(replaced, []string{"A", "B"}) {
		t.Errorf("set = %v, %v; want the old responses", replaced, ok)
	}
	if r, _ := st.next("faq"); r != "X" {
		t.Errorf("first response after set = %q, want X", r)
	}
}

func TestShortcutSetLimit(t *testing.T) {
	shortcuts := make(map[string]shortcutEntry)
	for i := range maxShortcutsPerGuild {
		shortcuts[fmt.Sprint(i)] = shortcutEntry{Responses: []string{"x"}}
	}
	st := newShortcutState(shortcuts)

	if _, ok := st.set("new", []string{"x"}); ok {
		t.Error("added a shortcut past the limit")
	}
	if _, ok := st.set("0", []string{"y"}); !ok {
		t.Error("refused to replace an existing shortcut at the limit")
	}
}

func TestShortcutRemove(t *testing.T) {
	st := newShortcutState(map[string]shortcutEntry{"faq": {Responses: []string{"A"}}})
	if removed, ok := st.remove("faq"); !ok || !slices.Equal(removed, []string{"A"}) {
		t.Errorf("remove = %v, %v", removed, ok)
	}
	if _, ok := st.remove("faq"); ok {
		t.Error("removed a shortcut twice")
	}
}

func TestShortcutChoices(t *testing.T) {
	shortcuts := map[string]shortcutEntry{
		"faq":      {Responses: []string{"A"}},
		"FAQ-long": {Responses: []string{"B"}},
		"rules":    {Responses: []string{"C"}},
	}
	if got := shortcutChoices(shortcuts, "faq"); !slices.Equal(got, []string{"FAQ-long", "faq"}) {
		t.Errorf("choices for faq = %v, want both, sorted", got)
	}
	if got := shortcutChoices(shortcuts, "zzz"); len(got) != 0 {
		t.Errorf("choices for zzz = %v, want none", got)
	}
}

func TestShortcutChoicesSortedBeforeCap(t *testing.T) {
	shortcuts := make(map[string]shortcutEntry)
	var all []string
	for i := range 30 {
		name := fmt.Sprintf("s%02d", i)
		shortcuts[name] = shortcutEntry{Responses: []string{"x"}}
		all = append(all, name)
	}

	// Map order varies between runs; the choices must not.
	for range 5 {
		if got := shortcutChoices(shortcuts, ""); !slices.Equal(got, all[:autocompleteMax]) {
			t.Fatalf("choices = %v, want the first %d names in order", got, autocompleteMax)
		}
	}
}

func TestNonBlankResponses(t *testing.T) {
	got := nonBlankResponses([]string{"hello world", "", "   ", "\t\n", "line 1\nline 2"})
	if want := []string{"hello world", "line 1\nline 2"}; !slices.Equal(got, want) {
		t.Errorf("responses = %q, want %q", got, want)
	}
}

func TestShortcutModalTitle(t *testing.T) {
	utf16Units := func(s string) int { return len(utf16.Encode([]rune(s))) }
	tests := []struct {
		name string
		want string
	}{
		{"faq", "Set shortcut: faq"},
		{strings.Repeat("a", 31), "Set shortcut: " + strings.Repeat("a", 31)},
		{strings.Repeat("a", 80), "Set shortcut: " + strings.Repeat("a", 30) + "…"},
		// Emoji take two UTF-16 units each.
		{strings.Repeat("☕️🫘", 10), ""},
	}
	for _, tt := range tests {
		got := shortcutModalTitle(tt.name)
		if n := utf16Units(got); n > modalTitleMax {
			t.Errorf("title %q is %d units, over %d", got, n, modalTitleMax)
		}
		if tt.want != "" && got != tt.want {
			t.Errorf("shortcutModalTitle(%q) = %q, want %q", tt.name, got, tt.want)
		}
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

func TestIsImageURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"https://example.com/photo.png", true},
		{"https://example.com/photo.jpg", true},
		{"https://example.com/photo.jpeg", true},
		{"https://example.com/photo.gif", true},
		{"https://example.com/photo.webp", true},
		{"https://example.com/photo.PNG", true},
		{"https://cdn.example.com/img.jpg?width=800", true},
		{"https://cdn.example.com/img.png#fragment", true},
		{"http://example.com/photo.jpg", true},
		{"  https://example.com/photo.png  ", true},
		{"https://example.com/page", false},
		{"https://example.com/photo.pdf", false},
		{"not a url at all", false},
		{"https://example.com/a.png and more text", false},
		{"https://example.com/a.png\nhttps://example.com/b.png", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isImageURL(tt.input); got != tt.want {
				t.Errorf("isImageURL(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// Saved shortcut files: 30 random characters plus an extension.
const (
	shortcutKeyA = "shortcut_files/10/" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png"
	shortcutKeyB = "shortcut_files/10/" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png"
)

func TestStoredFileKey(t *testing.T) {
	b, _, _ := newStubBotWithS3(t)
	web := sprobot.WebEndpoint + "files/shortcut/10/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png"
	bucket := strings.TrimSuffix(b.S3.Origin(), "/") + "/test-bucket/" + shortcutKeyA

	tests := []struct {
		ref    string
		want   string
		wantOK bool
	}{
		{shortcutKeyA, shortcutKeyA, true},
		{web, shortcutKeyA, true},
		{web + "/view", shortcutKeyA, true},
		{web + "?v=1", shortcutKeyA, true},
		{bucket, shortcutKeyA, true},
		{"https://elsewhere.example/files/shortcut/10/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png", "", false},
		{"https://example.com/cat.png", "", false},
		{"Be nice", "", false},
	}
	for _, tt := range tests {
		got, ok := b.storedFileKey(tt.ref)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("storedFileKey(%q) = %q, %v; want %q, %v", tt.ref, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestUnusedShortcutFiles(t *testing.T) {
	b, _, _ := newStubBotWithS3(t)
	modFile := "mod_files/10/cccccccccccccccccccccccccccccc.png"
	otherGuild := "shortcut_files/11/dddddddddddddddddddddddddddddd.png"
	old := []string{shortcutKeyA, shortcutKeyB, modFile, otherGuild, "plain text", shortcutKeyA}
	// Another shortcut still links to B through its web link.
	remaining := []string{"hello", sprobot.WebEndpoint + "files/shortcut/10/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png"}

	got := unusedShortcutFiles(old, remaining, "10", b.storedFileKey)
	if want := []string{shortcutKeyA}; !slices.Equal(got, want) {
		t.Errorf("unused = %v, want %v", got, want)
	}
}

// newShortcutBot returns a bot whose guild 10 has shortcuts a (file A),
// b (file B) and c (a link to file B).
func newShortcutBot(t *testing.T) (*Bot, *discordStub, *testutil.FakeS3) {
	t.Helper()
	b, stub, fake := newStubBotWithS3(t)
	b.shortcuts = newGuildStateStore(b.S3, b.Log, "shortcuts",
		func() *shortcutState { return newShortcutState(map[string]shortcutEntry{}) },
		func(st *shortcutState) *sync.Mutex { return &st.mu })
	b.shortcuts.set(10, newShortcutState(map[string]shortcutEntry{
		"a": {Responses: []string{shortcutKeyA}},
		"b": {Responses: []string{shortcutKeyB}},
		"c": {Responses: []string{sprobot.WebEndpoint + "files/shortcut/10/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.png"}},
	}))
	for _, key := range []string{shortcutKeyA, shortcutKeyB} {
		fake.Objects["/test-bucket/"+key] = []byte("img")
	}
	return b, stub, fake
}

func removeShortcutData(name string) string {
	return `{"id":"100","name":"config","type":1,"options":[{"name":"shortcuts","type":2,"options":[` +
		`{"name":"remove","type":1,"options":[{"name":"shortcut","type":3,"value":` + jsonString(name) + `}]}]}]}`
}

func objectExists(fake *testutil.FakeS3, key string) bool {
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	_, ok := fake.Objects["/test-bucket/"+key]
	return ok
}

func TestShortcutRemoveDeletesUnusedFiles(t *testing.T) {
	b, _, fake := newShortcutBot(t)

	e, _ := commandEvent(t, b.Client, removeShortcutData("a"))
	b.handleShortcutConfigRemove(e)
	if objectExists(fake, shortcutKeyA) {
		t.Error("file A outlived the only shortcut using it")
	}

	e, _ = commandEvent(t, b.Client, removeShortcutData("b"))
	b.handleShortcutConfigRemove(e)
	if !objectExists(fake, shortcutKeyB) {
		t.Error("deleted file B although shortcut c still links to it")
	}
}

func TestShortcutReplaceDeletesUnusedFiles(t *testing.T) {
	b, _, fake := newShortcutBot(t)

	e, _ := modalEvent(t, b.Client, "sconfig_set_a",
		`[{"type":1,"components":[{"type":4,"custom_id":"response_0","value":"text now"}]}]`)
	b.handleShortcutConfigSetModal(e)

	if objectExists(fake, shortcutKeyA) {
		t.Error("the replaced response's file was kept")
	}
	st := b.shortcuts.get(10)
	if got := st.Shortcuts["a"].Responses; !slices.Equal(got, []string{"text now"}) {
		t.Errorf("responses = %v, want the new text", got)
	}
}

// setShortcutModal builds a modal submit setting shortcut name to response.
func setShortcutModal(t *testing.T, b *Bot, name, response string) *events.ModalSubmitInteractionCreate {
	t.Helper()
	e, _ := modalEvent(t, b.Client, "sconfig_set_"+name,
		`[{"type":1,"components":[{"type":4,"custom_id":"response_0","value":`+jsonString(response)+`}]}]`)
	return e
}

// putsUnder returns the PUT requests made for keys under prefix.
func putsUnder(fake *testutil.FakeS3, prefix string) []string {
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	var out []string
	for _, r := range fake.Requests {
		if strings.HasPrefix(r, "PUT /test-bucket/"+prefix) {
			out = append(out, r)
		}
	}
	return out
}

func TestShortcutSetReusesOwnSavedFile(t *testing.T) {
	b, _, fake := newShortcutBot(t)

	b.handleShortcutConfigSetModal(setShortcutModal(t, b, "d", sprobot.WebEndpoint+"files/shortcut/10/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png"))

	if got := b.shortcuts.get(10).Shortcuts["d"].Responses; !slices.Equal(got, []string{shortcutKeyA}) {
		t.Errorf("responses = %v, want the bare key of the linked file", got)
	}
	if got := putsUnder(fake, "shortcut_files/"); len(got) != 0 {
		t.Errorf("stored the guild's own file again: %v", got)
	}
}

func TestShortcutSetCopiesFilesItDoesNotOwn(t *testing.T) {
	tests := []struct {
		name, key string
		web       bool // linked by its web route, else by its old bucket URL
	}{
		{"another guild's shortcut file", "shortcut_files/11/dddddddddddddddddddddddddddddd.png", true},
		{"a mod file of this guild", "mod_files/10/cccccccccccccccccccccccccccccc.gif", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, _, fake := newShortcutBot(t)
			fake.Objects["/test-bucket/"+tt.key] = []byte("theirs")
			link := b.S3.Origin() + "/test-bucket/" + tt.key
			if tt.web {
				link = b.S3.FileURL(tt.key)
			}

			b.handleShortcutConfigSetModal(setShortcutModal(t, b, "d", link))

			got := b.shortcuts.get(10).Shortcuts["d"].Responses
			if len(got) != 1 || got[0] == tt.key || !strings.HasPrefix(got[0], "shortcut_files/10/") || path.Ext(got[0]) != path.Ext(tt.key) {
				t.Fatalf("responses = %v, want a copy among guild 10's shortcut files", got)
			}
			fake.Mu.Lock()
			copied := string(fake.Objects["/test-bucket/"+got[0]])
			fake.Mu.Unlock()
			if copied != "theirs" {
				t.Errorf("copy holds %q, want the source's bytes", copied)
			}

			// Removing the shortcut deletes the copy, never the source.
			e, _ := commandEvent(t, b.Client, removeShortcutData("d"))
			b.handleShortcutConfigRemove(e)
			if objectExists(fake, got[0]) || !objectExists(fake, tt.key) {
				t.Errorf("after removal: copy exists %v, source exists %v; want only the source", objectExists(fake, got[0]), objectExists(fake, tt.key))
			}
		})
	}
}
