package s3client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/sadbox/sprobot/pkg/testutil"
)

func TestListKeysPages(t *testing.T) {
	fake := testutil.NewFakeS3()
	fake.PageSize = 2
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)

	want := []string{
		"profiles/1/Coffee Setup/10.json",
		"profiles/1/Coffee Setup/11.json",
		"profiles/1/Roasting Setup/10.json",
		"profiles/1/Roasting Setup/12.json",
		"profiles/1/Roasting Setup/13.json",
	}
	for _, key := range append(slices.Clone(want), "profiles/2/Coffee Setup/10.json", "images/1/x.png") {
		fake.Objects["/test-bucket/"+key] = []byte("{}")
	}

	got, err := c.ListKeys(context.Background(), "profiles/1/")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}

	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	if len(fake.Requests) != 3 {
		t.Errorf("made %d list requests, want 3 pages of up to 2", len(fake.Requests))
	}
}

func TestListKeysEmpty(t *testing.T) {
	server := httptest.NewServer(testutil.NewFakeS3())
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)

	got, err := c.ListKeys(context.Background(), "profiles/1/")
	if err != nil || len(got) != 0 {
		t.Errorf("ListKeys = %v, %v; want no keys and no error", got, err)
	}
}

func TestListKeysError(t *testing.T) {
	fake := testutil.NewFakeS3()
	fake.Fail["/test-bucket"] = http.StatusForbidden
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)

	if _, err := c.ListKeys(context.Background(), "profiles/1/"); err == nil {
		t.Error("ListKeys of a forbidden bucket returned no error")
	}
}
