// Package envconfig reads and checks a program's whole environment before
// the program does anything else: no Discord connection, no S3 request, no
// file written. Every variable is checked, so a bad deploy fails at once
// with every problem listed, and the container's start fails instead of a
// bot running half-configured. Problems name variables, never their values:
// most of them are secrets.
//
// Every value is used as it is. Secrets are rendered into the environment
// before the program starts (on bonnet, by quadlet/render-env.sh; in dev, by
// `op run` in the run scripts), so a 1Password reference (op://...) that
// reaches a program is a deploy mistake and is refused, not resolved.
package envconfig

import (
	"errors"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/Espresso-Aficionados/sprobot/pkg/sprobot"
)

// opRefPrefix starts a 1Password secret reference.
const opRefPrefix = "op://"

// Storage is the S3 bucket every program uses, and the web server's public
// URL, which links to profiles and stored files are built on.
type Storage struct {
	S3Key, S3Secret, S3Endpoint, S3Bucket string
	// WebEndpoint ends with "/": paths are appended to it.
	WebEndpoint string
}

// Bot is a Discord bot's environment.
type Bot struct {
	Token string
	// HealthcheckEndpoint is pinged while the gateway is ready; "" for none.
	HealthcheckEndpoint string
	Storage
}

// Web is sprobot-web's environment.
type Web struct {
	Port string
	Storage
}

// Error lists every problem found, each naming a variable.
type Error struct {
	Problems []string
}

func (e *Error) Error() string {
	return "invalid environment: " + strings.Join(e.Problems, "; ")
}

// LoadBot reads a bot's environment: {prefix}_DISCORD_TOKEN, the optional
// {prefix}_HEALTHCHECK_ENDPOINT, and the storage variables.
func LoadBot(prefix string) (Bot, error) {
	tokenVar, healthVar := prefix+"_DISCORD_TOKEN", prefix+"_HEALTHCHECK_ENDPOINT"
	r := &reader{}
	cfg := Bot{
		Token:               r.required(tokenVar, noSpaces),
		HealthcheckEndpoint: r.optional(healthVar, "", httpURL),
		Storage:             r.storage(),
	}
	return cfg, r.err()
}

// LoadWeb reads sprobot-web's environment: the optional PORT (default 8080)
// and the storage variables.
func LoadWeb() (Web, error) {
	r := &reader{}
	cfg := Web{
		Port:    r.optional("PORT", "8080", port),
		Storage: r.storage(),
	}
	return cfg, r.err()
}

// LoadStorage reads only the storage variables, for tools.
func LoadStorage() (Storage, error) {
	r := &reader{}
	cfg := r.storage()
	return cfg, r.err()
}

// Fatal logs each of err's problems on its own line and exits 1.
func Fatal(err error) {
	Log(slog.Default(), err)
	os.Exit(1)
}

// Log logs each of err's problems on its own line, or err itself if it
// isn't an *Error.
func Log(log *slog.Logger, err error) {
	var e *Error
	if !errors.As(err, &e) {
		log.Error("Invalid environment", "error", err)
		return
	}
	for _, p := range e.Problems {
		log.Error("Invalid environment", "problem", p)
	}
	log.Error("Not starting: fix the environment", "problems", len(e.Problems))
}

func (r *reader) storage() Storage {
	return Storage{
		S3Key:       r.required("S3_KEY", noSpaces),
		S3Secret:    r.required("S3_SECRET", noSpaces),
		S3Endpoint:  r.required("S3_ENDPOINT", httpURL),
		S3Bucket:    r.required("S3_BUCKET", bucketName),
		WebEndpoint: r.optional("WEB_ENDPOINT", sprobot.WebEndpoint, webEndpoint),
	}
}

// reader holds the problems found so far.
type reader struct {
	problems []string
}

// required returns the variable's value, recording a problem if it's unset
// or fails check.
func (r *reader) required(name string, check func(string) string) string {
	v := os.Getenv(name)
	if v == "" {
		r.problems = append(r.problems, name+" is not set")
		return ""
	}
	return r.checked(name, v, check)
}

// optional is required, but an unset variable gives def.
func (r *reader) optional(name, def string, check func(string) string) string {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	return r.checked(name, v, check)
}

// checked refuses an op:// reference, whatever the variable, before check
// runs: one problem per variable, quoting nothing past the prefix.
func (r *reader) checked(name, v string, check func(string) string) string {
	if strings.HasPrefix(v, opRefPrefix) {
		r.problems = append(r.problems, name+" is a 1Password reference ("+opRefPrefix+"…); render it on the host (quadlet/render-env.sh)")
		return ""
	}
	if why := check(v); why != "" {
		r.problems = append(r.problems, name+" "+why)
		return ""
	}
	return v
}

func (r *reader) err() error {
	if len(r.problems) == 0 {
		return nil
	}
	return &Error{Problems: r.problems}
}

// The checks return why a value is wrong, without quoting it, or "".

// noSpaces is for tokens and keys: a copied value with a trailing newline,
// surrounding quotes or a "Bot " prefix still left on is the usual mistake.
func noSpaces(v string) string {
	if strings.ContainsFunc(v, func(c rune) bool {
		return c <= ' ' || c == 0x7f || c == '"' || c == '\''
	}) {
		return "contains whitespace, quotes or control characters"
	}
	return ""
}

func httpURL(v string) string {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "is not an absolute http(s) URL"
	}
	return ""
}

func webEndpoint(v string) string {
	if why := httpURL(v); why != "" {
		return why
	}
	if !strings.HasSuffix(v, "/") {
		return "must end with /"
	}
	return ""
}

func port(v string) string {
	if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
		return "is not a port number (1-65535)"
	}
	return ""
}

// bucketRE is S3's bucket naming rule, less its rarer exceptions.
var bucketRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func bucketName(v string) string {
	if !bucketRE.MatchString(v) {
		return "is not a bucket name (3-63 lowercase letters, digits, dots and hyphens)"
	}
	return ""
}
