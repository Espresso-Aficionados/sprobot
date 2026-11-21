package envconfig

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Espresso-Aficionados/sprobot/pkg/logsetup"
	"github.com/Espresso-Aficionados/sprobot/pkg/opsecrets"
)

// allVars is every variable any loader reads.
var allVars = []string{
	"OP_SERVICE_ACCOUNT_TOKEN", "SPROBOT_DISCORD_TOKEN", "SPROBOT_HEALTHCHECK_ENDPOINT",
	"S3_KEY", "S3_SECRET", "S3_ENDPOINT", "S3_BUCKET", "WEB_ENDPOINT", "PORT",
}

// setEnv clears every variable the loaders read, then sets env.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range allVars {
		t.Setenv(name, env[name])
	}
}

// prodLike is the environment the production units give the bots today,
// with plain values in place of their op:// references.
func prodLike() map[string]string {
	return map[string]string{
		"SPROBOT_DISCORD_TOKEN":        "MTA.secret-token.value",
		"SPROBOT_HEALTHCHECK_ENDPOINT": "https://hc.example/ping/abc",
		"S3_KEY":                       "KEYVALUE",
		"S3_SECRET":                    "SECRETVALUE",
		"S3_ENDPOINT":                  "https://bucket.region.example.com/",
		"S3_BUCKET":                    "profile-bot",
	}
}

func problems(t *testing.T, err error) []string {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *Error", err)
	}
	return e.Problems
}

func TestLoadBotValid(t *testing.T) {
	env := prodLike()
	setEnv(t, env)
	// A variable nothing reads any more is ignored.
	t.Setenv("SPROBOT_POSTER_ROLE_THRESHOLD", "200")

	cfg, err := LoadBot("SPROBOT")
	if err != nil {
		t.Fatal(err)
	}
	want := Bot{
		Token:               env["SPROBOT_DISCORD_TOKEN"],
		HealthcheckEndpoint: env["SPROBOT_HEALTHCHECK_ENDPOINT"],
		Storage: Storage{
			S3Key: "KEYVALUE", S3Secret: "SECRETVALUE",
			S3Endpoint: env["S3_ENDPOINT"], S3Bucket: "profile-bot",
			WebEndpoint: "https://bot.espressoaf.com/",
		},
	}
	if cfg != want {
		t.Fatalf("LoadBot = %+v, want %+v", cfg, want)
	}
}

func TestLoadBotOptionalUnset(t *testing.T) {
	env := prodLike()
	delete(env, "SPROBOT_HEALTHCHECK_ENDPOINT")
	env["WEB_ENDPOINT"] = "http://localhost:8080/"
	setEnv(t, env)

	cfg, err := LoadBot("SPROBOT")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HealthcheckEndpoint != "" || cfg.WebEndpoint != "http://localhost:8080/" {
		t.Fatalf("LoadBot = %+v", cfg)
	}
}

// Every problem is reported at once, each by name, and no value appears in
// any of them.
func TestLoadBotReportsEveryProblemWithoutValues(t *testing.T) {
	setEnv(t, map[string]string{
		"SPROBOT_DISCORD_TOKEN":        "Bot sekrit-token-1",
		"SPROBOT_HEALTHCHECK_ENDPOINT": "sekrit-hc-2",
		"S3_SECRET":                    "\"sekrit-3\"",
		"S3_ENDPOINT":                  "sekrit-endpoint-4",
		"S3_BUCKET":                    "Sekrit_Bucket_5",
		"WEB_ENDPOINT":                 "https://sekrit-web-6.example",
	})

	_, err := LoadBot("SPROBOT")
	got := problems(t, err)
	for _, name := range []string{"SPROBOT_DISCORD_TOKEN", "SPROBOT_HEALTHCHECK_ENDPOINT", "S3_KEY", "S3_SECRET", "S3_ENDPOINT", "S3_BUCKET", "WEB_ENDPOINT"} {
		if !slices.ContainsFunc(got, func(p string) bool { return strings.HasPrefix(p, name+" ") }) {
			t.Errorf("no problem for %s in %q", name, got)
		}
	}
	if len(got) != 7 {
		t.Errorf("got %d problems, want 7: %q", len(got), got)
	}
	var log bytes.Buffer
	Log(logsetup.New(&log), err)
	if strings.Contains(strings.ToLower(err.Error()+log.String()), "sekrit") {
		t.Fatalf("a value leaked:\n%s\n%s", err, log.String())
	}
	if n := strings.Count(log.String(), "problem="); n != 7 {
		t.Errorf("logged %d problem lines, want 7:\n%s", n, log.String())
	}
}

func TestLoadBotEmpty(t *testing.T) {
	setEnv(t, nil)
	_, err := LoadBot("STICKYBOT")
	got := problems(t, err)
	want := []string{
		"STICKYBOT_DISCORD_TOKEN is not set",
		"S3_KEY is not set",
		"S3_SECRET is not set",
		"S3_ENDPOINT is not set",
		"S3_BUCKET is not set",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("problems = %q, want %q", got, want)
	}
}

func TestLoadWeb(t *testing.T) {
	env := prodLike()
	setEnv(t, env)
	cfg, err := LoadWeb()
	if err != nil || cfg.Port != "8080" {
		t.Fatalf("LoadWeb = %+v, %v; want port 8080", cfg, err)
	}

	t.Setenv("PORT", "9001")
	if cfg, err = LoadWeb(); err != nil || cfg.Port != "9001" {
		t.Fatalf("LoadWeb = %+v, %v; want port 9001", cfg, err)
	}

	for _, bad := range []string{"0", "65536", "http", "-1", "80 "} {
		t.Setenv("PORT", bad)
		if got := problems(t, func() error { _, err := LoadWeb(); return err }()); len(got) != 1 || !strings.HasPrefix(got[0], "PORT ") {
			t.Errorf("PORT=%q: problems %q", bad, got)
		}
	}
}

// op:// references are resolved in one request, and their resolved values
// are checked like plain ones.
func TestLoadResolvesReferencesOnce(t *testing.T) {
	var calls [][]string
	restore := opsecrets.ResolveForTesting(func(refs []string) (map[string]string, error) {
		calls = append(calls, slices.Clone(refs))
		out := map[string]string{}
		for _, ref := range refs {
			out[ref] = "resolved-" + strings.TrimPrefix(ref, "op://v/i/")
		}
		out["op://v/i/badtoken"] = "has space"
		return out, nil
	})
	t.Cleanup(restore)

	env := prodLike()
	env["OP_SERVICE_ACCOUNT_TOKEN"] = "ops_x"
	env["SPROBOT_DISCORD_TOKEN"] = "op://v/i/token"
	env["S3_KEY"] = "op://v/i/key"
	env["S3_SECRET"] = "op://v/i/secret"
	setEnv(t, env)

	cfg, err := LoadBot("SPROBOT")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "resolved-token" || cfg.S3Key != "resolved-key" || cfg.S3Secret != "resolved-secret" {
		t.Fatalf("LoadBot = %+v", cfg)
	}
	if len(calls) != 1 || len(calls[0]) != 3 {
		t.Fatalf("resolve calls = %q, want one with 3 references", calls)
	}

	restore()
	t.Cleanup(opsecrets.ResolveForTesting(func(refs []string) (map[string]string, error) {
		return map[string]string{"op://v/i/token": "has space"}, nil
	}))
	t.Setenv("SPROBOT_DISCORD_TOKEN", "op://v/i/token")
	t.Setenv("S3_KEY", "plain")
	t.Setenv("S3_SECRET", "plain")
	got := problems(t, func() error { _, err := LoadBot("SPROBOT"); return err }())
	if len(got) != 1 || !strings.HasPrefix(got[0], "SPROBOT_DISCORD_TOKEN contains") {
		t.Fatalf("problems = %q", got)
	}
}

// Without the service-account token, references are a problem, not a
// request, and the rest is still checked.
func TestLoadReferencesWithoutToken(t *testing.T) {
	restore := opsecrets.ResolveForTesting(func(refs []string) (map[string]string, error) {
		t.Fatalf("resolve called with %q", refs)
		return nil, nil
	})
	t.Cleanup(restore)

	env := prodLike()
	env["SPROBOT_DISCORD_TOKEN"] = "op://v/i/token"
	env["S3_KEY"] = "op://v/i/key"
	env["S3_BUCKET"] = ""
	setEnv(t, env)

	got := problems(t, func() error { _, err := LoadBot("SPROBOT"); return err }())
	want := []string{
		"OP_SERVICE_ACCOUNT_TOKEN is not set, but resolves the 1Password references (op://) in SPROBOT_DISCORD_TOKEN, S3_KEY",
		"S3_BUCKET is not set",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("problems = %q, want %q", got, want)
	}
}

func TestLoadReferenceFailure(t *testing.T) {
	t.Cleanup(opsecrets.ResolveForTesting(func(refs []string) (map[string]string, error) {
		return nil, errors.New("rate limited")
	}))
	env := prodLike()
	env["OP_SERVICE_ACCOUNT_TOKEN"] = "ops_x"
	env["S3_SECRET"] = "op://v/i/secret"
	setEnv(t, env)

	got := problems(t, func() error { _, err := LoadStorage(); return err }())
	if len(got) < 2 || !strings.Contains(got[0], "S3_SECRET") || !slices.Contains(got, "rate limited") {
		t.Fatalf("problems = %q", got)
	}
}

func TestChecks(t *testing.T) {
	for _, tc := range []struct {
		check func(string) string
		v     string
		ok    bool
	}{
		{httpURL, "https://a.example/", true},
		{httpURL, "http://localhost:8080", true},
		{httpURL, "ftp://a.example/", false},
		{httpURL, "a.example", false},
		{httpURL, "https://", false},
		{webEndpoint, "https://bot.espressoaf.com/", true},
		{webEndpoint, "https://bot.espressoaf.com", false},
		{bucketName, "profile-bot", true},
		{bucketName, "a.b-c1", true},
		{bucketName, "ab", false},
		{bucketName, "Profile", false},
		{bucketName, "-ab", false},
		{noSpaces, "abc.DEF-_123", true},
		{noSpaces, "abc\n", false},
		{noSpaces, "'abc'", false},
		{noSpaces, "a\x00b", false},
	} {
		if got := tc.check(tc.v) == ""; got != tc.ok {
			t.Errorf("check(%q) ok = %v, want %v", tc.v, got, tc.ok)
		}
	}
}
