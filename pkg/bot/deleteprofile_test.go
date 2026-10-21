package bot

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sadbox/sprobot/pkg/sprobot"
	"github.com/sadbox/sprobot/pkg/testutil"
)

func putStored(t *testing.T, fake *testutil.FakeS3, key string, body []byte) {
	t.Helper()
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	fake.Objects["/test-bucket/"+key] = body
}

func hasObject(fake *testutil.FakeS3, key string) bool {
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	_, ok := fake.Objects["/test-bucket/"+key]
	return ok
}

func TestDeleteProfileImageKeepsFields(t *testing.T) {
	b, fake := newProfileNamesBot(t)
	tmpl := sprobot.ProfileTemplate
	body, _ := json.Marshal(map[string]string{
		"Machine": "Rocket", "Gear Picture": "images/1/Coffee Setup/2.png", sprobot.ImageVersionKey: "v1",
	})
	putStored(t, fake, sprobot.ProfileS3Path("1", tmpl.Name, "2"), body)
	putStored(t, fake, "images/1/Coffee Setup/2.png", []byte("png"))
	putStored(t, fake, "images/1/Coffee Setup/3.png", []byte("another user"))

	if got := b.deleteProfileImage(context.Background(), tmpl, "1", "2"); got != "Deleted!" {
		t.Fatalf("reply = %q, want Deleted!", got)
	}

	if hasObject(fake, "images/1/Coffee Setup/2.png") {
		t.Error("the image object should be deleted, not just unlinked")
	}
	if !hasObject(fake, "images/1/Coffee Setup/3.png") {
		t.Error("another user's image was deleted")
	}
	profile := storedProfile(t, fake, tmpl, "2")
	if profile["Machine"] != "Rocket" {
		t.Errorf("stored profile = %v, want its fields kept", profile)
	}
	if _, ok := profile["Gear Picture"]; ok {
		t.Error("stored profile still points at the image")
	}
	if _, ok := profile[sprobot.ImageVersionKey]; ok {
		t.Error("stored profile keeps an image version with no image")
	}
}

func TestDeleteProfileImageOnlyImageDeletesProfile(t *testing.T) {
	b, fake := newProfileNamesBot(t)
	tmpl := sprobot.ProfileTemplate
	body, _ := json.Marshal(map[string]string{"Gear Picture": "images/1/Coffee Setup/2.webp"})
	putStored(t, fake, sprobot.ProfileS3Path("1", tmpl.Name, "2"), body)
	putStored(t, fake, "images/1/Coffee Setup/2.webp", []byte("webp"))

	if got := b.deleteProfileImage(context.Background(), tmpl, "1", "2"); got != "Deleted!" {
		t.Fatalf("reply = %q, want Deleted!", got)
	}
	if n := objectCount(fake); n != 0 {
		t.Errorf("%d objects left, want the profile and its image both deleted", n)
	}
}
