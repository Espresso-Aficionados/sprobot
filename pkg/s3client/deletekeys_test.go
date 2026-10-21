package s3client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sadbox/sprobot/pkg/testutil"
)

func TestDeleteKeys(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)

	fake.Mu.Lock()
	fake.Objects["/test-bucket/sticky_files/1/a.png"] = []byte("a")
	fake.Objects["/test-bucket/sticky_files/1/b.png"] = []byte("b")
	fake.Mu.Unlock()

	if err := c.DeleteKeys(context.Background(), "sticky_files/1/a.png", "", "sticky_files/1/b.png"); err != nil {
		t.Fatalf("DeleteKeys: %v", err)
	}

	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	if len(fake.Objects) != 0 {
		t.Errorf("objects left after delete: %v", fake.Objects)
	}
	// The empty key is skipped, not sent (the SDK rejects it).
	want := []string{"/test-bucket/sticky_files/1/a.png", "/test-bucket/sticky_files/1/b.png"}
	if !slices.Equal(fake.Deleted, want) {
		t.Errorf("deleted %v, want %v", fake.Deleted, want)
	}
}

func TestDeleteKeysNotFoundIsSuccess(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)

	// Some S3-compatible stores answer a delete of a missing key with 404.
	fake.Mu.Lock()
	fake.Fail["/test-bucket/gone.png"] = http.StatusNotFound
	fake.Mu.Unlock()

	if err := c.DeleteKeys(context.Background(), "gone.png", "never-existed.png"); err != nil {
		t.Errorf("DeleteKeys of missing keys = %v, want nil", err)
	}
}

func TestDeleteKeysJoinsFailures(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)

	fake.Mu.Lock()
	fake.Objects["/test-bucket/ok.png"] = []byte("x")
	fake.Fail["/test-bucket/denied1.png"] = http.StatusForbidden
	fake.Fail["/test-bucket/denied2.png"] = http.StatusForbidden
	fake.Mu.Unlock()

	err := c.DeleteKeys(context.Background(), "denied1.png", "ok.png", "denied2.png")
	if err == nil {
		t.Fatal("DeleteKeys with failing keys = nil, want error")
	}
	for _, key := range []string{"denied1.png", "denied2.png"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not name %s", err, key)
		}
	}

	// A failure doesn't stop the remaining keys from being deleted.
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	if _, ok := fake.Objects["/test-bucket/ok.png"]; ok {
		t.Error("ok.png was not deleted after an earlier failure")
	}
}
