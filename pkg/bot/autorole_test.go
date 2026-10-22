package bot

import (
	"net/http"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

func TestDefaultAutoRoleConfig(t *testing.T) {
	cfg := defaultAutoRoleConfig()
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

func messageFrom(userID snowflake.ID, roleIDs ...snowflake.ID) discord.Message {
	return discord.Message{
		Author: discord.User{ID: userID},
		Member: &discord.Member{RoleIDs: roleIDs},
	}
}

// A grant that keeps failing (here a 403 from the role hierarchy) is retried
// after a while, not on every message.
func TestEnsureAutoRoleBacksOffAfterFailure(t *testing.T) {
	b, api, _ := newRoleTestBot(t)
	b.autoRole.set(roleTestGuild, &autoRoleState{RoleID: roleTestMember})
	api.setRespond(func(string) (int, string) {
		return restError(http.StatusForbidden, 50013, "Missing Permissions")
	})

	b.ensureAutoRole(roleTestGuild, messageFrom(20))
	b.ensureAutoRole(roleTestGuild, messageFrom(20))
	if n := api.count(roleCall("PUT", 20, roleTestMember)); n != 1 {
		t.Errorf("%d grant attempts for one user, want 1", n)
	}

	b.ensureAutoRole(roleTestGuild, messageFrom(21))
	if n := api.count(roleCall("PUT", 21, roleTestMember)); n != 1 {
		t.Errorf("another user: %d grant attempts, want 1", n)
	}

}

func memberJoin(userID snowflake.ID) *events.GuildMemberJoin {
	return &events.GuildMemberJoin{GenericGuildMember: &events.GenericGuildMember{
		GuildID: roleTestGuild,
		Member:  discord.Member{GuildID: roleTestGuild, User: discord.User{ID: userID}},
	}}
}

// The role is checked again at grant time: it may have been edited into a
// moderator role, or be missing from the cache, since it was configured.
func TestEnsureAutoRoleRefusesUngrantableRole(t *testing.T) {
	const modRole, unknownRole snowflake.ID = 95, 96
	b, api, _ := newRoleTestBot(t)
	addTestRole(b, discord.Role{ID: modRole, Position: 5, Permissions: discord.PermissionManageMessages})

	for _, roleID := range []snowflake.ID{modRole, unknownRole} {
		b.autoRole.set(roleTestGuild, &autoRoleState{RoleID: roleID})
		b.ensureAutoRole(roleTestGuild, messageFrom(20))
		b.onMemberJoin(memberJoin(20))
		if n := api.count(roleCall("PUT", 20, roleID)); n != 0 {
			t.Errorf("role %d granted %d times", roleID, n)
		}
	}
}

func TestEnsureAutoRoleSkipsMembersWithTheRole(t *testing.T) {
	b, api, _ := newRoleTestBot(t)
	b.autoRole.set(roleTestGuild, &autoRoleState{RoleID: roleTestMember})
	b.ensureAutoRole(roleTestGuild, messageFrom(20, roleTestMember))
	b.ensureAutoRole(roleTestGuild, messageFrom(21))
	if n := api.count(roleCall("PUT", 20, roleTestMember)); n != 0 {
		t.Errorf("granted the auto-role to a member who has it")
	}
	if n := api.count(roleCall("PUT", 21, roleTestMember)); n != 1 {
		t.Errorf("%d grants to a member without the role, want 1", n)
	}
}
