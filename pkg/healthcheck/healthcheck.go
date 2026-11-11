// Package healthcheck is the client side of the container healthchecks: every
// binary takes a lone -healthcheck flag, which the container runtime runs
// inside the container (Podman HealthCmd) to ask the running process's
// /healthz.
package healthcheck

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Requested reports whether the process was started as a healthcheck:
// `<binary> -healthcheck`. Mains check it first, before reading any secret.
func Requested() bool {
	return len(os.Args) == 2 && os.Args[1] == "-healthcheck"
}

// Run GETs url and returns the process exit code: 0 for a 200, 1 otherwise.
func Run(url string) int {
	return run(&http.Client{Timeout: 5 * time.Second}, url, os.Stderr)
}

func run(c *http.Client, url string, stderr io.Writer) int {
	resp, err := c.Get(url)
	if err != nil {
		fmt.Fprintln(stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		fmt.Fprintf(stderr, "healthcheck: %s: %s\n", resp.Status, body)
		return 1
	}
	return 0
}
