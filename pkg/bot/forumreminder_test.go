package bot

import (
	"testing"
	"time"
)

func TestGetThreadHelpConfigDev(t *testing.T) {
	config := getThreadHelpConfig("dev")
	if config == nil {
		t.Fatal("dev config is nil")
	}
	info, ok := config[1019680268229021807]
	if !ok {
		t.Fatal("dev channel ID not found")
	}
	if info.HelperID != 1015493549430685706 {
		t.Errorf("HelperID = %d, want %d", info.HelperID, 1015493549430685706)
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
