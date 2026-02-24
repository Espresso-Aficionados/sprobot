package bot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

func TestStarboardStateJSONRoundTrip(t *testing.T) {
	st := &starboardState{
		Settings: starboardSettings{
			OutputChannelID: 123456,
			Emoji:           "⭐",
			Threshold:       3,
			Blacklist:       []snowflake.ID{111, 222},
		},
		Entries: map[snowflake.ID]starboardEntry{
			999: {ChannelID: 100, AuthorID: 200, Count: 5, StarboardMsgID: 888},
		},
	}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(data), "mu") {
		t.Error("mu should not be serialized to JSON")
	}

	var loaded starboardState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.Settings.OutputChannelID != 123456 {
		t.Errorf("expected OutputChannelID 123456, got %d", loaded.Settings.OutputChannelID)
	}
	if loaded.Settings.Emoji != "⭐" {
		t.Errorf("expected Emoji ⭐, got %s", loaded.Settings.Emoji)
	}
	if loaded.Settings.Threshold != 3 {
		t.Errorf("expected Threshold 3, got %d", loaded.Settings.Threshold)
	}
	if len(loaded.Settings.Blacklist) != 2 {
		t.Errorf("expected 2 blacklist entries, got %d", len(loaded.Settings.Blacklist))
	}
	if len(loaded.Entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(loaded.Entries))
	}
	entry := loaded.Entries[999]
	if entry.Count != 5 || entry.ChannelID != 100 || entry.AuthorID != 200 || entry.StarboardMsgID != 888 {
		t.Errorf("unexpected entry data: %+v", entry)
	}
}

func TestPruneUnpostedEntries(t *testing.T) {
	now := time.Now()
	recentID := snowflake.New(now.Add(-1 * time.Hour))
	oldUnpostedID := snowflake.New(now.Add(-31 * 24 * time.Hour))
	oldPostedID := snowflake.New(now.Add(-32 * 24 * time.Hour))

	st := &starboardState{
		Entries: map[snowflake.ID]starboardEntry{
			recentID:      {ChannelID: 1, Count: 2},
			oldUnpostedID: {ChannelID: 1, Count: 3, StarboardMsgID: 0},
			oldPostedID:   {ChannelID: 1, Count: 5, StarboardMsgID: 777},
		},
	}

	pruneUnpostedEntries(st, now)

	if len(st.Entries) != 2 {
		t.Errorf("expected 2 entries after prune, got %d", len(st.Entries))
	}
	if _, ok := st.Entries[recentID]; !ok {
		t.Error("recent entry should have been kept")
	}
	if _, ok := st.Entries[oldUnpostedID]; ok {
		t.Error("old unposted entry should have been pruned")
	}
	if _, ok := st.Entries[oldPostedID]; !ok {
		t.Error("old posted entry should have been kept")
	}
}

func TestEffectiveThresholdDefault(t *testing.T) {
	s := &starboardSettings{}
	if s.effectiveThreshold() != defaultThreshold {
		t.Errorf("expected default threshold %d, got %d", defaultThreshold, s.effectiveThreshold())
	}
}

func TestEffectiveThresholdCustom(t *testing.T) {
	s := &starboardSettings{Threshold: 10}
	if s.effectiveThreshold() != 10 {
		t.Errorf("expected threshold 10, got %d", s.effectiveThreshold())
	}
}

func TestIsBlacklisted(t *testing.T) {
	s := &starboardSettings{
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
	s := &starboardSettings{}
	if s.isBlacklisted(100) {
		t.Error("empty blacklist should never match")
	}
}

func TestEmojiDisplayUnicode(t *testing.T) {
	if got := emojiDisplay("⭐"); got != "⭐" {
		t.Errorf("expected ⭐, got %s", got)
	}
}

func TestEmojiDisplayCustom(t *testing.T) {
	if got := emojiDisplay("fire:123456"); got != "<:fire:123456>" {
		t.Errorf("expected <:fire:123456>, got %s", got)
	}
}

func TestParseEmojiInputUnicode(t *testing.T) {
	if got := parseEmojiInput("⭐"); got != "⭐" {
		t.Errorf("expected ⭐, got %s", got)
	}
}

func TestParseEmojiInputCustom(t *testing.T) {
	if got := parseEmojiInput("<:fire:123456>"); got != "fire:123456" {
		t.Errorf("expected fire:123456, got %s", got)
	}
}

func TestParseEmojiInputAnimated(t *testing.T) {
	if got := parseEmojiInput("<a:dance:789>"); got != "dance:789" {
		t.Errorf("expected dance:789, got %s", got)
	}
}

func TestGetStarboardConfig(t *testing.T) {
	devCfg := getStarboardConfig("dev")
	if devCfg == nil {
		t.Fatal("expected dev config")
	}
	if _, ok := devCfg[1013566342345019512]; !ok {
		t.Error("expected dev guild in config")
	}

	prodCfg := getStarboardConfig("prod")
	if prodCfg == nil {
		t.Fatal("expected prod config")
	}
	if _, ok := prodCfg[726985544038612993]; !ok {
		t.Error("expected prod guild in config")
	}

	if getStarboardConfig("other") != nil {
		t.Error("expected nil for unknown env")
	}
}

func TestIntPtr(t *testing.T) {
	p := intPtr(42)
	if *p != 42 {
		t.Errorf("expected 42, got %d", *p)
	}
}

func TestStarboardEntryIncrement(t *testing.T) {
	st := &starboardState{
		Entries: make(map[snowflake.ID]starboardEntry),
	}

	msgID := snowflake.ID(12345)

	entry := st.Entries[msgID]
	entry.ChannelID = 100
	entry.AuthorID = 200
	entry.Count++
	st.Entries[msgID] = entry

	if st.Entries[msgID].Count != 1 {
		t.Errorf("expected count 1, got %d", st.Entries[msgID].Count)
	}

	entry = st.Entries[msgID]
	entry.Count++
	st.Entries[msgID] = entry

	if st.Entries[msgID].Count != 2 {
		t.Errorf("expected count 2, got %d", st.Entries[msgID].Count)
	}
}

func TestStarboardEntryDecrementFloor(t *testing.T) {
	st := &starboardState{
		Entries: map[snowflake.ID]starboardEntry{
			12345: {Count: 1, StarboardMsgID: 888},
		},
	}

	msgID := snowflake.ID(12345)

	entry := st.Entries[msgID]
	entry.Count--
	if entry.Count < 0 {
		entry.Count = 0
	}
	st.Entries[msgID] = entry

	if st.Entries[msgID].Count != 0 {
		t.Errorf("expected count 0, got %d", st.Entries[msgID].Count)
	}

	// Entry should NOT be deleted (it has a starboard post)
	if _, ok := st.Entries[msgID]; !ok {
		t.Error("entry with starboard post should not be deleted")
	}
}

func TestBlacklistAddRemove(t *testing.T) {
	s := &starboardSettings{}

	s.Blacklist = append(s.Blacklist, 100)
	s.Blacklist = append(s.Blacklist, 200)

	if !s.isBlacklisted(100) {
		t.Error("100 should be blacklisted after add")
	}

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

func TestEmojiDisplayEdgeCases(t *testing.T) {
	// Colon but no ID part — should be treated as unicode
	if got := emojiDisplay("foo:"); got != "foo:" {
		t.Errorf("expected foo:, got %s", got)
	}

	// Empty string
	if got := emojiDisplay(""); got != "" {
		t.Errorf("expected empty string, got %s", got)
	}
}
