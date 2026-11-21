package main

import (
	"os"
	"strings"
	"testing"

	"github.com/Espresso-Aficionados/sprobot/pkg/testutil"
)

func TestMain(m *testing.M) {
	testutil.RunMainIfAsked(main)
	os.Exit(m.Run())
}

// A bad environment exits 1 before the bot connects or writes anything,
// naming every problem and no value.
func TestBadEnvironmentExits(t *testing.T) {
	code, stderr := testutil.RunMain(t, []string{
		"STICKYBOT_DISCORD_TOKEN=sekrit token",
		"STICKYBOT_HEALTHCHECK_ENDPOINT=sekrit-not-a-url",
		"S3_ENDPOINT=https://s3.example.com/",
	})
	if code != 1 {
		t.Fatalf("exit code %d, want 1; stderr:\n%s", code, stderr)
	}
	for _, name := range []string{"STICKYBOT_DISCORD_TOKEN", "STICKYBOT_HEALTHCHECK_ENDPOINT", "S3_KEY", "S3_SECRET", "S3_BUCKET"} {
		if !strings.Contains(stderr, "problem=\""+name+" ") {
			t.Errorf("no problem logged for %s:\n%s", name, stderr)
		}
	}
	if strings.Contains(stderr, "sekrit") {
		t.Errorf("a value leaked:\n%s", stderr)
	}
}
