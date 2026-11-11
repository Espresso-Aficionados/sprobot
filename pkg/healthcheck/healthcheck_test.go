package healthcheck

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok\n")
	}))
	defer ok.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gateway not ready", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	c := &http.Client{Timeout: time.Second}
	for _, tc := range []struct {
		name, url string
		want      int
		stderr    string
	}{
		{"healthy", ok.URL, 0, ""},
		{"unhealthy", down.URL, 1, "503"},
		{"not listening", closedURL, 1, "healthcheck:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr strings.Builder
			if got := run(c, tc.url, &stderr); got != tc.want {
				t.Fatalf("run() = %d, want %d (stderr %q)", got, tc.want, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.stderr) {
				t.Errorf("stderr %q, want it to contain %q", stderr.String(), tc.stderr)
			}
		})
	}
}
