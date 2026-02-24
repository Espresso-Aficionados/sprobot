package bot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

func TestTopReactionsStateJSONRoundTrip(t *testing.T) {
	st := &topReactionsState{
		Settings: topReactionsSettings{
			OutputChannelID:  123456,
			WindowMinutes:    720,
			FrequencyMinutes: 360,
			Count:            5,
			Blacklist:        []snowflake.ID{111, 222},
		},
		Messages: map[snowflake.ID]trackedMessage{
			999: {ChannelID: 100, AuthorID: 200, Count: 3},
		},
		LastPost: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	// mu should not appear in JSON
	if strings.Contains(string(data), "mu") {
		t.Error("mu should not be serialized to JSON")
	}

	var loaded topReactionsState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.Settings.OutputChannelID != 123456 {
		t.Errorf("expected OutputChannelID 123456, got %d", loaded.Settings.OutputChannelID)
	}
	if loaded.Settings.WindowMinutes != 720 {
		t.Errorf("expected WindowMinutes 720, got %d", loaded.Settings.WindowMinutes)
	}
	if loaded.Settings.FrequencyMinutes != 360 {
		t.Errorf("expected FrequencyMinutes 360, got %d", loaded.Settings.FrequencyMinutes)
	}
	if loaded.Settings.Count != 5 {
		t.Errorf("expected Count 5, got %d", loaded.Settings.Count)
	}
	if len(loaded.Settings.Blacklist) != 2 {
		t.Errorf("expected 2 blacklist entries, got %d", len(loaded.Settings.Blacklist))
	}
	if len(loaded.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(loaded.Messages))
	}
	tm := loaded.Messages[999]
	if tm.Count != 3 || tm.ChannelID != 100 || tm.AuthorID != 200 {
		t.Errorf("unexpected message data: %+v", tm)
	}
	if !loaded.LastPost.Equal(st.LastPost) {
		t.Errorf("expected LastPost %v, got %v", st.LastPost, loaded.LastPost)
	}
}

func TestPruneOldMessages(t *testing.T) {
	now := time.Now()
	// Create snowflakes: one recent, one old
	recentID := snowflake.New(now.Add(-30 * time.Minute))
	oldID := snowflake.New(now.Add(-25 * time.Hour))

	st := &topReactionsState{
		Settings: topReactionsSettings{WindowMinutes: 1440}, // 24h
		Messages: map[snowflake.ID]trackedMessage{
			recentID: {ChannelID: 1, AuthorID: 2, Count: 5},
			oldID:    {ChannelID: 1, AuthorID: 3, Count: 10},
		},
	}

	pruneOldMessages(st, now)

	if len(st.Messages) != 1 {
		t.Errorf("expected 1 message after prune, got %d", len(st.Messages))
	}
	if _, ok := st.Messages[recentID]; !ok {
		t.Error("recent message should have been kept")
	}
	if _, ok := st.Messages[oldID]; ok {
		t.Error("old message should have been pruned")
	}
}

func TestPruneOldMessagesDefaultWindow(t *testing.T) {
	now := time.Now()
	oldID := snowflake.New(now.Add(-25 * time.Hour))

	st := &topReactionsState{
		Settings: topReactionsSettings{}, // defaults to 1440 min
		Messages: map[snowflake.ID]trackedMessage{
			oldID: {Count: 1},
		},
	}

	pruneOldMessages(st, now)

	if len(st.Messages) != 0 {
		t.Error("old message should have been pruned with default window")
	}
}

func TestSettingsEffectiveDefaults(t *testing.T) {
	s := &topReactionsSettings{}

	if s.effectiveWindow() != defaultWindowMinutes {
		t.Errorf("expected default window %d, got %d", defaultWindowMinutes, s.effectiveWindow())
	}
	if s.effectiveFrequency() != defaultFrequencyMinutes {
		t.Errorf("expected default frequency %d, got %d", defaultFrequencyMinutes, s.effectiveFrequency())
	}
	if s.effectiveCount() != defaultTopCount {
		t.Errorf("expected default count %d, got %d", defaultTopCount, s.effectiveCount())
	}
}

func TestSettingsEffectiveCustom(t *testing.T) {
	s := &topReactionsSettings{
		WindowMinutes:    120,
		FrequencyMinutes: 60,
		Count:            3,
	}

	if s.effectiveWindow() != 120 {
		t.Errorf("expected window 120, got %d", s.effectiveWindow())
	}
	if s.effectiveFrequency() != 60 {
		t.Errorf("expected frequency 60, got %d", s.effectiveFrequency())
	}
	if s.effectiveCount() != 3 {
		t.Errorf("expected count 3, got %d", s.effectiveCount())
	}
}

func TestIsBlacklisted(t *testing.T) {
	s := &topReactionsSettings{
		Blacklist: []snowflake.ID{100, 200, 300},
	}

	if !s.isBlacklisted(200) {
		t.Error("200 should be blacklisted")
	}
	if s.isBlacklisted(999) {
		t.Error("999 should not be blacklisted")
	}
}

func TestIsBlacklistedEmpty(t *testing.T) {
	s := &topReactionsSettings{}

	if s.isBlacklisted(100) {
		t.Error("empty blacklist should never match")
	}
}

func TestGetTopReactionsConfig(t *testing.T) {
	devCfg := getTopReactionsConfig("dev")
	if devCfg == nil {
		t.Fatal("expected dev config")
	}
	if _, ok := devCfg[1013566342345019512]; !ok {
		t.Error("expected dev guild in config")
	}

	prodCfg := getTopReactionsConfig("prod")
	if prodCfg == nil {
		t.Fatal("expected prod config")
	}
	if _, ok := prodCfg[726985544038612993]; !ok {
		t.Error("expected prod guild in config")
	}

	if getTopReactionsConfig("other") != nil {
		t.Error("expected nil for unknown env")
	}
}

func TestIntPtr(t *testing.T) {
	p := intPtr(42)
	if *p != 42 {
		t.Errorf("expected 42, got %d", *p)
	}
}

func TestTrackedMessageIncrement(t *testing.T) {
	st := &topReactionsState{
		Messages: make(map[snowflake.ID]trackedMessage),
	}

	msgID := snowflake.ID(12345)

	// Simulate add
	tm := st.Messages[msgID]
	tm.ChannelID = 100
	tm.AuthorID = 200
	tm.Count++
	st.Messages[msgID] = tm

	if st.Messages[msgID].Count != 1 {
		t.Errorf("expected count 1, got %d", st.Messages[msgID].Count)
	}

	// Simulate second add
	tm = st.Messages[msgID]
	tm.Count++
	st.Messages[msgID] = tm

	if st.Messages[msgID].Count != 2 {
		t.Errorf("expected count 2, got %d", st.Messages[msgID].Count)
	}
}

func TestTrackedMessageDecrementDelete(t *testing.T) {
	st := &topReactionsState{
		Messages: map[snowflake.ID]trackedMessage{
			12345: {Count: 1},
		},
	}

	msgID := snowflake.ID(12345)

	// Simulate remove
	tm := st.Messages[msgID]
	tm.Count--
	if tm.Count <= 0 {
		delete(st.Messages, msgID)
	} else {
		st.Messages[msgID] = tm
	}

	if _, ok := st.Messages[msgID]; ok {
		t.Error("message should have been deleted when count reached 0")
	}
}

func TestBlacklistAddRemove(t *testing.T) {
	s := &topReactionsSettings{}

	// Add
	s.Blacklist = append(s.Blacklist, 100)
	s.Blacklist = append(s.Blacklist, 200)

	if !s.isBlacklisted(100) {
		t.Error("100 should be blacklisted after add")
	}

	// Remove 100
	for i, id := range s.Blacklist {
		if id == 100 {
			s.Blacklist = append(s.Blacklist[:i], s.Blacklist[i+1:]...)
			break
		}
	}

	if s.isBlacklisted(100) {
		t.Error("100 should not be blacklisted after remove")
	}
	if !s.isBlacklisted(200) {
		t.Error("200 should still be blacklisted")
	}
}
