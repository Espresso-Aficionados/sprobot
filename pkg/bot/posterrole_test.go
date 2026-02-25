package bot

import (
	"encoding/json"
	"testing"
)

func TestGetPosterRoleConfigDev(t *testing.T) {
	t.Setenv("SPROBOT_POSTER_ROLE_THRESHOLD", "100")
	cfg := getPosterRoleConfig("dev")
	if cfg == nil {
		t.Fatal("dev config is nil")
	}
	c, ok := cfg[1013566342345019512]
	if !ok {
		t.Fatal("missing dev guild entry")
	}
	if c.RoleID == 0 {
		t.Error("dev RoleID should be set")
	}
	if c.Threshold != 100 {
		t.Errorf("threshold = %d, want 100", c.Threshold)
	}
}

func TestGetPosterRoleConfigProd(t *testing.T) {
	t.Setenv("SPROBOT_POSTER_ROLE_THRESHOLD", "50")
	cfg := getPosterRoleConfig("prod")
	if cfg == nil {
		t.Fatal("prod config is nil")
	}
	c, ok := cfg[726985544038612993]
	if !ok {
		t.Fatal("missing prod guild entry")
	}
	if c.RoleID == 0 {
		t.Error("prod RoleID should be set")
	}
	if len(c.SkipChannels) == 0 {
		t.Error("prod SkipChannels should not be empty")
	}
}

func TestGetPosterRoleConfigUnknown(t *testing.T) {
	t.Setenv("SPROBOT_POSTER_ROLE_THRESHOLD", "100")
	if getPosterRoleConfig("staging") != nil {
		t.Error("expected nil for unknown env")
	}
}

func TestGetPosterRoleConfigMissingThreshold(t *testing.T) {
	t.Setenv("SPROBOT_POSTER_ROLE_THRESHOLD", "")
	if getPosterRoleConfig("dev") != nil {
		t.Error("expected nil when threshold not set")
	}
}

func TestGetPosterRoleConfigInvalidThreshold(t *testing.T) {
	t.Setenv("SPROBOT_POSTER_ROLE_THRESHOLD", "notanumber")
	if getPosterRoleConfig("dev") != nil {
		t.Error("expected nil for invalid threshold")
	}
}

func TestGetPosterRoleConfigZeroThreshold(t *testing.T) {
	t.Setenv("SPROBOT_POSTER_ROLE_THRESHOLD", "0")
	if getPosterRoleConfig("dev") != nil {
		t.Error("expected nil for zero threshold")
	}
}

func TestPosterRoleProgressMath(t *testing.T) {
	tests := []struct {
		name          string
		tracked       int
		history       int
		threshold     int
		wantPct       int
		wantRemaining int
	}{
		{"zero progress", 0, 0, 100, 0, 100},
		{"half way", 25, 25, 100, 50, 50},
		{"at threshold", 50, 50, 100, 100, 0},
		{"over threshold", 60, 50, 100, 110, 0},
		{"history only", 0, 75, 100, 75, 25},
		{"tracked only", 30, 0, 100, 30, 70},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := tt.tracked + tt.history
			pct := 0
			if tt.threshold > 0 {
				pct = total * 100 / tt.threshold
			}
			remaining := tt.threshold - total
			if remaining < 0 {
				remaining = 0
			}

			if pct != tt.wantPct {
				t.Errorf("pct = %d, want %d", pct, tt.wantPct)
			}
			if remaining != tt.wantRemaining {
				t.Errorf("remaining = %d, want %d", remaining, tt.wantRemaining)
			}
		})
	}
}

func TestPosterRoleStateJSONRoundTrip(t *testing.T) {
	st := &posterRoleState{
		Tracked:   map[string]int{"111": 5, "222": 10},
		History:   map[string]int{"111": 50},
		searching: map[string]bool{"333": true},
	}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	var loaded posterRoleState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.Tracked["111"] != 5 {
		t.Errorf("Tracked[111] = %d, want 5", loaded.Tracked["111"])
	}
	if loaded.History["111"] != 50 {
		t.Errorf("History[111] = %d, want 50", loaded.History["111"])
	}
	// searching is unexported and should not be serialized
	if loaded.searching != nil {
		t.Error("searching should be nil after JSON round-trip")
	}
}

func TestDiscordSearchResponseJSON(t *testing.T) {
	data := []byte(`{"total_results": 42, "extra_field": true}`)
	var resp discordSearchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TotalResults != 42 {
		t.Errorf("TotalResults = %d, want 42", resp.TotalResults)
	}
}
