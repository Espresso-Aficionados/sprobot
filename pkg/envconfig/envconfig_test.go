package envconfig

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Espresso-Aficionados/sprobot/pkg/logsetup"
)

// allVars is every variable any loader reads, and the retired
// SPROBOT_HEALTHCHECK_ENDPOINT, which production still sets.
var allVars = []string{
	"SPROBOT_DISCORD_TOKEN", "SPROBOT_HEALTHCHECK_ENDPOINT",
	"S3_KEY", "S3_SECRET", "S3_ENDPOINT", "S3_BUCKET", "WEB_ENDPOINT", "PORT",
	"EGRESS_FETCH_GATEWAY",
}

// setEnv clears every variable the loaders read, then sets env.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, name := range allVars {
		t.Setenv(name, env[name])
	}
}

// prodLike is the environment the production units give the bots: plain
// values, rendered on the host. It still includes the retired heartbeat URL.
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
	// A variable nothing reads any more is ignored: SPROBOT_HEALTHCHECK_ENDPOINT
	// (set, by prodLike) too.
	t.Setenv("SPROBOT_POSTER_ROLE_THRESHOLD", "200")

	cfg, err := LoadBot("SPROBOT")
	if err != nil {
		t.Fatal(err)
	}
	want := Bot{
		Token: env["SPROBOT_DISCORD_TOKEN"],
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
	if cfg.WebEndpoint != "http://localhost:8080/" {
		t.Fatalf("LoadBot = %+v", cfg)
	}
}

// Production names the egress fetch gateway; a value that isn't a plain
// HTTP URL stops the bot starting, so a typo never means fetching direct.
func TestLoadBotFetchGateway(t *testing.T) {
	env := prodLike()
	env["EGRESS_FETCH_GATEWAY"] = "http://10.89.3.3:4751"
	setEnv(t, env)
	cfg, err := LoadBot("SPROBOT")
	if err != nil || cfg.FetchGateway != "http://10.89.3.3:4751" {
		t.Fatalf("LoadBot = %+v, %v", cfg, err)
	}

	env["EGRESS_FETCH_GATEWAY"] = "10.89.3.3:4751"
	setEnv(t, env)
	_, err = LoadBot("SPROBOT")
	if got := problems(t, err); len(got) != 1 || !strings.HasPrefix(got[0], "EGRESS_FETCH_GATEWAY ") {
		t.Fatalf("problems = %q, want one for EGRESS_FETCH_GATEWAY", got)
	}
}

// The retired SPROBOT_HEALTHCHECK_ENDPOINT never stops a bot starting,
// whatever the deploy still sets it to.
func TestLoadBotIgnoresHealthcheckEndpoint(t *testing.T) {
	for _, v := range []string{"https://hc.example/ping/abc", "not a url", "op://vault/hc/url"} {
		env := prodLike()
		env["SPROBOT_HEALTHCHECK_ENDPOINT"] = v
		setEnv(t, env)
		if _, err := LoadBot("SPROBOT"); err != nil {
			t.Errorf("SPROBOT_HEALTHCHECK_ENDPOINT=%q: %v", v, err)
		}
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
	for _, name := range []string{"SPROBOT_DISCORD_TOKEN", "S3_KEY", "S3_SECRET", "S3_ENDPOINT", "S3_BUCKET", "WEB_ENDPOINT"} {
		if !slices.ContainsFunc(got, func(p string) bool { return strings.HasPrefix(p, name+" ") }) {
			t.Errorf("no problem for %s in %q", name, got)
		}
	}
	if len(got) != 6 {
		t.Errorf("got %d problems, want 6 (none for the retired heartbeat URL): %q", len(got), got)
	}
	var log bytes.Buffer
	Log(logsetup.New(&log), err)
	if strings.Contains(strings.ToLower(err.Error()+log.String()), "sekrit") {
		t.Fatalf("a value leaked:\n%s\n%s", err, log.String())
	}
	if n := strings.Count(log.String(), "problem="); n != 6 {
		t.Errorf("logged %d problem lines, want 6:\n%s", n, log.String())
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

// A 1Password reference that reaches a program is refused, whichever variable
// holds it (required, optional or not a secret) and whichever loader reads it:
// one problem per variable, nothing past "op://" quoted, other problems still
// reported. A leftover OP_SERVICE_ACCOUNT_TOKEN, or a reference in the
// retired SPROBOT_HEALTHCHECK_ENDPOINT, is ignored like any unused variable.
func TestLoadRefusesOPReferences(t *testing.T) {
	refusal := func(name string) string {
		return name + " is a 1Password reference (op://…); render it on the host (quadlet/render-env.sh)"
	}
	env := prodLike()
	env["SPROBOT_DISCORD_TOKEN"] = "op://sekrit vault/discord-sprobot/token"
	env["SPROBOT_HEALTHCHECK_ENDPOINT"] = "op://sekrit vault/hc/url"
	env["S3_KEY"] = "op://sekrit vault/linode-s3/access_key"
	env["S3_ENDPOINT"] = "op://sekrit vault/linode-s3/endpoint"
	env["S3_BUCKET"] = ""
	setEnv(t, env)
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "ops_sekrit")

	storage := []string{refusal("S3_KEY"), refusal("S3_ENDPOINT"), "S3_BUCKET is not set"}
	for _, tc := range []struct {
		name string
		load func() error
		want []string
	}{
		{"LoadBot", func() error { _, err := LoadBot("SPROBOT"); return err },
			append([]string{refusal("SPROBOT_DISCORD_TOKEN")}, storage...)},
		{"LoadWeb", func() error { _, err := LoadWeb(); return err }, storage},
		{"LoadStorage", func() error { _, err := LoadStorage(); return err }, storage},
	} {
		err := tc.load()
		if got := problems(t, err); !slices.Equal(got, tc.want) {
			t.Errorf("%s problems = %q, want %q", tc.name, got, tc.want)
		}
		var log bytes.Buffer
		Log(logsetup.New(&log), err)
		if strings.Contains(strings.ToLower(err.Error()+log.String()), "sekrit") {
			t.Errorf("%s: a value leaked:\n%s\n%s", tc.name, err, log.String())
		}
	}

	// An optional variable holding a reference is refused, not defaulted.
	t.Setenv("S3_KEY", "KEYVALUE")
	t.Setenv("S3_ENDPOINT", "https://bucket.region.example.com/")
	t.Setenv("S3_BUCKET", "profile-bot")
	t.Setenv("WEB_ENDPOINT", "op://v/i/web")
	t.Setenv("PORT", "op://v/i/port")
	got := problems(t, func() error { _, err := LoadWeb(); return err }())
	if want := []string{refusal("PORT"), refusal("WEB_ENDPOINT")}; !slices.Equal(got, want) {
		t.Fatalf("problems = %q, want %q", got, want)
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
		{plainHTTPURL, "http://10.89.3.3:4751", true},
		{plainHTTPURL, "https://10.89.3.3:4751", false},
		{plainHTTPURL, "10.89.3.3:4751", false},
		{plainHTTPURL, "http://", false},
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
