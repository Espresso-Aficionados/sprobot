package s3client_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Espresso-Aficionados/sprobot/pkg/testutil"
)

// Production's endpoint is the bucket's own hostname. Requests are path-style,
// so every key is stored under "{bucket}/"; listings and copies have to agree
// with what the object requests store.
func TestBucketHostEndpoint(t *testing.T) {
	fake := testutil.NewFakeS3()
	fake.BucketHost = testutil.TestBucketHost
	fake.PageSize = 2
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3ClientBucketHost(t, server)
	ctx := context.Background()

	src := "sticky_files/10/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png"
	fake.Mu.Lock()
	fake.Objects["/test-bucket/test-bucket/"+src] = []byte("img")
	fake.Mu.Unlock()
	for _, guildID := range []string{"10", "11", "12"} {
		if err := c.SaveGuildJSON(ctx, "stickies", guildID, []byte("{}")); err != nil {
			t.Fatalf("SaveGuildJSON: %v", err)
		}
	}

	sticky, err := c.CopyStickyFile(ctx, "10", src)
	if err != nil {
		t.Fatalf("CopyStickyFile: %v", err)
	}
	shortcut, err := c.CopyShortcutFile(ctx, "10", src)
	if err != nil {
		t.Fatalf("CopyShortcutFile: %v", err)
	}

	tests := []struct {
		prefix string
		want   []string
	}{
		{"stickies/", []string{"stickies/10.json", "stickies/11.json", "stickies/12.json"}},
		{"sticky_files/10/", []string{src, sticky}},
		{"shortcut_files/10/", []string{shortcut}},
		{"profiles/", nil},
	}
	for _, tt := range tests {
		objects, err := c.ListObjects(ctx, tt.prefix)
		if err != nil {
			t.Fatalf("ListObjects(%q): %v", tt.prefix, err)
		}
		var got []string
		for _, o := range objects {
			got = append(got, o.Key)
		}
		slices.Sort(tt.want)
		if !slices.Equal(got, tt.want) {
			t.Errorf("ListObjects(%q) = %q, want %q", tt.prefix, got, tt.want)
		}
	}

	// Listed keys are the keys the other calls take.
	if err := c.DeleteKeys(ctx, sticky, shortcut); err != nil {
		t.Fatalf("DeleteKeys: %v", err)
	}
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	for _, key := range []string{sticky, shortcut} {
		if _, ok := fake.Objects["/test-bucket/test-bucket/"+key]; ok {
			t.Errorf("%s is still stored after DeleteKeys", key)
		}
	}
}
