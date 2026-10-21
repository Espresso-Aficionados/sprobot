package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
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

// Every duration /config temprole add offers must be one the handler accepts,
// and be labelled with the duration it sets.
func TestTempRoleDurationChoicesAreAccepted(t *testing.T) {
	b, api, _ := newRoleTestBot(t)
	b.TestGuildIDsOverride = []snowflake.ID{roleTestGuild}
	api.setRespond(func(string) (int, string) { return http.StatusOK, "[]" })
	if err := b.registerAllCommands(); err != nil {
		t.Fatal(err)
	}

	type option struct {
		Name    string   `json:"name"`
		Options []option `json:"options"`
		Choices []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"choices"`
	}
	find := func(opts []option, name string) option {
		t.Helper()
		for _, o := range opts {
			if o.Name == name {
				return o
			}
		}
		t.Fatalf("option %q not registered", name)
		return option{}
	}
	var commands []option
	api.mu.Lock()
	body := api.bodies[fmt.Sprintf("PUT /applications/0/guilds/%d/commands", roleTestGuild)]
	api.mu.Unlock()
	if err := json.Unmarshal(body, &commands); err != nil {
		t.Fatalf("decoding registered commands: %v", err)
	}
	add := find(find(find(commands, "config").Options, "temprole").Options, "add")
	choices := find(add.Options, "duration").Choices
	if len(choices) == 0 {
		t.Fatal("no duration choices")
	}
	for _, c := range choices {
		d, ok := tempRoleDurations[c.Value]
		if !ok {
			t.Errorf("choice %q (%s) is rejected as an invalid duration", c.Name, c.Value)
			continue
		}
		if got := formatDuration(d); got != c.Name {
			t.Errorf("choice %q sets %s", c.Name, got)
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

// --- tempRoleState ---

func timerUsers(entries []tempRoleEntry) []snowflake.ID {
	var ids []snowflake.ID
	for _, e := range entries {
		ids = append(ids, e.UserID)
	}
	return ids
}

func TestTempRoleStateExpired(t *testing.T) {
	now := time.Now()
	st := &tempRoleState{Entries: []tempRoleEntry{
		{UserID: 1, RoleID: 10, ExpiryAt: now.Add(-time.Hour)},
		{UserID: 2, RoleID: 20, ExpiryAt: now.Add(time.Hour)},
		{UserID: 3, RoleID: 30, ExpiryAt: now.Add(-time.Minute)},
		{UserID: 4, RoleID: 40, ExpiryAt: now.Add(time.Minute)},
	}}

	if got := timerUsers(st.expired(now)); !slices.Equal(got, []snowflake.ID{1, 3}) {
		t.Errorf("expired users = %v, want [1 3]", got)
	}
	if len(st.Entries) != 4 {
		t.Errorf("expired removed entries from the state: %d left", len(st.Entries))
	}
	if got := st.expired(now.Add(-2 * time.Hour)); len(got) != 0 {
		t.Errorf("nothing has expired yet, got %v", timerUsers(got))
	}
}

func TestTempRoleStateAdd(t *testing.T) {
	now := time.Now()
	st := &tempRoleState{Entries: []tempRoleEntry{
		{UserID: 10, RoleID: 100, ExpiryAt: now.Add(-time.Minute)}, // expired, removal pending
		{UserID: 20, RoleID: 200, ExpiryAt: now.Add(time.Hour)},
	}}

	// A message from user 10 must not replace the timer whose role is being
	// removed.
	if st.add(tempRoleEntry{UserID: 10, RoleID: 100, ExpiryAt: now.Add(time.Hour)}, false) {
		t.Error("add without reset replaced an existing timer")
	}
	if !st.Entries[0].ExpiryAt.Equal(now.Add(-time.Minute)) {
		t.Error("existing timer changed")
	}

	// /temprole resets it.
	fresh := now.Add(2 * time.Hour)
	if !st.add(tempRoleEntry{UserID: 10, RoleID: 100, ExpiryAt: fresh}, true) {
		t.Fatal("reset didn't add a timer")
	}
	if got := timerUsers(st.Entries); !slices.Equal(got, []snowflake.ID{20, 10}) {
		t.Fatalf("users = %v, want [20 10]", got)
	}
	if !st.Entries[1].ExpiryAt.Equal(fresh) {
		t.Errorf("reset timer expires %v, want %v", st.Entries[1].ExpiryAt, fresh)
	}

	if !st.add(tempRoleEntry{UserID: 10, RoleID: 300, ExpiryAt: fresh}, false) {
		t.Error("a second role for the same user wasn't added")
	}
}

func TestTempRoleStateClearRole(t *testing.T) {
	st := &tempRoleState{Entries: []tempRoleEntry{
		{UserID: 1, RoleID: 100},
		{UserID: 2, RoleID: 200},
		{UserID: 3, RoleID: 100},
	}}
	if n := st.clearRole(100); n != 2 {
		t.Errorf("cleared %d, want 2", n)
	}
	if got := timerUsers(st.Entries); !slices.Equal(got, []snowflake.ID{2}) {
		t.Errorf("users left = %v, want [2]", got)
	}
	if n := st.clearRole(999); n != 0 {
		t.Errorf("cleared %d for an unknown role", n)
	}
}

// finish matches on expiry too: a timer /temprole reset while the old one's
// removal was in flight survives.
func TestTempRoleStateFinishKeepsResetTimer(t *testing.T) {
	now := time.Now()
	old := tempRoleEntry{UserID: 1, RoleID: 100, ExpiryAt: now.Add(-time.Minute)}
	st := &tempRoleState{Entries: []tempRoleEntry{old, {UserID: 2, RoleID: 100, ExpiryAt: now.Add(-time.Minute)}}}
	done := st.expired(now)
	st.add(tempRoleEntry{UserID: 1, RoleID: 100, ExpiryAt: now.Add(time.Hour)}, true)

	st.finish(done)
	if len(st.Entries) != 1 || st.Entries[0].UserID != 1 || !st.Entries[0].ExpiryAt.After(now) {
		t.Errorf("entries = %+v, want only user 1's reset timer", st.Entries)
	}
}

// --- addedRoleIDs ---

func TestAddedRoleIDs(t *testing.T) {
	tests := []struct {
		name        string
		before, now []snowflake.ID
		want        []snowflake.ID
	}{
		{"two added", []snowflake.ID{100, 200, 300}, []snowflake.ID{100, 200, 300, 400, 500}, []snowflake.ID{400, 500}},
		{"no change", []snowflake.ID{100, 200}, []snowflake.ID{100, 200}, nil},
		{"one removed", []snowflake.ID{100, 200, 300}, []snowflake.ID{100, 300}, nil},
		{"removed and added", []snowflake.ID{100}, []snowflake.ID{200}, []snowflake.ID{200}},
		{"no old roles", nil, []snowflake.ID{100}, []snowflake.ID{100}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := addedRoleIDs(tt.before, tt.now); !slices.Equal(got, tt.want) {
				t.Errorf("addedRoleIDs = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- expiry and grants against a fake Discord API ---

const tempTestRole snowflake.ID = 400

// newTempRoleTestBot returns a test bot with tempTestRole configured as a
// one-hour temp role.
func newTempRoleTestBot(t *testing.T) (*Bot, *roleAPI) {
	t.Helper()
	b, api, _ := newRoleTestBot(t)
	addTestRole(b, discord.Role{ID: tempTestRole, Position: 5, Permissions: discord.PermissionSendMessages})
	b.tempRoleConfig.set(roleTestGuild, &tempRoleConfigState{Roles: []tempRoleConfigEntry{{RoleID: tempTestRole, Duration: time.Hour}}})
	b.tempRoles.set(roleTestGuild, &tempRoleState{})
	return b, api
}

// An expired entry used to be dropped before RemoveMemberRole, so a failed
// removal left the role on a user forever. It now stays until the removal
// succeeds or Discord says the member or role is gone.
func TestProcessTempRoleExpiriesRetriesFailedRemovals(t *testing.T) {
	b, api := newTempRoleTestBot(t)
	st := b.tempRoles.get(roleTestGuild)
	past := time.Now().Add(-time.Minute)
	for _, u := range []snowflake.ID{1, 2, 3, 4} {
		st.Entries = append(st.Entries, tempRoleEntry{GuildID: roleTestGuild, UserID: u, RoleID: tempTestRole, ExpiryAt: past})
	}
	st.Entries = append(st.Entries, tempRoleEntry{GuildID: roleTestGuild, UserID: 5, RoleID: tempTestRole, ExpiryAt: time.Now().Add(time.Hour)})

	api.setRespond(func(call string) (int, string) {
		switch call {
		case roleCall("DELETE", 2, tempTestRole):
			return restError(http.StatusNotFound, 10007, "Unknown Member")
		case roleCall("DELETE", 3, tempTestRole):
			return restError(http.StatusNotFound, 10011, "Unknown Role")
		case roleCall("DELETE", 4, tempTestRole):
			return restError(http.StatusForbidden, 50013, "Missing Permissions")
		}
		return http.StatusNoContent, ""
	})
	b.processTempRoleExpiries()

	if got := timerUsers(st.Entries); !slices.Equal(got, []snowflake.ID{4, 5}) {
		t.Fatalf("users left = %v, want the failed removal (4) and the live timer (5)", got)
	}
	for _, u := range []snowflake.ID{1, 2, 3, 4} {
		if n := api.count(roleCall("DELETE", u, tempTestRole)); n != 1 {
			t.Errorf("user %d: %d removals, want 1", u, n)
		}
	}

	// A message from user 4 meanwhile doesn't start a new timer.
	if _, created := b.ensureTempRoleTimer(roleTestGuild, 4, tempTestRole, false); created {
		t.Error("a message re-created the timer of a role being removed")
	}

	// The next tick retries, and the removal now succeeds.
	api.setRespond(nil)
	b.processTempRoleExpiries()
	if got := timerUsers(st.Entries); !slices.Equal(got, []snowflake.ID{5}) {
		t.Errorf("users left = %v, want [5]", got)
	}
	if n := api.count(roleCall("DELETE", 4, tempTestRole)); n != 2 {
		t.Errorf("user 4: %d removals, want 2", n)
	}
}

// A temp role is checked at grant time like the other granted roles, and a
// role the cache doesn't know is refused rather than assumed safe.
func TestEnsureTempRoleTimerRefusesUngrantableRole(t *testing.T) {
	b, _ := newTempRoleTestBot(t)
	addTestRole(b, discord.Role{ID: tempTestRole, Position: 5, Permissions: discord.PermissionBanMembers})
	if _, created := b.ensureTempRoleTimer(roleTestGuild, 1, tempTestRole, true); created {
		t.Error("timer created for a moderator role")
	}

	b.Client.Caches.RemoveRole(roleTestGuild, tempTestRole)
	if _, created := b.ensureTempRoleTimer(roleTestGuild, 1, tempTestRole, true); created {
		t.Error("timer created for a role missing from the cache")
	}
	if n := len(b.tempRoles.get(roleTestGuild).Entries); n != 0 {
		t.Errorf("%d timers stored", n)
	}
}

// tempRoleCommand is /temprole <user> <role>.
func tempRoleCommand(userID, roleID snowflake.ID) string {
	return fmt.Sprintf(`[{"name":"user","type":6,"value":"%d"},{"name":"role","type":3,"value":"%d"}]`, userID, roleID)
}

func TestTempRoleRefusesRoleEditedIntoModeratorRole(t *testing.T) {
	b, api := newTempRoleTestBot(t)
	addTestRole(b, discord.Role{ID: tempTestRole, Position: 5, Permissions: discord.PermissionKickMembers})
	resolved := discord.ResolvedData{Users: map[snowflake.ID]discord.User{20: {ID: 20, Username: "member"}}}
	e, responses := roleCommand(t, roleTestOwner, nil, tempRoleCommand(20, tempTestRole), resolved)
	b.handleTempRole(e)
	if got := response(t, responses).Content; !strings.Contains(got, "moderator permissions") {
		t.Errorf("response = %q", got)
	}
	if n := api.count(roleCall("PUT", 20, tempTestRole)); n != 0 {
		t.Errorf("role granted %d times", n)
	}
}

// /temprole posts publicly; it must not ping everyone who has the role.
func TestTempRoleAnnouncementPingsOnlyTheUser(t *testing.T) {
	b, api := newTempRoleTestBot(t)
	resolved := discord.ResolvedData{Users: map[snowflake.ID]discord.User{20: {ID: 20, Username: "member"}}}
	e, responses := roleCommand(t, roleTestOwner, nil, tempRoleCommand(20, tempTestRole), resolved)
	b.handleTempRole(e)
	if n := api.count(roleCall("PUT", 20, tempTestRole)); n != 1 {
		t.Fatalf("%d grants, want 1", n)
	}
	msg := response(t, responses)
	if !strings.Contains(msg.Content, fmt.Sprintf("<@&%d>", tempTestRole)) {
		t.Errorf("content = %q, want the role named", msg.Content)
	}
	am := msg.AllowedMentions
	if am == nil {
		t.Fatal("no allowed mentions: the role would be pinged")
	}
	if len(am.Parse) != 0 || len(am.Roles) != 0 || !slices.Equal(am.Users, []snowflake.ID{20}) {
		t.Errorf("allowed mentions = %+v, want only user 20", *am)
	}
}

func TestTempRoleConfigAddRejectsModeratorRole(t *testing.T) {
	b, _ := newTempRoleTestBot(t)
	// Ban Members used to pass: only Manage Messages was refused.
	target := discord.Role{ID: 401, GuildID: roleTestGuild, Position: 5, Permissions: discord.PermissionBanMembers}
	opts := `[{"name":"temprole","type":2,"options":[{"name":"add","type":1,"options":[{"name":"role","type":8,"value":"401"},{"name":"duration","type":3,"value":"1d"}]}]}]`
	e, responses := roleCommand(t, roleTestOwner, nil, opts, resolvedRole(target))
	b.handleTempRoleConfig(e)
	if got := response(t, responses).Content; !strings.Contains(got, "moderator permissions") {
		t.Errorf("response = %q", got)
	}
	if _, ok := b.tempRoleConfig.get(roleTestGuild).find(401); ok {
		t.Error("moderator role configured as a temp role")
	}
}
