package bot

import (
	"encoding/json"
	"testing"

	"github.com/disgoorg/snowflake/v2"
)

func TestPosterRoleSettingsJSONRoundTrip(t *testing.T) {
	s := posterRoleSettings{
		RoleID:       1234567890,
		Threshold:    50,
		SkipChannels: []snowflake.ID{111, 222, 333},
	}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	var loaded posterRoleSettings
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.RoleID != s.RoleID {
		t.Errorf("RoleID = %d, want %d", loaded.RoleID, s.RoleID)
	}
	if loaded.Threshold != s.Threshold {
		t.Errorf("Threshold = %d, want %d", loaded.Threshold, s.Threshold)
	}
	if len(loaded.SkipChannels) != len(s.SkipChannels) {
		t.Fatalf("SkipChannels len = %d, want %d", len(loaded.SkipChannels), len(s.SkipChannels))
	}
	for i, id := range loaded.SkipChannels {
		if id != s.SkipChannels[i] {
			t.Errorf("SkipChannels[%d] = %d, want %d", i, id, s.SkipChannels[i])
		}
	}
}

func TestIsSkippedSingle(t *testing.T) {
	s := posterRoleSettings{
		SkipChannels: []snowflake.ID{100, 200, 300},
	}

	if !s.isSkipped(200) {
		t.Error("expected 200 to be skipped")
	}
	if s.isSkipped(400) {
		t.Error("expected 400 to not be skipped")
	}
}

func TestIsSkippedVariadic(t *testing.T) {
	s := posterRoleSettings{
		SkipChannels: []snowflake.ID{100, 200},
	}

	if !s.isSkipped(999, 200) {
		t.Error("expected match when second arg is in skip list")
	}
	if s.isSkipped(999, 888) {
		t.Error("expected no match when neither arg is in skip list")
	}
}

func TestIsSkippedEmpty(t *testing.T) {
	s := posterRoleSettings{}

	if s.isSkipped(100) {
		t.Error("expected no match with nil skip list")
	}

	s.SkipChannels = []snowflake.ID{}
	if s.isSkipped(100) {
		t.Error("expected no match with empty skip list")
	}
}

func TestPosterRoleProgressMath(t *testing.T) {
	tests := []struct {
		name          string
		count         int
		threshold     int
		wantPct       int
		wantRemaining int
	}{
		{"zero progress", 0, 100, 0, 100},
		{"half way", 50, 100, 50, 50},
		{"at threshold", 100, 100, 100, 0},
		{"over threshold", 110, 100, 110, 0},
		{"low count", 30, 100, 30, 70},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pct := 0
			if tt.threshold > 0 {
				pct = tt.count * 100 / tt.threshold
			}
			remaining := tt.threshold - tt.count
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
		Settings: posterRoleSettings{
			RoleID:       9999,
			Threshold:    100,
			SkipChannels: []snowflake.ID{111},
		},
		Counts:  map[string]int{"111": 55, "222": 10},
		Fetched: map[string]bool{"111": true},
	}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	var loaded posterRoleState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.Settings.RoleID != 9999 {
		t.Errorf("Settings.RoleID = %d, want 9999", loaded.Settings.RoleID)
	}
	if loaded.Settings.Threshold != 100 {
		t.Errorf("Settings.Threshold = %d, want 100", loaded.Settings.Threshold)
	}
	if loaded.Counts["111"] != 55 {
		t.Errorf("Counts[111] = %d, want 55", loaded.Counts["111"])
	}
	if !loaded.Fetched["111"] {
		t.Error("Fetched[111] should be true")
	}
	if loaded.Fetched["222"] {
		t.Error("Fetched[222] should be false")
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

func TestDiscordSearchResponseWithMessages(t *testing.T) {
	data := []byte(`{
		"total_results": 3,
		"messages": [
			[{"channel_id": "100"}],
			[{"channel_id": "200"}],
			[{"channel_id": "100"}]
		]
	}`)
	var resp discordSearchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TotalResults != 3 {
		t.Errorf("TotalResults = %d, want 3", resp.TotalResults)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("Messages len = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[0][0].ChannelID != 100 {
		t.Errorf("Messages[0][0].ChannelID = %d, want 100", resp.Messages[0][0].ChannelID)
	}
	if resp.Messages[1][0].ChannelID != 200 {
		t.Errorf("Messages[1][0].ChannelID = %d, want 200", resp.Messages[1][0].ChannelID)
	}
}

func TestTopChannels(t *testing.T) {
	t.Run("multiple channels", func(t *testing.T) {
		resp := &discordSearchResponse{
			Messages: [][]searchHitMessage{
				{{ChannelID: 100}},
				{{ChannelID: 200}},
				{{ChannelID: 100}},
				{{ChannelID: 300}},
				{{ChannelID: 100}},
				{{ChannelID: 200}},
			},
		}
		top := topChannels(resp, 5)
		if len(top) != 3 {
			t.Fatalf("len = %d, want 3", len(top))
		}
		if top[0].ChannelID != 100 || top[0].Count != 3 {
			t.Errorf("top[0] = {%d, %d}, want {100, 3}", top[0].ChannelID, top[0].Count)
		}
		if top[1].ChannelID != 200 || top[1].Count != 2 {
			t.Errorf("top[1] = {%d, %d}, want {200, 2}", top[1].ChannelID, top[1].Count)
		}
		if top[2].ChannelID != 300 || top[2].Count != 1 {
			t.Errorf("top[2] = {%d, %d}, want {300, 1}", top[2].ChannelID, top[2].Count)
		}
	})

	t.Run("capped at n", func(t *testing.T) {
		resp := &discordSearchResponse{
			Messages: [][]searchHitMessage{
				{{ChannelID: 1}},
				{{ChannelID: 2}},
				{{ChannelID: 3}},
				{{ChannelID: 4}},
			},
		}
		top := topChannels(resp, 2)
		if len(top) != 2 {
			t.Fatalf("len = %d, want 2", len(top))
		}
	})

	t.Run("empty", func(t *testing.T) {
		resp := &discordSearchResponse{}
		top := topChannels(resp, 5)
		if len(top) != 0 {
			t.Errorf("len = %d, want 0", len(top))
		}
	})

	t.Run("tie breaking by channel ID", func(t *testing.T) {
		resp := &discordSearchResponse{
			Messages: [][]searchHitMessage{
				{{ChannelID: 300}},
				{{ChannelID: 100}},
			},
		}
		top := topChannels(resp, 5)
		if len(top) != 2 {
			t.Fatalf("len = %d, want 2", len(top))
		}
		// Same count (1 each), should sort by channel ID ascending
		if top[0].ChannelID != 100 {
			t.Errorf("top[0].ChannelID = %d, want 100", top[0].ChannelID)
		}
		if top[1].ChannelID != 300 {
			t.Errorf("top[1].ChannelID = %d, want 300", top[1].ChannelID)
		}
	})
}
