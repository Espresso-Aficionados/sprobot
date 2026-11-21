package testutil

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"
)

const runMainEnv = "SPROBOT_TEST_RUN_MAIN"

// RunMainIfAsked runs main in place of the tests when RunMain started this
// test binary. Call it first in TestMain.
func RunMainIfAsked(main func()) {
	if os.Getenv(runMainEnv) != "1" {
		return
	}
	main()
	os.Exit(0)
}

// RunMain runs the package's main (see RunMainIfAsked) in a new process with
// only env as its environment and args after the test flags, and returns its
// exit code and stderr.
func RunMain(t *testing.T, env []string, args ...string) (code int, stderr string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^$"}, args...)...)
	cmd.Env = append([]string{runMainEnv + "=1"}, env...)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, errBuf.String()
	case errors.As(err, &exit):
		return exit.ExitCode(), errBuf.String()
	default:
		t.Fatalf("running main: %v", err)
		return 0, ""
	}
}
