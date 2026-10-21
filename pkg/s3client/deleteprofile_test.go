package s3client_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
	"github.com/sadbox/sprobot/pkg/testutil"
)

const (
	profileJSON = "/test-bucket/profiles/1/Coffee Setup/2.json"
	imagePNG    = "/test-bucket/images/1/Coffee Setup/2.png"
)

func newDeleteTest(t *testing.T, imageField string) (*testutil.FakeS3, *s3client.Client) {
	t.Helper()
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	c := testutil.NewTestS3Client(t, server)

	profile := map[string]string{"Machine": "Rocket"}
	if imageField != "" {
		profile["Gear Picture"] = imageField
	}
	body, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	fake.Objects[profileJSON] = body
	for _, key := range []string{
		imagePNG,
		"/test-bucket/images/1/Coffee Setup/2.jpg",    // left behind by an earlier upload
		"/test-bucket/images/1/Coffee Setup/3.png",    // another user
		"/test-bucket/images/1/Roasting Setup/2.png",  // the user's other profile
		"/test-bucket/images/9/Coffee Setup/2.png",    // the user in another guild
		"/test-bucket/stickies/1.json",                // not an image at all
		"/test-bucket/profiles/1/Coffee Setup/3.json", // another user's profile
	} {
		fake.Objects[key] = []byte("x")
	}
	return fake, c
}

func remaining(fake *testutil.FakeS3) map[string]bool {
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	left := make(map[string]bool, len(fake.Objects))
	for key := range fake.Objects {
		left[key] = true
	}
	return left
}

func TestDeleteProfileDeletesItsImages(t *testing.T) {
	fake, c := newDeleteTest(t, "images/1/Coffee Setup/2.png")

	if err := c.DeleteProfile(context.Background(), sprobot.ProfileTemplate, "1", "2"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}

	left := remaining(fake)
	for _, gone := range []string{profileJSON, imagePNG, "/test-bucket/images/1/Coffee Setup/2.jpg"} {
		if left[gone] {
			t.Errorf("%s should be deleted", gone)
		}
	}
	if len(left) != 5 {
		t.Errorf("objects left = %v, want only other users', profiles' and guilds' data", left)
	}
}

// An image stored under a key no upload uses today (an old bucket URL with a
// .jpeg extension) is found through the profile.
func TestDeleteProfileDeletesStoredImageKey(t *testing.T) {
	fake, c := newDeleteTest(t, "")
	server := c.Origin()
	fake.Mu.Lock()
	fake.Objects["/test-bucket/images/1/Coffee Setup/2.jpeg"] = []byte("x")
	body, _ := json.Marshal(map[string]string{"Gear Picture": server + "/test-bucket/images%2F1%2FCoffee%20Setup%2F2.jpeg"})
	fake.Objects[profileJSON] = body
	fake.Mu.Unlock()

	if err := c.DeleteProfile(context.Background(), sprobot.ProfileTemplate, "1", "2"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if remaining(fake)["/test-bucket/images/1/Coffee Setup/2.jpeg"] {
		t.Error("the image the profile pointed at should be deleted")
	}
}

// A profile's image field pointing at someone else's object never gets that
// object deleted.
func TestDeleteProfileKeepsForeignKeys(t *testing.T) {
	for _, field := range []string{"images/1/Coffee Setup/3.png", "stickies/1.json", "profiles/1/Coffee Setup/3.json"} {
		fake, c := newDeleteTest(t, field)
		if err := c.DeleteProfile(context.Background(), sprobot.ProfileTemplate, "1", "2"); err != nil {
			t.Fatalf("DeleteProfile: %v", err)
		}
		if !remaining(fake)["/test-bucket/"+field] {
			t.Errorf("image field %q: DeleteProfile deleted an object the user doesn't own", field)
		}
	}
}

func TestDeleteProfileImageFailureKeepsProfile(t *testing.T) {
	fake, c := newDeleteTest(t, "images/1/Coffee Setup/2.png")
	fake.Mu.Lock()
	fake.Fail[imagePNG] = http.StatusForbidden
	fake.Mu.Unlock()

	if err := c.DeleteProfile(context.Background(), sprobot.ProfileTemplate, "1", "2"); err == nil {
		t.Fatal("DeleteProfile with a failing image delete = nil, want error")
	}
	if !remaining(fake)[profileJSON] {
		t.Error("the profile must stay until its image is gone, so deleting again can finish")
	}
}

// A profile that can't be read is still deleted, with its uploads.
func TestDeleteProfileUnreadableProfile(t *testing.T) {
	fake, c := newDeleteTest(t, "")
	fake.Mu.Lock()
	fake.Objects[profileJSON] = []byte("not json")
	fake.Mu.Unlock()

	if err := c.DeleteProfile(context.Background(), sprobot.ProfileTemplate, "1", "2"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if left := remaining(fake); left[profileJSON] || left[imagePNG] {
		t.Error("the profile and its upload should be deleted")
	}
}

// With no profile left, deleting still clears any image an earlier version left.
func TestDeleteProfileWithoutProfileClearsImages(t *testing.T) {
	fake, c := newDeleteTest(t, "")
	fake.Mu.Lock()
	delete(fake.Objects, profileJSON)
	fake.Mu.Unlock()

	if err := c.DeleteProfile(context.Background(), sprobot.ProfileTemplate, "1", "2"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if left := remaining(fake); left[imagePNG] || left["/test-bucket/images/1/Coffee Setup/2.jpg"] {
		t.Error("orphaned uploads should be deleted")
	}
}

func TestDeleteProfileImageKeepsProfile(t *testing.T) {
	fake, c := newDeleteTest(t, "images/1/Coffee Setup/2.png")
	profile, err := c.FetchProfileSimple(context.Background(), "1", "Coffee Setup", "2")
	if err != nil {
		t.Fatal(err)
	}

	if err := c.DeleteProfileImage(context.Background(), sprobot.ProfileTemplate, "1", "2", profile); err != nil {
		t.Fatalf("DeleteProfileImage: %v", err)
	}
	left := remaining(fake)
	if left[imagePNG] || left["/test-bucket/images/1/Coffee Setup/2.jpg"] {
		t.Error("the user's uploads should be deleted")
	}
	if !left[profileJSON] || len(left) != 6 {
		t.Errorf("objects left = %v, want the profile and everyone else's data", left)
	}
}
