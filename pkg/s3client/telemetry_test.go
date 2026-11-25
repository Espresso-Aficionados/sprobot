package s3client

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

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
	// The recorder is the test binary's: other tests' fetches are in it too.
	if !slices.Contains(peers, "s3") {
		t.Errorf("client request peers = %q, want s3 among them", peers)
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

// S3 calls through a proxy (egress) carry the client span's trace on the
// CONNECT, though the SDK wraps the transport telemetry.Client sees.
func TestS3ConnectCarriesTrace(t *testing.T) {
	telemetrytest.Install(t)
	api := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(api.Close)
	got := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		got <- r.Header.Get("Traceparent")
		up, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		down, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			up.Close()
			return
		}
		go func() { _, _ = io.Copy(up, down); up.Close() }()
		_, _ = io.Copy(down, up)
		down.Close()
	}))
	t.Cleanup(proxy.Close)
	proxyURL, _ := url.Parse(proxy.URL)
	roots := api.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	c := s3HTTPClientWith(func(tr *http.Transport) {
		tr.Proxy = http.ProxyURL(proxyURL)
		tr.TLSClientConfig = &tls.Config{RootCAs: roots}
	})
	ctx, parent := otel.Tracer("test").Start(t.Context(), "parent")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/b/k", nil)
	resp, err := c.Do(req)
	parent.End()
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	select {
	case tp := <-got:
		sc := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(), propagation.HeaderCarrier{"Traceparent": {tp}}))
		if sc.TraceID() != parent.SpanContext().TraceID() {
			t.Errorf("CONNECT traceparent %q, want trace %s", tp, parent.SpanContext().TraceID())
		}
	default:
		t.Fatal("the request never went through the proxy")
	}
}
