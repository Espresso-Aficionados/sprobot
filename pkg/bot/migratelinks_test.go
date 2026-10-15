package bot

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/sprobot"
	"github.com/sadbox/sprobot/pkg/testutil"
)

const migrateTestName = "abcdefghijABCDEFGHIJ0123456789"

// newTestMigration returns a migration whose bucket holds one profile with an
// image (user 2, now a PNG), one without (user 3), and one whose image is now
// external (user 5).
func newTestMigration(t *testing.T) (*linkMigration, string) {
	t.Helper()
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	tmpl := sprobot.ProfileTemplate
	profiles := map[string]string{
		"2": `{"` + tmpl.Image.Name + `":"images/1/Coffee Setup/2.png","` + sprobot.ImageVersionKey + `":"7"}`,
		"3": `{}`,
		"5": `{"` + tmpl.Image.Name + `":"https://example.com/me.png"}`,
	}
	for user, body := range profiles {
		fake.Objects["/test-bucket/"+sprobot.ProfileS3Path("1", tmpl.Name, user)] = []byte(body)
	}

	b := &Bot{
		BaseBot:   &botutil.BaseBot{S3: testutil.NewTestS3Client(t, server), Log: testutil.DiscardLogger()},
		templates: map[snowflake.ID][]sprobot.Template{1: {tmpl}},
	}
	m := &linkMigration{b: b, ctx: context.Background(), guildID: 1, profiles: map[string]string{}}
	return m, server.URL + "/test-bucket/"
}

func TestMigratePlanModLogEntry(t *testing.T) {
	m, bucket := newTestMigration(t)
	old := bucket + "mod_files/1/" + migrateTestName + "..png"
	presigned := bucket + "mod_files/1/" + migrateTestName + ".mov?X-Amz-Expires=172800&X-Amz-Signature=abc"
	msg := discord.Message{Embeds: []discord.Embed{{
		Type:   discord.EmbedTypeRich,
		Title:  "Message from @a to #b",
		Footer: &discord.EmbedFooter{Text: "archived on behalf of @mod", IconURL: "https://cdn.discordapp.com/avatars/1/x.png"},
		Fields: []discord.EmbedField{
			{Value: "message text"},
			{Name: "Attachments", Value: old + " " + presigned},
			{Name: "Link to Message", Value: "[Click here](https://discord.com/channels/1/2/3)"},
		},
	}}}

	update, ok := m.plan(msg)
	if !ok {
		t.Fatal("expected an edit")
	}
	if update.Content != nil {
		t.Error("content should be left alone")
	}
	got := (*update.Embeds)[0]
	want := sprobot.WebEndpoint + "files/mod/1/" + migrateTestName + "..png " +
		sprobot.WebEndpoint + "files/mod/1/" + migrateTestName + ".mov"
	if got.Fields[1].Value != want {
		t.Errorf("Attachments = %q, want %q", got.Fields[1].Value, want)
	}
	if got.Title != msg.Embeds[0].Title || got.Fields[2].Value != msg.Embeds[0].Fields[2].Value || got.Footer.IconURL != msg.Embeds[0].Footer.IconURL {
		t.Errorf("unrelated parts changed: %+v", got)
	}
	if m.files != 2 {
		t.Errorf("files = %d, want 2", m.files)
	}
}

func TestMigratePlanProfileAndAvatar(t *testing.T) {
	m, bucket := newTestMigration(t)
	avatar := strings.TrimSuffix(bucket, "test-bucket/") + "76916743.gif"
	msg := discord.Message{Embeds: []discord.Embed{{
		Image:  &discord.EmbedResource{URL: bucket + "images/1/Coffee%20Setup/2.jpg?X-Amz-Expires=300", ProxyURL: "https://media.discordapp.net/x"},
		Footer: &discord.EmbedFooter{Text: "sprobot", IconURL: avatar},
	}}}

	update, ok := m.plan(msg)
	if !ok {
		t.Fatal("expected an edit")
	}
	got := (*update.Embeds)[0]
	// The profile's current image, as /getprofile would embed it.
	if want := sprobot.WebEndpoint + "profiles/1/Coffee%20Setup/2/image.png?v=7"; got.Image.URL != want {
		t.Errorf("image = %q, want %q", got.Image.URL, want)
	}
	if got.Image.ProxyURL != "" {
		t.Error("read-only embed fields should not be sent back")
	}
	if want := sprobot.WebEndpoint + sprobot.AvatarWebPath; got.Footer.IconURL != want {
		t.Errorf("footer icon = %q, want %q", got.Footer.IconURL, want)
	}
}

func TestMigratePlanExternalProfileImage(t *testing.T) {
	m, bucket := newTestMigration(t)
	msg := discord.Message{Embeds: []discord.Embed{{Image: &discord.EmbedResource{URL: bucket + "images/1/Coffee%20Setup/5.jpg"}}}}

	update, ok := m.plan(msg)
	if !ok {
		t.Fatal("expected an edit")
	}
	if got := (*update.Embeds)[0].Image.URL; got != "https://example.com/me.png" {
		t.Errorf("image = %q, want the profile's external image", got)
	}
}

func TestMigratePlanSkips(t *testing.T) {
	m, bucket := newTestMigration(t)
	avatar := strings.TrimSuffix(bucket, "test-bucket/") + "76916743.gif"
	tests := map[string]discord.Message{
		"avatar only": {Embeds: []discord.Embed{{Footer: &discord.EmbedFooter{Text: "sprobot", IconURL: avatar}}}},
		"profile without image": {Embeds: []discord.Embed{{
			Image: &discord.EmbedResource{URL: bucket + "images/1/Coffee%20Setup/3.jpg"},
		}}},
		"no profile": {Embeds: []discord.Embed{{
			Image: &discord.EmbedResource{URL: bucket + "images/1/Coffee%20Setup/4.jpg"},
		}}},
		"unknown template": {Embeds: []discord.Embed{{
			Image: &discord.EmbedResource{URL: bucket + "images/1/Nope/2.jpg"},
		}}},
		"external":           {Content: "https://example.com/a.png"},
		"other guild's file": {Content: bucket + "mod_files/9/" + migrateTestName + ".png"},
		"other guild's profile": {Embeds: []discord.Embed{{
			Image: &discord.EmbedResource{URL: bucket + "images/9/Coffee%20Setup/2.jpg"},
		}}},
	}
	for name, msg := range tests {
		if _, ok := m.plan(msg); ok {
			t.Errorf("%s: should not be edited", name)
		}
	}
	if m.missingProfiles != 3 {
		t.Errorf("missingProfiles = %d, want 3", m.missingProfiles)
	}
}

func TestMigratePlanContent(t *testing.T) {
	m, bucket := newTestMigration(t)
	link := bucket + "shortcut_files/1/" + migrateTestName + ".png"
	unknown := bucket + "profiles/1/x.json"
	msg := discord.Message{
		Content: "see " + link + " and <" + unknown + ">",
		Embeds: []discord.Embed{
			// Generated from the content link; Discord rebuilds it.
			{Type: discord.EmbedTypeImage, Thumbnail: &discord.EmbedResource{URL: link}},
		},
	}

	update, ok := m.plan(msg)
	if !ok {
		t.Fatal("expected an edit")
	}
	want := "see " + sprobot.WebEndpoint + "files/shortcut/1/" + migrateTestName + ".png and <" + unknown + ">"
	if update.Content == nil || *update.Content != want {
		t.Errorf("content = %v, want %q", update.Content, want)
	}
	if update.Embeds != nil {
		t.Error("generated embeds should not be sent back")
	}
	if m.unknownCount != 1 || !strings.Contains(m.unknown[0], unknown) {
		t.Errorf("unknown = %d %v, want the profile JSON link", m.unknownCount, m.unknown)
	}
}

func TestTruncateReport(t *testing.T) {
	if got := truncateReport("short", 2000); got != "short" {
		t.Errorf("short report changed: %q", got)
	}
	long := strings.Repeat("line of text\n", 300)
	got := truncateReport(long, 2000)
	if len(got) > 2000 || !strings.HasSuffix(got, "\n…") || strings.Contains(got, "line of t\n") {
		t.Errorf("truncated report: len %d, tail %q", len(got), got[len(got)-20:])
	}
}
