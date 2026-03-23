package bot

import (
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
)

// --- formatDuration ---

func TestFormatDurationHours(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{time.Hour, "1 hour"},
		{6 * time.Hour, "6 hours"},
		{12 * time.Hour, "12 hours"},
	}
	for _, tt := range tests {
		if got := formatDuration(tt.d); got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestFormatDurationDays(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{24 * time.Hour, "1 day"},
		{3 * 24 * time.Hour, "3 days"},
		{7 * 24 * time.Hour, "7 days"},
		{14 * 24 * time.Hour, "14 days"},
		{30 * 24 * time.Hour, "30 days"},
	}
	for _, tt := range tests {
		if got := formatDuration(tt.d); got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// --- tempRoleDurations ---

func TestTempRoleDurationsCompleteness(t *testing.T) {
	expected := map[string]time.Duration{
		"1h":  time.Hour,
		"6h":  6 * time.Hour,
		"12h": 12 * time.Hour,
		"1d":  24 * time.Hour,
		"3d":  3 * 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"14d": 14 * 24 * time.Hour,
		"30d": 30 * 24 * time.Hour,
	}
	if len(tempRoleDurations) != len(expected) {
		t.Fatalf("tempRoleDurations has %d entries, want %d", len(tempRoleDurations), len(expected))
	}
	for key, want := range expected {
		got, ok := tempRoleDurations[key]
		if !ok {
			t.Errorf("missing key %q", key)
			continue
		}
		if got != want {
			t.Errorf("tempRoleDurations[%q] = %v, want %v", key, got, want)
		}
	}
}

// --- tempRoleConfigState.find ---

func TestConfigStateFind(t *testing.T) {
	st := &tempRoleConfigState{
		Roles: []tempRoleConfigEntry{
			{RoleID: 100, Duration: time.Hour},
			{RoleID: 200, Duration: 24 * time.Hour},
		},
	}

	entry, ok := st.find(100)
	if !ok {
		t.Fatal("expected to find role 100")
	}
	if entry.Duration != time.Hour {
		t.Errorf("duration = %v, want %v", entry.Duration, time.Hour)
	}

	entry, ok = st.find(200)
	if !ok {
		t.Fatal("expected to find role 200")
	}
	if entry.Duration != 24*time.Hour {
		t.Errorf("duration = %v, want %v", entry.Duration, 24*time.Hour)
	}
}

func TestConfigStateFindNotFound(t *testing.T) {
	st := &tempRoleConfigState{
		Roles: []tempRoleConfigEntry{
			{RoleID: 100, Duration: time.Hour},
		},
	}
	_, ok := st.find(999)
	if ok {
		t.Error("expected not found for role 999")
	}
}

func TestConfigStateFindEmpty(t *testing.T) {
	st := &tempRoleConfigState{}
	_, ok := st.find(100)
	if ok {
		t.Error("expected not found on empty config")
	}
}

// --- configuredRoleIDs ---

func TestConfiguredRoleIDs(t *testing.T) {
	st := &tempRoleConfigState{
		Roles: []tempRoleConfigEntry{
			{RoleID: 100, Duration: time.Hour},
			{RoleID: 200, Duration: 24 * time.Hour},
		},
	}
	ids := st.configuredRoleIDs()
	if len(ids) != 2 {
		t.Fatalf("got %d IDs, want 2", len(ids))
	}
	if _, ok := ids[100]; !ok {
		t.Error("missing role 100")
	}
	if _, ok := ids[200]; !ok {
		t.Error("missing role 200")
	}
}

func TestConfiguredRoleIDsEmpty(t *testing.T) {
	st := &tempRoleConfigState{}
	ids := st.configuredRoleIDs()
	if ids != nil {
		t.Errorf("expected nil for empty config, got %v", ids)
	}
}

// --- partitionExpired (extracted logic from processTempRoleExpiries) ---

func TestPartitionEntries(t *testing.T) {
	now := time.Now()
	entries := []tempRoleEntry{
		{UserID: 1, RoleID: 10, ExpiryAt: now.Add(-time.Hour)},   // expired
		{UserID: 2, RoleID: 20, ExpiryAt: now.Add(time.Hour)},    // remaining
		{UserID: 3, RoleID: 30, ExpiryAt: now.Add(-time.Minute)}, // expired
		{UserID: 4, RoleID: 40, ExpiryAt: now.Add(time.Minute)},  // remaining
	}

	var remaining, expired []tempRoleEntry
	for _, entry := range entries {
		if now.After(entry.ExpiryAt) {
			expired = append(expired, entry)
		} else {
			remaining = append(remaining, entry)
		}
	}

	if len(expired) != 2 {
		t.Errorf("expired count = %d, want 2", len(expired))
	}
	if len(remaining) != 2 {
		t.Errorf("remaining count = %d, want 2", len(remaining))
	}
	if expired[0].UserID != 1 || expired[1].UserID != 3 {
		t.Errorf("wrong expired entries: %+v", expired)
	}
	if remaining[0].UserID != 2 || remaining[1].UserID != 4 {
		t.Errorf("wrong remaining entries: %+v", remaining)
	}
}

func TestPartitionEntriesAllExpired(t *testing.T) {
	now := time.Now()
	entries := []tempRoleEntry{
		{UserID: 1, ExpiryAt: now.Add(-time.Hour)},
		{UserID: 2, ExpiryAt: now.Add(-time.Minute)},
	}

	var remaining, expired []tempRoleEntry
	for _, entry := range entries {
		if now.After(entry.ExpiryAt) {
			expired = append(expired, entry)
		} else {
			remaining = append(remaining, entry)
		}
	}

	if len(expired) != 2 {
		t.Errorf("expired count = %d, want 2", len(expired))
	}
	if len(remaining) != 0 {
		t.Errorf("remaining count = %d, want 0", len(remaining))
	}
}

func TestPartitionEntriesNoneExpired(t *testing.T) {
	now := time.Now()
	entries := []tempRoleEntry{
		{UserID: 1, ExpiryAt: now.Add(time.Hour)},
		{UserID: 2, ExpiryAt: now.Add(time.Minute)},
	}

	var expired []tempRoleEntry
	for _, entry := range entries {
		if now.After(entry.ExpiryAt) {
			expired = append(expired, entry)
		}
	}

	if len(expired) != 0 {
		t.Errorf("expired count = %d, want 0", len(expired))
	}
}

// --- tempRoleState timer management (ensureTempRoleTimer logic) ---

func TestTempRoleStateAddTimer(t *testing.T) {
	st := &tempRoleState{}
	expiry := time.Now().Add(time.Hour)
	st.Entries = append(st.Entries, tempRoleEntry{
		GuildID: 1, UserID: 10, RoleID: 100, ExpiryAt: expiry,
	})

	if len(st.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(st.Entries))
	}
	if st.Entries[0].UserID != 10 || st.Entries[0].RoleID != 100 {
		t.Errorf("unexpected entry: %+v", st.Entries[0])
	}
}

func TestTempRoleStateSkipExisting(t *testing.T) {
	st := &tempRoleState{
		Entries: []tempRoleEntry{
			{GuildID: 1, UserID: 10, RoleID: 100, ExpiryAt: time.Now().Add(time.Hour)},
		},
	}

	// Simulate the "skip if exists" logic from ensureTempRoleTimer(resetTimer=false)
	exists := false
	for _, entry := range st.Entries {
		if entry.UserID == 10 && entry.RoleID == 100 {
			exists = true
			break
		}
	}
	if !exists {
		t.Error("expected existing timer to be found")
	}
}

func TestTempRoleStateResetTimer(t *testing.T) {
	originalExpiry := time.Now().Add(time.Hour)
	st := &tempRoleState{
		Entries: []tempRoleEntry{
			{GuildID: 1, UserID: 10, RoleID: 100, ExpiryAt: originalExpiry},
			{GuildID: 1, UserID: 20, RoleID: 200, ExpiryAt: originalExpiry},
		},
	}

	// Simulate the reset logic from ensureTempRoleTimer(resetTimer=true)
	filtered := st.Entries[:0]
	for _, entry := range st.Entries {
		if !(entry.UserID == 10 && entry.RoleID == 100) {
			filtered = append(filtered, entry)
		}
	}
	newExpiry := time.Now().Add(2 * time.Hour)
	st.Entries = append(filtered, tempRoleEntry{
		GuildID: 1, UserID: 10, RoleID: 100, ExpiryAt: newExpiry,
	})

	if len(st.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(st.Entries))
	}
	// The other entry should be preserved
	if st.Entries[0].UserID != 20 {
		t.Errorf("first entry should be user 20, got %d", st.Entries[0].UserID)
	}
	// The reset entry should have the new expiry
	if st.Entries[1].UserID != 10 {
		t.Errorf("second entry should be user 10, got %d", st.Entries[1].UserID)
	}
	if !st.Entries[1].ExpiryAt.Equal(newExpiry) {
		t.Errorf("expiry = %v, want %v", st.Entries[1].ExpiryAt, newExpiry)
	}
}

// --- clearTimersForRole (logic from handleTempRoleConfigRemove) ---

func TestClearTimersForRole(t *testing.T) {
	st := &tempRoleState{
		Entries: []tempRoleEntry{
			{UserID: 1, RoleID: 100},
			{UserID: 2, RoleID: 200},
			{UserID: 3, RoleID: 100},
			{UserID: 4, RoleID: 300},
		},
	}

	var roleID snowflake.ID = 100
	filtered := make([]tempRoleEntry, 0, len(st.Entries))
	cleared := 0
	for _, entry := range st.Entries {
		if entry.RoleID == roleID {
			cleared++
			continue
		}
		filtered = append(filtered, entry)
	}
	st.Entries = filtered

	if cleared != 2 {
		t.Errorf("cleared = %d, want 2", cleared)
	}
	if len(st.Entries) != 2 {
		t.Fatalf("remaining = %d, want 2", len(st.Entries))
	}
	for _, entry := range st.Entries {
		if entry.RoleID == 100 {
			t.Error("role 100 should have been cleared")
		}
	}
}

func TestClearTimersForRoleNoneMatch(t *testing.T) {
	st := &tempRoleState{
		Entries: []tempRoleEntry{
			{UserID: 1, RoleID: 200},
			{UserID: 2, RoleID: 300},
		},
	}

	var roleID snowflake.ID = 100
	cleared := 0
	for _, entry := range st.Entries {
		if entry.RoleID == roleID {
			cleared++
		}
	}

	if cleared != 0 {
		t.Errorf("cleared = %d, want 0", cleared)
	}
}

// --- newRolesAdded (set-diff logic from checkTempRolesOnMemberUpdate) ---

func TestNewRolesDetection(t *testing.T) {
	oldRoleIDs := []snowflake.ID{100, 200, 300}
	newRoleIDs := []snowflake.ID{100, 200, 300, 400, 500}

	oldSet := make(map[snowflake.ID]struct{}, len(oldRoleIDs))
	for _, id := range oldRoleIDs {
		oldSet[id] = struct{}{}
	}

	var added []snowflake.ID
	for _, id := range newRoleIDs {
		if _, wasOld := oldSet[id]; !wasOld {
			added = append(added, id)
		}
	}

	if len(added) != 2 {
		t.Fatalf("added = %d, want 2", len(added))
	}
	if added[0] != 400 || added[1] != 500 {
		t.Errorf("added = %v, want [400, 500]", added)
	}
}

func TestNewRolesDetectionNoChange(t *testing.T) {
	roleIDs := []snowflake.ID{100, 200}

	oldSet := make(map[snowflake.ID]struct{}, len(roleIDs))
	for _, id := range roleIDs {
		oldSet[id] = struct{}{}
	}

	var added []snowflake.ID
	for _, id := range roleIDs {
		if _, wasOld := oldSet[id]; !wasOld {
			added = append(added, id)
		}
	}

	if len(added) != 0 {
		t.Errorf("added = %d, want 0", len(added))
	}
}

func TestNewRolesDetectionRoleRemoved(t *testing.T) {
	oldRoleIDs := []snowflake.ID{100, 200, 300}
	newRoleIDs := []snowflake.ID{100, 300} // 200 removed

	oldSet := make(map[snowflake.ID]struct{}, len(oldRoleIDs))
	for _, id := range oldRoleIDs {
		oldSet[id] = struct{}{}
	}

	var added []snowflake.ID
	for _, id := range newRoleIDs {
		if _, wasOld := oldSet[id]; !wasOld {
			added = append(added, id)
		}
	}

	if len(added) != 0 {
		t.Errorf("added = %d, want 0 (removals shouldn't appear as adds)", len(added))
	}
}
