package bot

import (
	"sync"
	"testing"

	"github.com/disgoorg/disgo/discord"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

func TestGetTopPostersConfigDev(t *testing.T) {
	configs := getTopPostersConfig("dev")
	if configs == nil {
		t.Fatal("dev config is nil")
	}
	cfg, ok := configs[1013566342345019512]
	if !ok {
		t.Fatal("dev guild ID not found")
	}
	if cfg.TargetRoleID != 0 {
		t.Errorf("TargetRoleID = %d, want 0", cfg.TargetRoleID)
	}
}

func TestGetTopPostersConfigProd(t *testing.T) {
	configs := getTopPostersConfig("prod")
	if configs == nil {
		t.Fatal("prod config is nil")
	}
	cfg, ok := configs[726985544038612993]
	if !ok {
		t.Fatal("prod guild ID not found")
	}
	if cfg.TargetRoleID != 791104833117225000 {
		t.Errorf("TargetRoleID = %d, want 791104833117225000", cfg.TargetRoleID)
	}
}

func TestGetTopPostersConfigUnknown(t *testing.T) {
	if getTopPostersConfig("staging") != nil {
		t.Error("expected nil for unknown env")
	}
	if getTopPostersConfig("") != nil {
		t.Error("expected nil for empty env")
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

func TestGuildPostCountsConcurrent(t *testing.T) {
	gc := &guildPostCounts{Counts: make(map[string]map[string]int)}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gc.mu.Lock()
			if gc.Counts["2026-02-16"] == nil {
				gc.Counts["2026-02-16"] = make(map[string]int)
			}
			gc.Counts["2026-02-16"]["user1"]++
			gc.mu.Unlock()
		}()
	}
	wg.Wait()

	if gc.Counts["2026-02-16"]["user1"] != 100 {
		t.Errorf("concurrent count = %d, want 100", gc.Counts["2026-02-16"]["user1"])
	}
}

func TestTemplateCommandsCount(t *testing.T) {
	cmds := templateCommands(sprobot.ProfileTemplate)

	// 3 slash commands + 1 user menu + 1 message menu = 5
	if len(cmds) != 5 {
		t.Errorf("templateCommands returned %d commands, want 5", len(cmds))
	}
}

func TestTemplateCommandsNames(t *testing.T) {
	cmds := templateCommands(sprobot.ProfileTemplate)

	names := make(map[string]bool)
	for _, cmd := range cmds {
		switch c := cmd.(type) {
		case discord.SlashCommandCreate:
			names[c.Name] = true
		case discord.UserCommandCreate:
			names[c.Name] = true
		case discord.MessageCommandCreate:
			names[c.Name] = true
		}
	}

	expected := []string{
		"editprofile",
		"getprofile",
		"deleteprofile",
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

	var slashCount, userCount, msgCount int
	for _, cmd := range cmds {
		switch cmd.(type) {
		case discord.SlashCommandCreate:
			slashCount++
		case discord.UserCommandCreate:
			userCount++
		case discord.MessageCommandCreate:
			msgCount++
		}
	}

	if slashCount != 3 {
		t.Errorf("slash commands = %d, want 3", slashCount)
	}
	if userCount != 1 {
		t.Errorf("user commands = %d, want 1", userCount)
	}
	if msgCount != 1 {
		t.Errorf("message commands = %d, want 1", msgCount)
	}
}

func TestTemplateCommandsRoaster(t *testing.T) {
	cmds := templateCommands(sprobot.RoasterTemplate)

	names := make(map[string]bool)
	for _, cmd := range cmds {
		switch c := cmd.(type) {
		case discord.SlashCommandCreate:
			names[c.Name] = true
		case discord.UserCommandCreate:
			names[c.Name] = true
		case discord.MessageCommandCreate:
			names[c.Name] = true
		}
	}

	expected := []string{
		"editroaster",
		"getroaster",
		"deleteroaster",
		"Get Roasting Setup Profile",
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing command %q", name)
		}
	}
}

func TestTemplateCommandsGetHasUserOption(t *testing.T) {
	cmds := templateCommands(sprobot.ProfileTemplate)

	for _, cmd := range cmds {
		if sc, ok := cmd.(discord.SlashCommandCreate); ok && sc.Name == "getprofile" {
			if len(sc.Options) != 1 {
				t.Fatalf("getprofile options = %d, want 1", len(sc.Options))
			}
			opt, ok := sc.Options[0].(discord.ApplicationCommandOptionUser)
			if !ok {
				t.Fatalf("getprofile option type = %T, want ApplicationCommandOptionUser", sc.Options[0])
			}
			if opt.Name != "name" {
				t.Errorf("getprofile option name = %q, want %q", opt.Name, "name")
			}
			return
		}
	}
	t.Error("getprofile command not found")
}
