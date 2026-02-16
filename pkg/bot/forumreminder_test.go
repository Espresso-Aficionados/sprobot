package bot

import (
	"testing"
	"time"
)

func TestSnowflakeToTime(t *testing.T) {
	// Discord snowflake for a known timestamp
	// Snowflake 1019753326469980262 was created around Aug 2022
	ts := snowflakeToTime(1019753326469980262)

	if ts.Year() < 2020 || ts.Year() > 2030 {
		t.Errorf("snowflakeToTime returned unexpected year: %d", ts.Year())
	}
	if ts.IsZero() {
		t.Error("snowflakeToTime returned zero time")
	}
}

func TestSnowflakeToTimeEpoch(t *testing.T) {
	// Snowflake 0 should be the Discord epoch: Jan 1, 2015 UTC
	ts := snowflakeToTime(0).UTC()
	if ts.Year() != 2015 || ts.Month() != 1 || ts.Day() != 1 {
		t.Errorf("snowflakeToTime(0) = %v, expected 2015-01-01", ts)
	}
}

func TestSnowflakeToTimeOrdering(t *testing.T) {
	// Higher snowflake IDs should be later in time
	t1 := snowflakeToTime(1000000000000000000)
	t2 := snowflakeToTime(1100000000000000000)
	if !t2.After(t1) {
		t.Errorf("higher snowflake should be later: %v vs %v", t1, t2)
	}
}

func TestGetThreadHelpConfigDev(t *testing.T) {
	config := getThreadHelpConfig("dev")
	if config == nil {
		t.Fatal("dev config is nil")
	}
	info, ok := config[1019680268229021807]
	if !ok {
		t.Fatal("dev channel ID not found")
	}
	if info.HelperID != "1015493549430685706" {
		t.Errorf("HelperID = %q, want %q", info.HelperID, "1015493549430685706")
	}
	if info.MaxThreadAge != 5*time.Minute {
		t.Errorf("MaxThreadAge = %v, want 5m", info.MaxThreadAge)
	}
	if info.HistoryLimit != 5 {
		t.Errorf("HistoryLimit = %d, want 5", info.HistoryLimit)
	}
}

func TestGetThreadHelpConfigProd(t *testing.T) {
	config := getThreadHelpConfig("prod")
	if config == nil {
		t.Fatal("prod config is nil")
	}
	info, ok := config[1019753326469980262]
	if !ok {
		t.Fatal("prod channel ID not found")
	}
	if info.MaxThreadAge != 24*time.Hour {
		t.Errorf("MaxThreadAge = %v, want 24h", info.MaxThreadAge)
	}
	if info.HistoryLimit != 50 {
		t.Errorf("HistoryLimit = %d, want 50", info.HistoryLimit)
	}
}

func TestGetThreadHelpConfigUnknown(t *testing.T) {
	config := getThreadHelpConfig("staging")
	if config != nil {
		t.Error("expected nil for unknown env")
	}
}
