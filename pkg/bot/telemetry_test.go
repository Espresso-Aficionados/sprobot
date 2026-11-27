package bot

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/sprobot"
	"github.com/Espresso-Aficionados/sprobot/pkg/telemetrytest"
)

// A periodic save's job fails when a guild's state can't be written, and
// succeeds again once it can.
func TestGuildStateStoreSaveReportsFailure(t *testing.T) {
	store, h := newTestStore(t)
	if err := store.load(t.Context(), []snowflake.ID{testGuild}); err != nil {
		t.Fatal(err)
	}
	store.getOrCreate(testGuild)

	h.setHook(func(r *http.Request) bool { return r.Method != http.MethodPut })
	if err := store.save(t.Context()); err == nil {
		t.Error("save succeeded while S3 refuses writes")
	}
	h.setHook(nil)
	if err := store.save(t.Context()); err != nil {
		t.Errorf("save after recovery: %v", err)
	}
}

// Every loop's job name is distinct: two loops on one Job would mix their
// series.
func TestLoopJobsDistinct(t *testing.T) {
	seen := map[any]bool{}
	for _, j := range []any{
		retryFailedLoadsJob, processTempRoleExpiriesJob, saveTopPostersJob, savePosterRoleJob,
		saveShortcutsJob, saveTicketsJob, saveWelcomeJob, saveCachesJob, saveStarboardJob,
		retryStarboardDeletesJob, saveRenameLogsJob, saveTempRolesJob, pruneCorruptBackupsJob,
		forumRemindersJob,
	} {
		if seen[j] {
			t.Errorf("job %v used twice", j)
		}
		seen[j] = true
	}
}

// The custom IDs sprobot really generates are labelled by a listed prefix,
// never "other", and never with an ID or user text in the label.
func TestInteractionLabelsOfRealCustomIDs(t *testing.T) {
	const id snowflake.ID = 726985544038612993
	tmpl := sprobot.AllTemplates()[id][0]
	components := []string{
		"ticket_open", "ticket_close", "del_cancel",
		ticketCloseCustomID("ticket_close_yes", id), ticketCloseCustomID("ticket_close_no", id),
		fmt.Sprintf("selfrole_%d", id),
		"del_profile_" + tmpl.ShortName, "del_image_" + tmpl.ShortName,
		"del_confirm_profile_" + tmpl.ShortName, "del_confirm_image_" + tmpl.ShortName,
	}
	modals := []string{
		"edit_" + tmpl.ShortName, "sconfig_set_my 2 shortcut", "welcome_set", fmt.Sprintf("modlog_%d_%d", id, id),
	}
	check := func(ids, names []string) {
		for _, customID := range ids {
			got := botutil.CustomIDLabel(customID, names)
			if !slices.Contains(names, got) || strings.ContainsAny(got, "0123456789 ") {
				t.Errorf("custom ID %q labelled %q", customID, got)
			}
		}
	}
	check(components, componentNames)
	check(modals, modalNames)
}

// statPoints collects the gauge called name, by the value of label key; it
// fails on any other label.
func statPoints(t *testing.T, rec *telemetrytest.Recorder, name, key string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, p := range rec.Points(t, name) {
		for _, kv := range p.Attrs.ToSlice() {
			if string(kv.Key) != key {
				t.Errorf("%s has label %s=%q", name, kv.Key, kv.Value.String())
			}
		}
		out[p.Get(key)] = p.Value
	}
	return out
}

// The gauges count the served guilds' state only, profiles once the guild's
// listing is in, and never carry an ID.
func TestStatsGauges(t *testing.T) {
	rec := telemetrytest.Install(t)
	b, fake := newProfileNamesBot(t)
	b.initStateStores()
	b.guildCache = newCappedCache[discord.CacheGuild](10)
	b.TestGuildIDsOverride = []snowflake.ID{1}
	reg, err := b.registerStats()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Unregister() })

	if got := statPoints(t, rec, "sprobot.profiles", "template"); len(got) != 0 {
		t.Errorf("profiles before the listing = %v, want none", got)
	}
	fake.Objects[profileObject(sprobot.ProfileTemplate, "2")] = []byte(`{}`)
	fake.Objects[profileObject(sprobot.ProfileTemplate, "3")] = []byte(`{}`)
	fake.Objects[profileObject(sprobot.ProfileTemplate, "5")] = []byte(`{}`)
	// Deleted after the listing was taken: the listing mustn't bring it back.
	b.profileStored("1", "profile", "5", false)
	b.loadProfileOwners(t.Context(), []snowflake.ID{1})
	if got := statPoints(t, rec, "sprobot.profiles", "template"); got["profile"] != 2 || got["roaster"] != 0 || len(got) != 2 {
		t.Errorf("profiles after the listing = %v, want profile 2, roaster 0", got)
	}
	b.profileStored("1", "roaster", "2", true)
	b.profileStored("1", "profile", "3", false)
	if got := statPoints(t, rec, "sprobot.profiles", "template"); got["profile"] != 1 || got["roaster"] != 1 {
		t.Errorf("profiles after a save and a delete = %v, want 1 each", got)
	}

	sb := b.starboard.getOrCreate(1)
	sb.Entries[10] = starboardEntry{Count: 5, StarboardMsgID: 11}
	sb.Entries[12] = starboardEntry{Count: 1}
	sb.Entries[13] = starboardEntry{StarboardMsgID: 14, SourceDeleted: true}
	b.tempRoles.getOrCreate(1).Entries = []tempRoleEntry{{GuildID: 1, UserID: 2, RoleID: 3}, {GuildID: 1, UserID: 4, RoleID: 3}}
	// Loaded, but not a guild this instance is in.
	b.starboard.getOrCreate(2).Entries[20] = starboardEntry{StarboardMsgID: 21}
	b.tempRoles.getOrCreate(2).Entries = []tempRoleEntry{{GuildID: 2, UserID: 2, RoleID: 3}}

	if got := statPoints(t, rec, "sprobot.starboard.entries", "state"); got["posted"] != 1 || got["pending"] != 1 {
		t.Errorf("starboard entries = %v, want posted 1, pending 1", got)
	}
	if got := statPoints(t, rec, "sprobot.temp_roles", ""); got[""] != 2 {
		t.Errorf("temp roles = %v, want 2", got)
	}

	if got := statPoints(t, rec, "sprobot.members", ""); len(got) != 0 {
		t.Errorf("members before the guild is cached = %v, want none", got)
	}
	b.guildCache.Put(1, discord.CacheGuild{Guild: discord.Guild{ID: 1}, MemberCount: 1234})
	b.guildCache.Put(2, discord.CacheGuild{Guild: discord.Guild{ID: 2}, MemberCount: 5}) // not served
	if got := statPoints(t, rec, "sprobot.members", ""); got[""] != 1234 {
		t.Errorf("members = %v, want 1234", got)
	}
}

// The member count needs every guild cached, and a guild at all.
func TestMemberCount(t *testing.T) {
	guilds := newCappedCache[discord.CacheGuild](10)
	guilds.Put(1, discord.CacheGuild{MemberCount: 100})
	guilds.Put(2, discord.CacheGuild{MemberCount: 20})
	if n, ok := memberCount(guilds, []snowflake.ID{1, 2}); !ok || n != 120 {
		t.Errorf("memberCount = %d, %v; want 120, true", n, ok)
	}
	if _, ok := memberCount(guilds, []snowflake.ID{1, 3}); ok {
		t.Error("counted with a guild missing from the cache")
	}
	if _, ok := memberCount(guilds, nil); ok {
		t.Error("counted without a guild")
	}
}

// A gauge over a store is left out while a served guild's state failed to
// load, rather than reading short.
func TestStatsGaugesSkipUnloaded(t *testing.T) {
	rec := telemetrytest.Install(t)
	b, _ := newProfileNamesBot(t)
	b.initStateStores()
	b.guildCache = newCappedCache[discord.CacheGuild](10)
	b.TestGuildIDsOverride = []snowflake.ID{1}
	b.tempRoles.failed[1] = true
	reg, err := b.registerStats()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Unregister() })
	if got := statPoints(t, rec, "sprobot.temp_roles", ""); len(got) != 0 {
		t.Errorf("temp roles while unloaded = %v, want none", got)
	}
}

// Edits and deletes are counted by template short name and outcome; the
// counters' fixed series exist from the start.
func TestProfileCounters(t *testing.T) {
	rec := telemetrytest.Install(t)
	count := func(name string, labels map[string]string) float64 {
		t.Helper()
	points:
		for _, p := range rec.Points(t, name) {
			for k, v := range labels {
				if p.Get(k) != v {
					continue points
				}
			}
			return p.Value
		}
		t.Fatalf("%s has no series %v", name, labels)
		return 0
	}
	failure := map[string]string{"template": "roaster", "outcome": "failure"}
	before := count("sprobot.profile.edits", failure)
	recordProfileEdit(sprobot.RoasterTemplate, errors.New("S3 down"))
	if got := count("sprobot.profile.edits", failure); got != before+1 {
		t.Errorf("failed edits = %v, want %v", got, before+1)
	}

	image := map[string]string{"template": "profile", "target": "image", "outcome": "success"}
	before = count("sprobot.profile.deletes", image)
	b, fake := newProfileNamesBot(t)
	putStored(t, fake, sprobot.ProfileS3Path("1", sprobot.ProfileTemplate.Name, "2"), []byte(`{"Gear Picture":"images/1/Coffee Setup/2.png"}`))
	_, err := b.deleteProfileImage(t.Context(), sprobot.ProfileTemplate, "1", "2")
	recordProfileDelete(sprobot.ProfileTemplate, "image", err)
	if got := count("sprobot.profile.deletes", image); got != before+1 {
		t.Errorf("image deletes = %v, want %v", got, before+1)
	}
	if stored, ok := b.profileNames.stored[storedProfileKey{"1", "profile", "2"}]; !ok || stored {
		t.Error("deleting the only field left the profile counted as stored")
	}
}

// The shortcut label takes the first names it is given, then "other".
func TestShortcutLabelCap(t *testing.T) {
	c := &labelCap{max: 2}
	for name, want := range map[string]string{"espresso": "espresso", "grinders": "grinders"} {
		if got := c.label(name); got != want {
			t.Errorf("label(%q) = %q, want %q", name, got, want)
		}
	}
	if got := c.label("third"); got != "other" {
		t.Errorf("label past the cap = %q, want other", got)
	}
	if got := c.label("espresso"); got != "espresso" {
		t.Errorf("label of a name already given = %q, want espresso", got)
	}
}
