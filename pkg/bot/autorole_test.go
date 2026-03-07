package bot

import "testing"

func TestGetAutoRoleConfig(t *testing.T) {
	cfg := getAutoRoleConfig()
	if len(cfg) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(cfg))
	}
	if cfg[726985544038612993] == 0 {
		t.Error("prod auto role ID should not be 0")
	}
	if cfg[1013566342345019512] == 0 {
		t.Error("dev auto role ID should not be 0")
	}
}
