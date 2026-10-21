package s3client_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sadbox/sprobot/pkg/sprobot"
	"github.com/sadbox/sprobot/pkg/testutil"
)

func TestCopyShortcutFile(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)

	src := "shortcut_files/11/dddddddddddddddddddddddddddddd..png" // a doubled dot, as the old bot saved
	fake.Mu.Lock()
	fake.Objects["/test-bucket/"+src] = []byte("img")
	fake.Mu.Unlock()

	got, err := c.CopyShortcutFile(context.Background(), "10", src)
	if err != nil {
		t.Fatalf("CopyShortcutFile: %v", err)
	}
	if kind, guildID, name, ok := sprobot.ParseFileKey(got); !ok || kind != "shortcut" || guildID != "10" || !strings.HasSuffix(name, ".png") || strings.Contains(name, "..") {
		t.Errorf("copied to %q, want a new guild-10 shortcut file ending .png", got)
	}
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	if string(fake.Objects["/test-bucket/"+got]) != "img" || fake.Objects["/test-bucket/"+src] == nil {
		t.Error("want the bytes copied and the source kept")
	}
}

func TestCopyShortcutFileRejects(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)

	tests := []struct{ name, guildID, src string }{
		{"missing source", "10", "mod_files/10/cccccccccccccccccccccccccccccc.png"},
		{"not a saved file", "10", "templates/10.json"},
		{"bad guild", "../10", "mod_files/10/cccccccccccccccccccccccccccccc.png"},
	}
	for _, tt := range tests {
		if got, err := c.CopyShortcutFile(context.Background(), tt.guildID, tt.src); err == nil {
			t.Errorf("%s: copied to %q, want an error", tt.name, got)
		}
	}
}
