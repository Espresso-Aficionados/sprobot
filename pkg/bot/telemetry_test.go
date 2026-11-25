package bot

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/sprobot"
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
