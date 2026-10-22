package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	disgobot "github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/testutil"
)

const (
	roleTestGuild  snowflake.ID = 1013566342345019512 // dev guild
	roleTestOwner  snowflake.ID = 1
	roleTestSelf   snowflake.ID = 7  // the bot's user
	roleTestBotTop snowflake.ID = 70 // the bot's role, position 50
	roleTestMember snowflake.ID = 80 // a plain member role, position 1
)

// roleAPI is a fake Discord API for the role features: member role add and
// remove, the message search, and command registration. It records every
// call as "METHOD /path".
type roleAPI struct {
	mu      sync.Mutex
	calls   []string
	bodies  map[string][]byte
	respond func(call string) (int, string) // nil answers 204
}

func (f *roleAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	call := r.Method + " " + r.URL.Path
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.bodies[call] = body
	respond := f.respond
	f.mu.Unlock()

	status, out := http.StatusNoContent, ""
	if respond != nil {
		status, out = respond(call)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(out))
}

func (f *roleAPI) setRespond(fn func(call string) (int, string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond = fn
}

func (f *roleAPI) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (f *roleAPI) countSuffix(suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasSuffix(c, suffix) {
			n++
		}
	}
	return n
}

func roleCall(method string, userID, roleID snowflake.ID) string {
	return fmt.Sprintf("%s /guilds/%d/members/%d/roles/%d", method, roleTestGuild, userID, roleID)
}

func restError(status, code int, message string) (int, string) {
	return status, fmt.Sprintf(`{"message":%q,"code":%d}`, message, code)
}

// redirectTransport sends every request to target, keeping its path, so the
// bot's hard-coded discord.com search URL reaches the fake.
type redirectTransport struct{ target *url.URL }

func (t redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme = t.target.Scheme
	r.URL.Host = t.target.Host
	r.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// newRoleTestBot returns a bot wired to a fake Discord API and a fake S3, with
// the role features' stores and a role cache holding @everyone, the bot's own
// role (position 50) and a plain member role (position 1).
func newRoleTestBot(t *testing.T) (*Bot, *roleAPI, *testutil.FakeS3) {
	t.Helper()
	api := &roleAPI{bodies: make(map[string][]byte)}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	fakeS3 := testutil.NewFakeS3()
	s3srv := httptest.NewServer(fakeS3)
	t.Cleanup(s3srv.Close)
	s3 := testutil.NewTestS3Client(t, s3srv)
	log := testutil.DiscardLogger()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	b := &Bot{
		BaseBot:      &botutil.BaseBot{S3: s3, Log: log},
		searchClient: &http.Client{Transport: redirectTransport{target}},
		autoRole: newGuildStateStore(s3, log, "autorole",
			func() *autoRoleState { return &autoRoleState{} },
			func(st *autoRoleState) *sync.Mutex { return &st.mu }),
		posterRole: newGuildStateStore(s3, log, "posterroles",
			func() *posterRoleState {
				return &posterRoleState{Counts: make(map[string]int), Fetched: make(map[string]bool)}
			},
			func(st *posterRoleState) *sync.Mutex { return &st.mu }),
		tempRoleConfig: newGuildStateStore(s3, log, "temproleconfig",
			func() *tempRoleConfigState { return &tempRoleConfigState{} },
			func(st *tempRoleConfigState) *sync.Mutex { return &st.mu }),
		tempRoles: newGuildStateStore(s3, log, "temproles",
			func() *tempRoleState { return &tempRoleState{} },
			func(st *tempRoleState) *sync.Mutex { return &st.mu }),
		modLog: newGuildStateStore(s3, log, "modlog",
			func() *modLogState { return &modLogState{} },
			func(st *modLogState) *sync.Mutex { return &st.mu }),
		eventLog: newGuildStateStore(s3, log, "eventlog",
			func() *eventLogState { return &eventLogState{} },
			func(st *eventLogState) *sync.Mutex { return &st.mu }),
		welcome: newGuildStateStore(s3, log, "welcome",
			func() *welcomeState { return &welcomeState{} },
			func(st *welcomeState) *sync.Mutex { return &st.mu }),
	}
	b.Client = &disgobot.Client{
		Rest:   rest.New(rest.NewClient("test", rest.WithURL(srv.URL), rest.WithLogger(log))),
		Caches: cache.New(cache.WithCaches(cache.FlagGuilds | cache.FlagRoles | cache.FlagMembers)),
	}
	c := b.Client.Caches
	c.AddGuild(discord.Guild{ID: roleTestGuild, OwnerID: roleTestOwner})
	c.SetSelfUser(discord.OAuth2User{User: discord.User{ID: roleTestSelf}})
	addTestRole(b, discord.Role{ID: roleTestGuild, Permissions: discord.PermissionViewChannel})
	addTestRole(b, discord.Role{ID: roleTestBotTop, Position: 50, Permissions: discord.PermissionManageRoles})
	addTestRole(b, discord.Role{ID: roleTestMember, Position: 1, Permissions: discord.PermissionSendMessages})
	c.AddMember(discord.Member{GuildID: roleTestGuild, User: discord.User{ID: roleTestSelf}, RoleIDs: []snowflake.ID{roleTestBotTop}})
	return b, api, fakeS3
}

func addTestRole(b *Bot, role discord.Role) {
	role.GuildID = roleTestGuild
	b.Client.Caches.AddRole(role)
}

// roleCommand builds a /config interaction in roleTestGuild from member userID
// holding roleIDs, with resolved holding its option values. Its responses go
// to the returned channel.
func roleCommand(t *testing.T, userID snowflake.ID, roleIDs []snowflake.ID, options string, resolved discord.ResolvedData) (*events.ApplicationCommandInteractionCreate, <-chan discord.MessageCreate) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": "1", "application_id": "2", "type": 2, "token": "tok",
		"guild_id": roleTestGuild.String(),
		"channel":  map[string]any{"id": "500", "type": 0, "guild_id": roleTestGuild.String(), "name": "mods"},
		"member": map[string]any{
			"user":        map[string]any{"id": userID.String(), "username": "mod"},
			"roles":       roleIDs,
			"joined_at":   "2024-01-01T00:00:00Z",
			"permissions": "0",
		},
		"data": map[string]any{
			"id": "3", "name": "config", "type": 1,
			"options":  json.RawMessage(options),
			"resolved": resolved,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var i discord.ApplicationCommandInteraction
	if err := json.Unmarshal(raw, &i); err != nil {
		t.Fatalf("decoding interaction: %v", err)
	}
	responses := make(chan discord.MessageCreate, 4)
	return &events.ApplicationCommandInteractionCreate{
		ApplicationCommandInteraction: i,
		Respond: func(_ discord.InteractionResponseType, d discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			m, _ := d.(discord.MessageCreate)
			responses <- m
			return nil
		},
	}, responses
}

func response(t *testing.T, responses <-chan discord.MessageCreate) discord.MessageCreate {
	t.Helper()
	select {
	case m := <-responses:
		return m
	default:
		t.Fatal("no response")
		return discord.MessageCreate{}
	}
}

// waitFor polls cond until it holds, failing the test after five seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRoleGrantBlocked(t *testing.T) {
	const guild snowflake.ID = 100
	// Every permission the maintainer listed must block a grant on its own.
	for _, p := range []discord.Permissions{
		discord.PermissionAdministrator,
		discord.PermissionManageGuild,
		discord.PermissionManageRoles,
		discord.PermissionManageChannels,
		discord.PermissionManageWebhooks,
		discord.PermissionBanMembers,
		discord.PermissionKickMembers,
		discord.PermissionModerateMembers,
		discord.PermissionManageMessages,
		discord.PermissionManageThreads,
		discord.PermissionManageNicknames,
		discord.PermissionManageGuildExpressions,
		discord.PermissionManageEvents,
		discord.PermissionMentionEveryone,
		discord.PermissionViewAuditLog,
	} {
		role := discord.Role{ID: 5, Permissions: discord.PermissionSendMessages | p}
		if roleGrantBlocked(guild, role) == "" {
			t.Errorf("a role with %s is grantable", p)
		}
	}

	if got := roleGrantBlocked(guild, discord.Role{ID: guild}); got == "" {
		t.Error("@everyone is grantable")
	}
	if got := roleGrantBlocked(guild, discord.Role{ID: 5, Managed: true}); got == "" {
		t.Error("a managed role is grantable")
	}
	plain := discord.Role{ID: 5, Permissions: discord.PermissionSendMessages | discord.PermissionViewChannel | discord.PermissionAttachFiles}
	if got := roleGrantBlocked(guild, plain); got != "" {
		t.Errorf("a plain role is refused: %s", got)
	}
	got := roleGrantBlocked(guild, discord.Role{ID: 5, Permissions: discord.PermissionBanMembers | discord.PermissionManageMessages})
	if !strings.Contains(got, "Ban Members, Manage Messages") {
		t.Errorf("reason %q doesn't name the permissions in order", got)
	}
}

func TestHighestRole(t *testing.T) {
	const guild snowflake.ID = 100
	if got := highestRole(guild, nil); got.ID != guild {
		t.Errorf("no roles: highest = %d, want @everyone", got.ID)
	}
	roles := []discord.Role{{ID: 11, Position: 3}, {ID: 12, Position: 9}, {ID: 13, Position: 4}}
	if got := highestRole(guild, roles); got.ID != 12 {
		t.Errorf("highest = %d, want 12", got.ID)
	}
	// Equal positions: the older role (lower ID) ranks higher.
	tied := []discord.Role{{ID: 21, Position: 5}, {ID: 20, Position: 5}}
	if got := highestRole(guild, tied); got.ID != 20 {
		t.Errorf("tie: highest = %d, want 20", got.ID)
	}
}

func TestValidateGrantableRole(t *testing.T) {
	const guild snowflake.ID = 100
	target := discord.Role{ID: 30, Position: 10, Permissions: discord.PermissionSendMessages}
	botTop := discord.Role{ID: 40, Position: 20}
	modTop := discord.Role{ID: 50, Position: 15}
	low := discord.Role{ID: 60, Position: 5}

	tests := []struct {
		name   string
		role   discord.Role
		c      roleGrantConfigurer
		reject string // substring of the reason, "" for allowed
	}{
		{"mod with Manage Roles above the role", target,
			roleGrantConfigurer{BotTop: botTop, Perms: discord.PermissionManageRoles, InvokerTop: modTop}, ""},
		{"administrator above the role", target,
			roleGrantConfigurer{BotTop: botTop, Perms: discord.PermissionAdministrator, InvokerTop: modTop}, ""},
		{"Manage Messages only", target,
			roleGrantConfigurer{BotTop: botTop, Perms: discord.PermissionManageMessages, InvokerTop: modTop}, "Manage Roles"},
		{"invoker's top role below the role", target,
			roleGrantConfigurer{BotTop: botTop, Perms: discord.PermissionManageRoles, InvokerTop: low}, "below your highest role"},
		{"invoker's top role is the role", target,
			roleGrantConfigurer{BotTop: botTop, Perms: discord.PermissionManageRoles, InvokerTop: target}, "below your highest role"},
		{"owner without roles", target,
			roleGrantConfigurer{BotTop: botTop, IsOwner: true, InvokerTop: discord.Role{ID: guild}}, ""},
		{"bot's top role below the role", target,
			roleGrantConfigurer{BotTop: low, IsOwner: true}, "bot's highest role"},
		{"bot's top role at the same position, newer", target,
			roleGrantConfigurer{BotTop: discord.Role{ID: 31, Position: 10}, IsOwner: true}, "bot's highest role"},
		{"owner granting a moderator role",
			discord.Role{ID: 30, Position: 10, Permissions: discord.PermissionKickMembers},
			roleGrantConfigurer{BotTop: botTop, IsOwner: true}, "moderator permissions"},
		{"@everyone", discord.Role{ID: guild},
			roleGrantConfigurer{BotTop: botTop, IsOwner: true}, "@everyone"},
		{"managed role", discord.Role{ID: 30, Position: 10, Managed: true},
			roleGrantConfigurer{BotTop: botTop, IsOwner: true}, "managed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateGrantableRole(guild, tt.role, tt.c)
			if tt.reject == "" && got != "" {
				t.Errorf("refused: %s", got)
			}
			if tt.reject != "" && !strings.Contains(got, tt.reject) {
				t.Errorf("reason = %q, want it to mention %q", got, tt.reject)
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	var r retryAfter
	now := time.Now()
	if !r.ready(1, now) {
		t.Fatal("a user with no failure isn't ready")
	}
	r.fail(1, now, time.Minute)
	if r.ready(1, now.Add(59*time.Second)) {
		t.Error("ready before the wait ran out")
	}
	if !r.ready(1, now.Add(time.Minute)) {
		t.Error("not ready once the wait ran out")
	}
	if !r.ready(2, now) {
		t.Error("one user's failure held off another")
	}
	r.clear(1)
	if !r.ready(1, now) {
		t.Error("not ready after clear")
	}

	// Expired entries are dropped once the map grows large.
	for i := range 1000 {
		r.fail(snowflake.ID(1000+i), now, time.Minute)
	}
	r.fail(1, now.Add(time.Hour), time.Minute)
	if len(r.until) != 1 {
		t.Errorf("%d entries left, want only the fresh one", len(r.until))
	}
}

func resolvedRole(role discord.Role) discord.ResolvedData {
	return discord.ResolvedData{Roles: map[snowflake.ID]discord.Role{role.ID: role}}
}

// autoroleSet is /config autorole set <role>.
func autoroleSet(roleID snowflake.ID) string {
	return fmt.Sprintf(`[{"name":"autorole","type":2,"options":[{"name":"set","type":1,"options":[{"name":"role","type":8,"value":"%d"}]}]}]`, roleID)
}

// The config commands are open to Manage Messages, so the role checks run
// against the invoker's and the bot's roles from the role cache.
func TestConfigRoleChecksUseRoleCache(t *testing.T) {
	const (
		modRole    snowflake.ID = 90 // Manage Roles, position 20
		helperRole snowflake.ID = 91 // Manage Messages only, position 30
		modUser    snowflake.ID = 9
	)
	b, _, _ := newRoleTestBot(t)
	addTestRole(b, discord.Role{ID: modRole, Position: 20, Permissions: discord.PermissionManageRoles})
	addTestRole(b, discord.Role{ID: helperRole, Position: 30, Permissions: discord.PermissionManageMessages})
	b.autoRole.set(roleTestGuild, &autoRoleState{RoleID: roleTestMember})

	plain := func(id snowflake.ID, pos int) discord.Role {
		return discord.Role{ID: id, GuildID: roleTestGuild, Position: pos, Permissions: discord.PermissionSendMessages}
	}
	tests := []struct {
		name    string
		user    snowflake.ID
		roles   []snowflake.ID
		target  discord.Role
		allowed bool
	}{
		{"mod below the bot", modUser, []snowflake.ID{modRole}, plain(200, 10), true},
		{"helper without Manage Roles", modUser, []snowflake.ID{helperRole}, plain(200, 10), false},
		{"mod below the role", modUser, []snowflake.ID{modRole}, plain(200, 25), false},
		{"owner with no roles", roleTestOwner, nil, plain(200, 10), true},
		{"owner, role above the bot", roleTestOwner, nil, plain(200, 60), false},
		{"moderator role", roleTestOwner, nil, discord.Role{ID: 200, GuildID: roleTestGuild, Position: 10, Permissions: discord.PermissionBanMembers}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := b.autoRole.get(roleTestGuild)
			st.RoleID = roleTestMember
			e, responses := roleCommand(t, tt.user, tt.roles, autoroleSet(tt.target.ID), resolvedRole(tt.target))
			b.handleAutoRoleConfig(e)
			resp := response(t, responses)
			if got := st.RoleID == tt.target.ID; got != tt.allowed {
				t.Errorf("auto-role set = %v, want %v (response %q)", got, tt.allowed, resp.Content)
			}
		})
	}
}

// The bot's own member can fall out of the capped member cache. Its roles are
// then fetched, and if that fails too, nothing is configured.
func TestCheckGrantableRoleWithoutCachedBotMember(t *testing.T) {
	b, api, _ := newRoleTestBot(t)
	b.Client.Caches.RemoveMember(roleTestGuild, roleTestSelf)
	selfCall := fmt.Sprintf("GET /guilds/%d/members/%d", roleTestGuild, roleTestSelf)
	target := discord.Role{ID: 200, GuildID: roleTestGuild, Position: 1, Permissions: discord.PermissionSendMessages}

	api.setRespond(func(string) (int, string) { return restError(http.StatusInternalServerError, 0, "oops") })
	b.autoRole.set(roleTestGuild, &autoRoleState{})
	e, responses := roleCommand(t, roleTestOwner, nil, autoroleSet(200), resolvedRole(target))
	b.handleAutoRoleConfig(e)
	resp := response(t, responses)
	if got := b.autoRole.get(roleTestGuild).RoleID; got != 0 {
		t.Errorf("auto-role set to %d without knowing the bot's roles (response %q)", got, resp.Content)
	}

	api.setRespond(func(call string) (int, string) {
		if call == selfCall {
			return http.StatusOK, fmt.Sprintf(`{"user":{"id":"%d","username":"sprobot"},"roles":["%d"],"joined_at":"2024-01-01T00:00:00Z"}`, roleTestSelf, roleTestBotTop)
		}
		return http.StatusNoContent, ""
	})
	e, responses = roleCommand(t, roleTestOwner, nil, autoroleSet(200), resolvedRole(target))
	b.handleAutoRoleConfig(e)
	resp = response(t, responses)
	if got := b.autoRole.get(roleTestGuild).RoleID; got != 200 {
		t.Errorf("auto-role = %d after fetching the bot's member, want 200 (response %q)", got, resp.Content)
	}
	if n := api.count(selfCall); n != 2 {
		t.Errorf("%d fetches of the bot's member, want 2", n)
	}
}
