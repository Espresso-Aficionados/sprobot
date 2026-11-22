package s3client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Espresso-Aficionados/sprobot/pkg/envconfig"
	"github.com/Espresso-Aficionados/sprobot/pkg/telemetrytest"
)

// New's S3 calls are recorded as calls to the "s3" dependency, through the
// SDK's own client (a future SDK whose default isn't an *http.Client would
// silently go uninstrumented).
func TestNewInstrumentsS3(t *testing.T) {
	rec := telemetrytest.Install(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	if _, ok := s3HTTPClient().(*http.Client); !ok {
		t.Fatalf("s3HTTPClient is %T, not an instrumented *http.Client", s3HTTPClient())
	}

	c, err := New(envconfig.Storage{S3Key: "k", S3Secret: "s", S3Endpoint: srv.URL, S3Bucket: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.StatFile(t.Context(), "mod_files/1/x.png"); err == nil {
		t.Fatal("StatFile of a 404 succeeded")
	}

	var peers []string
	for _, p := range rec.Points(t, "http.client.request.duration") {
		peers = append(peers, p.Get("peer.service"))
	}
	if len(peers) == 0 || peers[0] != "s3" {
		t.Errorf("client request peers = %q, want s3", peers)
	}
}

// Fetches of user-supplied URLs are recorded as "fetch", and the
// instrumentation sits outside the dial check, which still refuses
// internal addresses.
func TestFetchClientsInstrumented(t *testing.T) {
	rec := telemetrytest.Install(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)

	resp, err := httpClient.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("fetch of a loopback address wasn't blocked")
	}

	found := false
	for _, p := range rec.Points(t, "http.client.request.duration") {
		if p.Get("peer.service") == "fetch" {
			found = true
		}
	}
	if !found {
		t.Error("no client request recorded for peer fetch")
	}
}
