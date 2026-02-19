package stickybot

import "testing"

func TestGetGuildIDsDev(t *testing.T) {
	ids := getGuildIDs("dev")
	if ids == nil {
		t.Fatal("dev guild IDs is nil")
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 dev guild, got %d", len(ids))
	}
	if ids[0] != 1013566342345019512 {
		t.Errorf("dev guild ID = %d, want 1013566342345019512", ids[0])
	}
}

func TestGetGuildIDsProd(t *testing.T) {
	ids := getGuildIDs("prod")
	if ids == nil {
		t.Fatal("prod guild IDs is nil")
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 prod guild, got %d", len(ids))
	}
	if ids[0] != 726985544038612993 {
		t.Errorf("prod guild ID = %d, want 726985544038612993", ids[0])
	}
}

func TestGetGuildIDsUnknown(t *testing.T) {
	if getGuildIDs("staging") != nil {
		t.Error("expected nil for unknown env")
	}
	if getGuildIDs("") != nil {
		t.Error("expected nil for empty env")
	}
}
