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
// naming every problem and no value. The retired SPROBOT_HEALTHCHECK_ENDPOINT
// is no problem, whatever its value.
func TestBadEnvironmentExits(t *testing.T) {
	code, stderr := testutil.RunMain(t, []string{
		"SPROBOT_DISCORD_TOKEN=sekrit token",
		"SPROBOT_HEALTHCHECK_ENDPOINT=sekrit-not-a-url",
		"S3_ENDPOINT=https://s3.example.com/",
	})
	if code != 1 {
		t.Fatalf("exit code %d, want 1; stderr:\n%s", code, stderr)
	}
	for _, name := range []string{"SPROBOT_DISCORD_TOKEN", "S3_KEY", "S3_SECRET", "S3_BUCKET"} {
		if !strings.Contains(stderr, "problem=\""+name+" ") {
			t.Errorf("no problem logged for %s:\n%s", name, stderr)
		}
	}
	if strings.Contains(stderr, "SPROBOT_HEALTHCHECK_ENDPOINT") {
		t.Errorf("the retired SPROBOT_HEALTHCHECK_ENDPOINT was named:\n%s", stderr)
	}
	if strings.Contains(stderr, "sekrit") {
		t.Errorf("a value leaked:\n%s", stderr)
	}
}

// -check-state needs only the storage environment, not the Discord token.
func TestCheckStateFlags(t *testing.T) {
	code, stderr := testutil.RunMain(t, nil, "-check-state")
	if code != 1 || !strings.Contains(stderr, "S3_KEY is not set") || strings.Contains(stderr, "SPROBOT_DISCORD_TOKEN") {
		t.Errorf("-check-state without S3: exit %d, stderr:\n%s", code, stderr)
	}
	for _, args := range [][]string{{"-json"}, {"-bogus"}, {"-check-state", "extra"}} {
		if code, stderr := testutil.RunMain(t, nil, args...); code != 2 {
			t.Errorf("%q: exit %d, want 2; stderr:\n%s", args, code, stderr)
		}
	}
}
