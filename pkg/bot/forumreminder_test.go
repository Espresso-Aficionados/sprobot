package bot

import (
	"testing"
	"time"
)

func TestGetThreadHelpConfig(t *testing.T) {
	config := getThreadHelpConfig()
	if config == nil {
		t.Fatal("config is nil")
	}
	if len(config) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(config))
	}

	info, ok := config[1019680268229021807]
	if !ok {
		t.Fatal("dev channel ID not found")
	}
	if info.HelperID != 1015493549430685706 {
		t.Errorf("dev HelperID = %d, want %d", info.HelperID, 1015493549430685706)
	}
	if info.MaxThreadAge != 5*time.Minute {
		t.Errorf("dev MaxThreadAge = %v, want 5m", info.MaxThreadAge)
	}
	if info.HistoryLimit != 5 {
		t.Errorf("dev HistoryLimit = %d, want 5", info.HistoryLimit)
	}

	info, ok = config[1019753326469980262]
	if !ok {
		t.Fatal("prod channel ID not found")
	}
	if info.MaxThreadAge != 24*time.Hour {
		t.Errorf("prod MaxThreadAge = %v, want 24h", info.MaxThreadAge)
	}
	if info.HistoryLimit != 50 {
		t.Errorf("prod HistoryLimit = %d, want 50", info.HistoryLimit)
	}
}
