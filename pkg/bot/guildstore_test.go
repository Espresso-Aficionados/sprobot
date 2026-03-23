package bot

import (
	"sync"
	"testing"

	"github.com/disgoorg/snowflake/v2"
)

type testState struct {
	mu    sync.Mutex
	Value string `json:"value"`
}

func newTestStore() *guildStateStore[testState] {
	return &guildStateStore[testState]{
		s3Key:    "test",
		states:   make(map[snowflake.ID]*testState),
		getMu:    func(st *testState) *sync.Mutex { return &st.mu },
		newState: func() *testState { return &testState{} },
	}
}

func TestGuildStateStoreGetSet(t *testing.T) {
	store := newTestStore()

	var guildID snowflake.ID = 123

	if got := store.get(guildID); got != nil {
		t.Error("expected nil for missing guild")
	}

	st := &testState{Value: "hello"}
	store.set(guildID, st)

	got := store.get(guildID)
	if got == nil {
		t.Fatal("expected non-nil after set")
	}
	if got.Value != "hello" {
		t.Errorf("Value = %q, want %q", got.Value, "hello")
	}
}

func TestGuildStateStoreEach(t *testing.T) {
	store := newTestStore()
	store.set(100, &testState{Value: "a"})
	store.set(200, &testState{Value: "b"})

	seen := make(map[snowflake.ID]string)
	store.each(func(guildID snowflake.ID, st *testState) {
		seen[guildID] = st.Value
	})

	if len(seen) != 2 {
		t.Fatalf("each visited %d entries, want 2", len(seen))
	}
	if seen[100] != "a" {
		t.Errorf("guild 100 = %q, want %q", seen[100], "a")
	}
	if seen[200] != "b" {
		t.Errorf("guild 200 = %q, want %q", seen[200], "b")
	}
}

func TestGuildStateStoreEachEmpty(t *testing.T) {
	store := newTestStore()
	count := 0
	store.each(func(_ snowflake.ID, _ *testState) {
		count++
	})
	if count != 0 {
		t.Errorf("each on empty store visited %d entries", count)
	}
}

func TestGuildStateStoreSetOverwrite(t *testing.T) {
	store := newTestStore()
	var guildID snowflake.ID = 100

	store.set(guildID, &testState{Value: "first"})
	store.set(guildID, &testState{Value: "second"})

	got := store.get(guildID)
	if got.Value != "second" {
		t.Errorf("Value = %q, want %q", got.Value, "second")
	}
}
