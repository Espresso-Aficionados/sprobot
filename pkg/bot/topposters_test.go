package bot

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

func TestDefaultTopPostersConfig(t *testing.T) {
	configs := defaultTopPostersConfig()
	if configs == nil {
		t.Fatal("config is nil")
	}
	if len(configs) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(configs))
	}
	roleID, ok := configs[1013566342345019512]
	if !ok {
		t.Fatal("dev guild ID not found")
	}
	if roleID != 0 {
		t.Errorf("dev TargetRoleID = %d, want 0", roleID)
	}
	roleID, ok = configs[726985544038612993]
	if !ok {
		t.Fatal("prod guild ID not found")
	}
	if roleID != 791104833117225000 {
		t.Errorf("prod TargetRoleID = %d, want 791104833117225000", roleID)
	}
}

func TestPruneOldDays(t *testing.T) {
	counts := map[string]map[string]int{
		"2026-02-01": {"user1": 5},
		"2026-02-05": {"user2": 3},
		"2026-02-10": {"user3": 7},
		"2026-02-14": {"user4": 2},
	}

	pruneOldDays(counts, "2026-02-09")

	if _, ok := counts["2026-02-01"]; ok {
		t.Error("2026-02-01 should have been pruned")
	}
	if _, ok := counts["2026-02-05"]; ok {
		t.Error("2026-02-05 should have been pruned")
	}
	if _, ok := counts["2026-02-10"]; !ok {
		t.Error("2026-02-10 should be kept")
	}
	if _, ok := counts["2026-02-14"]; !ok {
		t.Error("2026-02-14 should be kept")
	}
}

func TestPruneOldDaysEmpty(t *testing.T) {
	counts := map[string]map[string]int{}
	pruneOldDays(counts, "2026-02-09")
	if len(counts) != 0 {
		t.Errorf("expected empty map, got %d entries", len(counts))
	}
}

func TestPruneOldDaysCutoffExact(t *testing.T) {
	counts := map[string]map[string]int{
		"2026-02-09": {"user1": 5},
		"2026-02-10": {"user2": 3},
	}

	pruneOldDays(counts, "2026-02-10")

	if _, ok := counts["2026-02-09"]; ok {
		t.Error("date before cutoff should be pruned")
	}
	if _, ok := counts["2026-02-10"]; !ok {
		t.Error("date equal to cutoff should be kept")
	}
}

func TestAggregateCounts(t *testing.T) {
	counts := map[string]map[string]int{
		"2026-02-10": {"user1": 5, "user2": 3},
		"2026-02-11": {"user1": 2, "user3": 7},
		"2026-02-12": {"user2": 1},
	}

	totals := aggregateCounts(counts)

	if totals["user1"] != 7 {
		t.Errorf("user1 total = %d, want 7", totals["user1"])
	}
	if totals["user2"] != 4 {
		t.Errorf("user2 total = %d, want 4", totals["user2"])
	}
	if totals["user3"] != 7 {
		t.Errorf("user3 total = %d, want 7", totals["user3"])
	}
}

func TestOldestDate(t *testing.T) {
	counts := map[string]map[string]int{
		"2026-02-12": {"user1": 1},
		"2026-02-10": {"user2": 2},
		"2026-02-14": {"user3": 3},
	}
	if got := oldestDate(counts); got != "2026-02-10" {
		t.Errorf("oldestDate = %q, want %q", got, "2026-02-10")
	}
}

func TestOldestDateEmpty(t *testing.T) {
	if got := oldestDate(map[string]map[string]int{}); got != "" {
		t.Errorf("oldestDate = %q, want empty", got)
	}
}

func TestOldestDateSingleDay(t *testing.T) {
	counts := map[string]map[string]int{
		"2026-02-10": {"user1": 5},
	}
	if got := oldestDate(counts); got != "2026-02-10" {
		t.Errorf("oldestDate = %q, want %q", got, "2026-02-10")
	}
}

func TestAggregateCountsEmpty(t *testing.T) {
	totals := aggregateCounts(map[string]map[string]int{})
	if len(totals) != 0 {
		t.Errorf("expected empty map, got %d entries", len(totals))
	}
}

func TestAggregateCountsSingleDay(t *testing.T) {
	counts := map[string]map[string]int{
		"2026-02-10": {"user1": 10, "user2": 5},
	}

	totals := aggregateCounts(counts)

	if totals["user1"] != 10 {
		t.Errorf("user1 total = %d, want 10", totals["user1"])
	}
	if totals["user2"] != 5 {
		t.Errorf("user2 total = %d, want 5", totals["user2"])
	}
}

func TestAggregateCountsSingleUserMultipleDays(t *testing.T) {
	counts := map[string]map[string]int{
		"2026-02-10": {"user1": 3},
		"2026-02-11": {"user1": 4},
		"2026-02-12": {"user1": 5},
	}

	totals := aggregateCounts(counts)

	if totals["user1"] != 12 {
		t.Errorf("user1 total = %d, want 12", totals["user1"])
	}
	if len(totals) != 1 {
		t.Errorf("expected 1 user, got %d", len(totals))
	}
}

func TestRecordCountsAndKeepsLatestUsername(t *testing.T) {
	// Usernames is nil, as in data saved before usernames were stored.
	gc := &guildPostCounts{Counts: make(map[string]map[string]int)}

	gc.record("2026-02-16", "1", "alice")
	gc.record("2026-02-16", "1", "alice_new")
	gc.record("2026-02-17", "1", "alice_new")
	gc.record("2026-02-17", "2", "bob")

	if got := gc.Counts["2026-02-16"]["1"]; got != 2 {
		t.Errorf("day 1 count = %d, want 2", got)
	}
	if got := aggregateCounts(gc.Counts); got["1"] != 3 || got["2"] != 1 {
		t.Errorf("totals = %v, want 1:3 2:1", got)
	}
	if gc.Usernames["1"] != "alice_new" || gc.Usernames["2"] != "bob" {
		t.Errorf("usernames = %v, want the latest name per user", gc.Usernames)
	}
}

func TestPruneDropsUsernamesWithoutCounts(t *testing.T) {
	gc := &guildPostCounts{
		Counts: map[string]map[string]int{
			"2026-02-01": {"1": 5, "2": 1},
			"2026-02-10": {"2": 3},
		},
		Usernames: map[string]string{"1": "alice", "2": "bob", "3": "carol"},
	}

	gc.prune("2026-02-09")

	if _, ok := gc.Counts["2026-02-01"]; ok {
		t.Error("the day before the cutoff should be pruned")
	}
	// 1 only posted on the pruned day, and 3 has no counts at all.
	if len(gc.Usernames) != 1 || gc.Usernames["2"] != "bob" {
		t.Errorf("usernames = %v, want only 2:bob", gc.Usernames)
	}
}

// What saveTopPosters writes to S3 holds only the last 7 days and the
// usernames those days still count.
func TestSaveTopPostersPrunesWhatItStores(t *testing.T) {
	b, fake := newProfileNamesBot(t)
	b.topPosters = newGuildStateStore[guildPostCounts](b.S3, b.Log, "topposters", nil,
		func(gc *guildPostCounts) *sync.Mutex { return &gc.mu })
	today := time.Now().UTC().Format("2006-01-02")
	b.topPosters.set(1, &guildPostCounts{
		Counts: map[string]map[string]int{
			"2026-01-01": {"1": 5},
			today:        {"2": 3},
		},
		Usernames: map[string]string{"1": "alice", "2": "bob"},
	})

	b.saveTopPosters()

	fake.Mu.Lock()
	body, ok := fake.Objects["/test-bucket/topposters/1.json"]
	fake.Mu.Unlock()
	if !ok {
		t.Fatal("topposters/1.json was not saved")
	}
	var saved guildPostCounts
	if err := json.Unmarshal(body, &saved); err != nil {
		t.Fatal(err)
	}
	if _, ok := saved.Counts["2026-01-01"]; ok || saved.Counts[today]["2"] != 3 {
		t.Errorf("saved counts = %v, want only today's", saved.Counts)
	}
	if len(saved.Usernames) != 1 || saved.Usernames["2"] != "bob" {
		t.Errorf("saved usernames = %v, want only 2:bob", saved.Usernames)
	}
}

func TestTemplateCommandsCount(t *testing.T) {
	cmds := templateCommands(sprobot.ProfileTemplate)

	// 1 user menu + 1 message menu + 3 slash commands = 5
	if len(cmds) != 5 {
		t.Errorf("templateCommands returned %d commands, want 5", len(cmds))
	}
}

func TestTemplateCommandsNames(t *testing.T) {
	cmds := templateCommands(sprobot.ProfileTemplate)

	names := make(map[string]bool)
	for _, cmd := range cmds {
		switch c := cmd.(type) {
		case discord.UserCommandCreate:
			names[c.Name] = true
		case discord.MessageCommandCreate:
			names[c.Name] = true
		}
	}

	expected := []string{
		"Get Coffee Setup Profile",
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing command %q", name)
		}
	}
}

func TestTemplateCommandsTypes(t *testing.T) {
	cmds := templateCommands(sprobot.ProfileTemplate)

	var userCount, msgCount, slashCount int
	for _, cmd := range cmds {
		switch cmd.(type) {
		case discord.UserCommandCreate:
			userCount++
		case discord.MessageCommandCreate:
			msgCount++
		case discord.SlashCommandCreate:
			slashCount++
		}
	}

	if userCount != 1 {
		t.Errorf("user commands = %d, want 1", userCount)
	}
	if msgCount != 1 {
		t.Errorf("message commands = %d, want 1", msgCount)
	}
	if slashCount != 3 {
		t.Errorf("slash commands = %d, want 3", slashCount)
	}
}

func TestTemplateCommandsRoaster(t *testing.T) {
	cmds := templateCommands(sprobot.RoasterTemplate)

	names := make(map[string]bool)
	for _, cmd := range cmds {
		switch c := cmd.(type) {
		case discord.UserCommandCreate:
			names[c.Name] = true
		case discord.MessageCommandCreate:
			names[c.Name] = true
		}
	}

	expected := []string{
		"Get Roasting Setup Profile",
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing command %q", name)
		}
	}
}

// /config topposters creates a guild's state on first use, but not while its
// load has failed: a save of fresh state would replace the stored filters.
func TestTopPostersConfigNeedsLoadedState(t *testing.T) {
	b, _ := newStubBot(t)
	b.topPostersConfig = newGuildStateStore(b.S3, b.Log, "toppostersconfig",
		func() *topPostersConfigState { return &topPostersConfigState{} },
		func(st *topPostersConfigState) *sync.Mutex { return &st.mu })
	const filterAdd = `{"id":"100","name":"config","type":1,"options":[{"name":"topposters","type":2,"options":[` +
		`{"name":"filter-add","type":1,"options":[{"name":"user","type":6,"value":"55"}]}]}],` +
		`"resolved":{"users":{"55":{"id":"55","username":"bob"}}}}`

	b.topPostersConfig.mu.Lock()
	b.topPostersConfig.failed[10] = true
	b.topPostersConfig.mu.Unlock()
	e, replies := commandEvent(t, b.Client, filterAdd)
	b.handleTopPostersConfigCmd(e)
	if _, resp := replies.only(t); !isEphemeral(resp) || !strings.Contains(contentOf(resp), "can't be changed right now") {
		t.Errorf("reply while the load has failed = %q, want an ephemeral refusal", contentOf(resp))
	}
	if st := b.topPostersConfig.get(10); st != nil {
		t.Errorf("state %+v installed for a guild whose load failed", st)
	}

	b.topPostersConfig.mu.Lock()
	delete(b.topPostersConfig.failed, 10)
	b.topPostersConfig.mu.Unlock()
	e, replies = commandEvent(t, b.Client, filterAdd)
	b.handleTopPostersConfigCmd(e)
	if _, resp := replies.only(t); !strings.HasPrefix(contentOf(resp), "Added to filter") {
		t.Errorf("reply on a guild with no state = %q, want the filter added", contentOf(resp))
	}
	if st := b.topPostersConfig.get(10); st == nil || !st.isUserFiltered(55) {
		t.Errorf("state after filter-add on a guild with none = %+v, want user 55 filtered", st)
	}
}
