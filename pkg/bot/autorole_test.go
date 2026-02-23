package bot

import "testing"

func TestGetAutoRoleIDDev(t *testing.T) {
	id := getAutoRoleID("dev")
	if id == 0 {
		t.Error("dev auto role ID should not be 0")
	}
}

func TestGetAutoRoleIDProd(t *testing.T) {
	id := getAutoRoleID("prod")
	if id == 0 {
		t.Error("prod auto role ID should not be 0")
	}
}

func TestGetAutoRoleIDUnknown(t *testing.T) {
	id := getAutoRoleID("staging")
	if id != 0 {
		t.Errorf("unknown env should return 0, got %d", id)
	}
}

func TestGetAutoRoleIDEmpty(t *testing.T) {
	id := getAutoRoleID("")
	if id != 0 {
		t.Errorf("empty env should return 0, got %d", id)
	}
}
