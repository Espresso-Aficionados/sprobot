package s3client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sadbox/sprobot/pkg/testutil"
)

func TestListObjectsFollowsPages(t *testing.T) {
	fake := testutil.NewFakeS3()
	fake.PageSize = 2
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)

	modified := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fake.Mu.Lock()
	for _, key := range []string{"images/1/a.png", "images/1/b.png", "images/2/c.png", "images/2/d.png", "images/3/e e.png"} {
		fake.Objects["/test-bucket/"+key] = []byte(key)
		fake.Modified["/test-bucket/"+key] = modified
	}
	fake.Objects["/test-bucket/profiles/1/x.json"] = []byte("{}")
	fake.Mu.Unlock()

	got, err := c.ListObjects(context.Background(), "images/")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	want := []string{"images/1/a.png", "images/1/b.png", "images/2/c.png", "images/2/d.png", "images/3/e e.png"}
	if len(got) != len(want) {
		t.Fatalf("ListObjects returned %d objects, want %d: %+v", len(got), len(want), got)
	}
	for i, o := range got {
		if o.Key != want[i] || o.Size != int64(len(want[i])) || !o.LastModified.Equal(modified) {
			t.Errorf("object %d = %+v, want key %q, size %d, modified %s", i, o, want[i], len(want[i]), modified)
		}
	}
}

func TestListObjectsError(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)

	fake.Mu.Lock()
	fake.Fail["/test-bucket"] = http.StatusForbidden
	fake.Mu.Unlock()

	if _, err := c.ListObjects(context.Background(), "images/"); err == nil {
		t.Error("ListObjects with a failing listing = nil, want error")
	}
}
