package bot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/sprobot"
	"github.com/sadbox/sprobot/pkg/testutil"
)

func newProfileNamesBot(t *testing.T) (*Bot, *testutil.FakeS3) {
	t.Helper()
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	b := &Bot{
		BaseBot:   &botutil.BaseBot{S3: testutil.NewTestS3Client(t, server), Log: testutil.DiscardLogger()},
		templates: map[snowflake.ID][]sprobot.Template{1: {sprobot.ProfileTemplate, sprobot.RoasterTemplate}},
	}
	return b, fake
}

func profileObject(tmpl sprobot.Template, userID string) string {
	return "/test-bucket/" + sprobot.ProfileS3Path("1", tmpl.Name, userID)
}

func storedProfile(t *testing.T, fake *testutil.FakeS3, tmpl sprobot.Template, userID string) map[string]string {
	t.Helper()
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	body, ok := fake.Objects[profileObject(tmpl, userID)]
	if !ok {
		return nil
	}
	var profile map[string]string
	if err := json.Unmarshal(body, &profile); err != nil {
		t.Fatal(err)
	}
	return profile
}

func objectCount(fake *testutil.FakeS3) int {
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	return len(fake.Objects)
}

func nameMessage(userID snowflake.ID, username string, nick *string) discord.Message {
	return discord.Message{
		Author: discord.User{ID: userID, Username: username},
		Member: &discord.Member{Nick: nick},
	}
}

// syncAndWait runs syncProfileName and waits for any update it started.
func syncAndWait(t *testing.T, b *Bot, msg discord.Message) *profileOwner {
	t.Helper()
	b.syncProfileName(1, msg)
	key := profileKey("1", msg.Author.ID.String())
	deadline := time.Now().Add(5 * time.Second)
	for {
		b.profileNames.mu.Lock()
		o := b.profileNames.owners[key]
		busy := o != nil && o.busy
		b.profileNames.mu.Unlock()
		if !busy {
			return o
		}
		if time.Now().After(deadline) {
			t.Fatal("name update did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSyncProfileNameUpdatesExistingProfiles(t *testing.T) {
	b, fake := newProfileNamesBot(t)
	tmpl := sprobot.ProfileTemplate
	fake.Objects[profileObject(tmpl, "2")] = []byte(`{"Grinder":"Niche"}`)

	syncAndWait(t, b, nameMessage(2, "alice", nil))
	if got := storedProfile(t, fake, tmpl, "2"); got[sprobot.DisplayNameKey] != "alice" || got["Grinder"] != "Niche" {
		t.Errorf("profile = %v, want name backfilled and fields kept", got)
	}
	if got := storedProfile(t, fake, sprobot.RoasterTemplate, "2"); got != nil {
		t.Errorf("created a profile the user never saved: %v", got)
	}

	nick := "Alice the Barista"
	o := syncAndWait(t, b, nameMessage(2, "alice", &nick))
	if got := storedProfile(t, fake, tmpl, "2")[sprobot.DisplayNameKey]; got != nick {
		t.Errorf("name = %q, want the nickname %q", got, nick)
	}
	if o.name != nick {
		t.Errorf("cached name = %q, want %q", o.name, nick)
	}
}

func TestSyncProfileNameSkipsUsersWithoutProfiles(t *testing.T) {
	b, fake := newProfileNamesBot(t)

	o := syncAndWait(t, b, nameMessage(3, "bob", nil))
	if !o.none {
		t.Error("user without profiles should be remembered as such")
	}
	if objectCount(fake) != 0 {
		t.Errorf("wrote objects for a user without profiles: %v", fake.Objects)
	}

	// Saving a profile makes them an owner again.
	b.profileSaved("1", "3", "bob")
	fake.Objects[profileObject(sprobot.ProfileTemplate, "3")] = []byte(`{"_display_name":"bob"}`)
	syncAndWait(t, b, nameMessage(3, "bobby", nil))
	if got := storedProfile(t, fake, sprobot.ProfileTemplate, "3")[sprobot.DisplayNameKey]; got != "bobby" {
		t.Errorf("name = %q, want bobby", got)
	}
}

func TestSyncProfileNameDoesNotRecreateDeletedProfile(t *testing.T) {
	b, fake := newProfileNamesBot(t)
	b.profileSaved("1", "4", "carol")
	b.profileDeleted("1", "4")

	syncAndWait(t, b, nameMessage(4, "carol2", nil))
	if objectCount(fake) != 0 {
		t.Errorf("recreated a deleted profile: %v", fake.Objects)
	}
}

func TestSyncProfileNameIgnoresOtherGuildsAndWebhooks(t *testing.T) {
	b, _ := newProfileNamesBot(t)
	b.syncProfileName(9, nameMessage(5, "dave", nil))
	webhook := nameMessage(6, "hook", nil)
	webhook.WebhookID = &webhook.Author.ID
	b.syncProfileName(1, webhook)
	if len(b.profileNames.owners) != 0 {
		t.Errorf("tracked %v", b.profileNames.owners)
	}
}

func TestMessageDisplayName(t *testing.T) {
	global := "Global"
	nick := "Nick"
	tests := []struct {
		msg  discord.Message
		want string
	}{
		{discord.Message{Author: discord.User{Username: "user"}}, "user"},
		{discord.Message{Author: discord.User{Username: "user", GlobalName: &global}, Member: &discord.Member{}}, "Global"},
		{discord.Message{Author: discord.User{Username: "user", GlobalName: &global}, Member: &discord.Member{Nick: &nick}}, "Nick"},
	}
	for _, tt := range tests {
		if got := messageDisplayName(tt.msg); got != tt.want {
			t.Errorf("messageDisplayName = %q, want %q", got, tt.want)
		}
	}
}

func TestProfileOwnersFromKeys(t *testing.T) {
	keys := []string{
		"profiles/1/Coffee Setup/2.json",
		"profiles/1/Roasting Setup/2.json",
		"profiles/1/Roasting Setup/3.json",
		"profiles/1/Retired Template/4.json",
		"profiles/1/Coffee Setup/notanid.json",
		"profiles/1/Coffee Setup/5.txt",
		"profiles/12/Coffee Setup/6.json",
	}
	got := profileOwnersFromKeys("1", keys, []string{"Coffee Setup", "Roasting Setup"})
	want := map[string][]string{"2": {"Coffee Setup", "Roasting Setup"}, "3": {"Roasting Setup"}}
	if len(got) != len(want) {
		t.Fatalf("owners = %v, want %v", got, want)
	}
	for user, tmpls := range want {
		if !slices.Equal(got[user], tmpls) {
			t.Errorf("owner %s templates = %v, want %v", user, got[user], tmpls)
		}
	}
}

// profileRequests returns the S3 requests made for userID's profiles.
func profileRequests(fake *testutil.FakeS3, userID string) []string {
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	var out []string
	for _, r := range fake.Requests {
		if strings.HasSuffix(r, "/"+userID+".json") {
			out = append(out, r)
		}
	}
	return out
}

func TestSyncProfileNameAfterListing(t *testing.T) {
	b, fake := newProfileNamesBot(t)
	fake.Objects[profileObject(sprobot.ProfileTemplate, "2")] = []byte(`{"Grinder":"Niche"}`)
	b.loadProfileOwners([]snowflake.ID{1})

	// An author without a profile costs no S3 reads.
	if o := syncAndWait(t, b, nameMessage(3, "bob", nil)); o == nil || !o.none {
		t.Errorf("owner = %+v, want known to have no profiles", o)
	}
	if got := profileRequests(fake, "3"); len(got) != 0 {
		t.Errorf("S3 requests for an author without profiles: %v", got)
	}

	// An owner's update touches only the template they have a profile in.
	syncAndWait(t, b, nameMessage(2, "alice", nil))
	if got := storedProfile(t, fake, sprobot.ProfileTemplate, "2")[sprobot.DisplayNameKey]; got != "alice" {
		t.Errorf("name = %q, want alice", got)
	}
	for _, r := range profileRequests(fake, "2") {
		if strings.Contains(r, sprobot.RoasterTemplate.Name) {
			t.Errorf("probed a template the owner has no profile in: %s", r)
		}
	}
}

func TestSyncProfileNameAfterListingAndDelete(t *testing.T) {
	b, fake := newProfileNamesBot(t)
	fake.Objects[profileObject(sprobot.ProfileTemplate, "4")] = []byte(`{}`)
	fake.Objects[profileObject(sprobot.RoasterTemplate, "4")] = []byte(`{}`)
	b.loadProfileOwners([]snowflake.ID{1})

	// Deleting one profile must not make the other look absent.
	fake.Mu.Lock()
	delete(fake.Objects, profileObject(sprobot.ProfileTemplate, "4"))
	fake.Mu.Unlock()
	b.profileDeleted("1", "4")
	syncAndWait(t, b, nameMessage(4, "dana", nil))

	if got := storedProfile(t, fake, sprobot.RoasterTemplate, "4")[sprobot.DisplayNameKey]; got != "dana" {
		t.Errorf("remaining profile's name = %q, want dana", got)
	}
}

func TestSyncProfileNameWhenListingFails(t *testing.T) {
	b, fake := newProfileNamesBot(t)
	fake.Fail["/test-bucket"] = http.StatusForbidden
	b.loadProfileOwners([]snowflake.ID{1})
	fake.Mu.Lock()
	delete(fake.Fail, "/test-bucket")
	fake.Objects[profileObject(sprobot.ProfileTemplate, "5")] = []byte(`{}`)
	fake.Mu.Unlock()

	// Without a listing every author is checked, as before.
	syncAndWait(t, b, nameMessage(5, "erin", nil))
	if got := storedProfile(t, fake, sprobot.ProfileTemplate, "5")[sprobot.DisplayNameKey]; got != "erin" {
		t.Errorf("name = %q, want erin", got)
	}
}
