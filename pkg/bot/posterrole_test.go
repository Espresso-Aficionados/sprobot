package bot

import "testing"

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
