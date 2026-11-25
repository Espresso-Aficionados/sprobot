package s3client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/Espresso-Aficionados/sprobot/pkg/telemetrytest"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// fakeGateway stands in for the egress proxy's fetch gateway
// (homelab-configs egress/gateway.go): it records what it was asked and
// answers by URL.
type fakeGateway struct {
	*httptest.Server
	answer func(target string, w http.ResponseWriter)

	mu   sync.Mutex
	asks []*http.Request
}

// upstream marks a response as relayed from the URL's host.
func upstream(w http.ResponseWriter) { w.Header().Set(fetchResultHeader, "upstream") }

// refuse answers as the gateway does when it refuses.
func refuse(w http.ResponseWriter, status int, code string) {
	w.Header().Set(fetchErrorHeader, code)
	http.Error(w, code, status)
}

func newFakeGateway(t *testing.T, answer func(target string, w http.ResponseWriter)) *fakeGateway {
	t.Helper()
	g := &fakeGateway{answer: answer}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.asks = append(g.asks, r.Clone(context.Background()))
		g.mu.Unlock()
		g.answer(r.Header.Get(fetchURLHeader), w)
	}))
	t.Cleanup(g.Close)
	u, err := url.Parse(g.URL)
	if err != nil {
		t.Fatal(err)
	}
	orig := fetchGateway
	fetchGateway = func() (*url.URL, error) { return u, nil }
	t.Cleanup(func() { fetchGateway = orig })
	return g
}

func (g *fakeGateway) asked() []*http.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*http.Request(nil), g.asks...)
}

func (g *fakeGateway) urls() []string {
	var out []string
	for _, r := range g.asked() {
		out = append(out, r.Header.Get(fetchURLHeader))
	}
	return out
}

// proxyEverything makes the environment's proxy apply to every URL, and
// returns it: with a gateway, a user's URL must still never reach it.
func proxyEverything(t *testing.T) *recordingProxy {
	t.Helper()
	p := newRecordingProxy(t, "from the proxy", "")
	orig := fetchProxy
	fetchProxy = func(*http.Request) (*url.URL, error) { return p.proxyURL(t), nil }
	t.Cleanup(func() { fetchProxy = orig })
	return p
}

// With a gateway, a user's URL is asked of it: one plain GET naming the
// URL, never the environment's proxy, never the URL's host directly (it
// doesn't resolve). The gateway gets the client span's traceparent.
func TestFetchGoesToTheGateway(t *testing.T) {
	rec := telemetrytest.Install(t)
	g := newFakeGateway(t, func(_ string, w http.ResponseWriter) {
		upstream(w)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	p := proxyEverything(t)

	ctx, span := otel.Tracer("test").Start(context.Background(), "save")
	defer span.End()
	const target = "https://images.invalid/a/cat.png?sig=abc"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	req.Header.Set("Cookie", "never=sent")
	resp, err := newFetchClient(0, 0).Do(req)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != string(pngBytes) || resp.StatusCode != http.StatusOK {
		t.Errorf("got %d %q", resp.StatusCode, body)
	}
	if resp.Request.URL.String() != target {
		t.Errorf("the response's request is %s, want the caller's", resp.Request.URL)
	}

	asks := g.asked()
	if len(asks) != 1 {
		t.Fatalf("the gateway was asked %d times, want 1", len(asks))
	}
	a := asks[0]
	if a.Method != http.MethodGet || a.URL.Path != "/fetch" || a.URL.RawQuery != "" || a.Header.Get(fetchURLHeader) != target {
		t.Errorf("asked %s %s with %s: %q", a.Method, a.URL, fetchURLHeader, a.Header.Get(fetchURLHeader))
	}
	if a.Header.Get("Cookie") != "" {
		t.Error("the caller's headers went to the gateway")
	}
	// The traceparent is the fetch client span's (the save span's child).
	tp := a.Header.Get("Traceparent")
	var fetchSpan string
	for _, s := range rec.Spans() {
		if s.Name == "fetch GET" && s.Parent.SpanID() == span.SpanContext().SpanID() {
			fetchSpan = s.SpanContext.SpanID().String()
		}
	}
	if fetchSpan == "" || !strings.Contains(tp, span.SpanContext().TraceID().String()+"-"+fetchSpan) {
		t.Errorf("Traceparent %q, want the fetch span %s of trace %s", tp, fetchSpan, span.SpanContext().TraceID())
	}
	if got := p.seen(); len(got) != 0 {
		t.Errorf("the proxy saw %q, want nothing", got)
	}
}

// The gateway doesn't follow redirects: each hop comes back and is asked
// of it again, resolved against the URL that redirected.
func TestFetchGatewayRedirects(t *testing.T) {
	g := newFakeGateway(t, func(target string, w http.ResponseWriter) {
		upstream(w)
		switch target {
		case "https://short.invalid/x":
			w.Header().Set("Location", "https://cdn.invalid/dir/")
			w.WriteHeader(http.StatusFound)
		case "https://cdn.invalid/dir/":
			w.Header().Set("Location", "cat.png") // relative
			w.WriteHeader(http.StatusMovedPermanently)
		default:
			_, _ = w.Write(pngBytes)
		}
	})
	resp, err := newFetchClient(0, 0).Get("https://short.invalid/x")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	resp.Body.Close()
	want := []string{"https://short.invalid/x", "https://cdn.invalid/dir/", "https://cdn.invalid/dir/cat.png"}
	if got := g.urls(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("the gateway was asked for %q, want %q", got, want)
	}
}

// The gateway's refusals are errors, the two callers act on by name; the
// host's own statuses are responses.
func TestFetchGatewayRefusals(t *testing.T) {
	g := newFakeGateway(t, func(target string, w http.ResponseWriter) {
		switch {
		case strings.HasSuffix(target, "/page"):
			refuse(w, http.StatusUnsupportedMediaType, "not-image")
		case strings.HasSuffix(target, "/huge"):
			refuse(w, http.StatusRequestEntityTooLarge, "too-large")
		case strings.HasSuffix(target, "/internal"):
			refuse(w, http.StatusForbidden, "denied")
		case strings.HasSuffix(target, "/unmarked"):
			_, _ = w.Write(pngBytes) // something else on that port
		case strings.HasSuffix(target, "/gone"):
			upstream(w)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	client := newFetchClient(0, 0)
	for _, tc := range []struct {
		path     string
		is       error // errors.Is
		code     string
		notImage bool
	}{
		{path: "/page", is: ErrNotImage, code: "not-image"},
		{path: "/huge", is: ErrFileTooLarge, code: "too-large"},
		{path: "/internal", code: "denied"},
		{path: "/unmarked"},
	} {
		resp, err := client.Get("https://images.invalid" + tc.path)
		if err == nil {
			resp.Body.Close()
			t.Errorf("%s: no error", tc.path)
			continue
		}
		var ge *gatewayError
		if !errors.As(err, &ge) || ge.code != tc.code {
			t.Errorf("%s: err = %v, want a gateway error %q", tc.path, err, tc.code)
		}
		if tc.is != nil && !errors.Is(err, tc.is) {
			t.Errorf("%s: err = %v, want %v", tc.path, err, tc.is)
		}
		if tc.is == nil && (errors.Is(err, ErrNotImage) || errors.Is(err, ErrFileTooLarge)) {
			t.Errorf("%s: err = %v reads as not-an-image or too-large", tc.path, err)
		}
	}
	resp, err := client.Get("https://images.invalid/gone")
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the host's 404: got %v, %v", resp, err)
	}
	resp.Body.Close()

	// Only GETs: anything else fails here, and the gateway never hears it.
	before := len(g.asked())
	if resp, err := client.Post("https://images.invalid/upload", "text/plain", strings.NewReader("data")); err == nil {
		resp.Body.Close()
		t.Error("a POST went through")
	}
	if len(g.asked()) != before {
		t.Error("the gateway was asked for a POST")
	}
}

// A shortcut's image is a user's URL: through the gateway, and a refusal
// there keeps the shortcut a link, as a page fetched directly does. An
// attachment is Discord's URL: the bot's own traffic, never the gateway.
func TestSaveRemoteFileRoutes(t *testing.T) {
	disableURLValidation(t)
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	c := newTestClient(t, server)
	attachment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("any file at all"))
	}))
	t.Cleanup(attachment.Close)
	g := newFakeGateway(t, func(target string, w http.ResponseWriter) {
		if strings.HasSuffix(target, "/page") {
			refuse(w, http.StatusUnsupportedMediaType, "not-image")
			return
		}
		upstream(w)
		_, _ = w.Write(pngBytes)
	})

	if _, err := c.SaveShortcutImage(context.Background(), "123", "https://images.invalid/cat.png"); err != nil {
		t.Fatalf("shortcut image: %v", err)
	}
	if _, err := c.SaveShortcutImage(context.Background(), "123", "https://images.invalid/page"); !errors.Is(err, ErrNotImage) {
		t.Fatalf("shortcut page: err = %v, want ErrNotImage", err)
	}
	if got := g.urls(); len(got) != 2 {
		t.Fatalf("the gateway was asked for %q, want the two shortcut URLs", got)
	}
	if _, err := c.SaveModFile(context.Background(), "123", attachment.URL+"/log.txt"); err != nil {
		t.Fatalf("mod file: %v", err)
	}
	if _, err := c.SaveStickyFile(context.Background(), "123", attachment.URL+"/sticky.bin"); err != nil {
		t.Fatalf("sticky file: %v", err)
	}
	if got := g.urls(); len(got) != 2 {
		t.Errorf("the gateway was asked for %q: attachments went to it", got)
	}
}

// With a gateway the URL isn't resolved here (the bot's network has no
// DNS for it); the scheme is still checked.
func TestValidateImageURLLeavesHostsToTheGateway(t *testing.T) {
	newFakeGateway(t, func(string, http.ResponseWriter) {})
	if err := validateImageURL("https://images.invalid/cat.png"); err != nil {
		t.Errorf("with a gateway: %v, want no lookup and no error", err)
	}
	if err := validateImageURL("ftp://images.invalid/cat.png"); err == nil {
		t.Error("an ftp URL passed, want the scheme check")
	}
}

func TestParseFetchGateway(t *testing.T) {
	if u, err := parseFetchGateway(""); u != nil || err != nil {
		t.Errorf("unset: got %v, %v", u, err)
	}
	if u, err := parseFetchGateway("http://10.89.3.3:4751"); err != nil || u.Host != "10.89.3.3:4751" {
		t.Errorf("got %v, %v", u, err)
	}
	for _, bad := range []string{"10.89.3.3:4751", "https://10.89.3.3:4751", "http://", "::"} {
		if u, err := parseFetchGateway(bad); err == nil {
			t.Errorf("%q: accepted as %v", bad, u)
		}
	}
	// A value that can't be used fails the fetch: never a direct one.
	orig := fetchGateway
	fetchGateway = func() (*url.URL, error) { return parseFetchGateway("10.89.3.3:4751") }
	t.Cleanup(func() { fetchGateway = orig })
	if resp, err := newFetchClient(0, 0).Get("https://images.invalid/cat.png"); err == nil {
		resp.Body.Close()
		t.Error("a fetch went through with an unusable gateway")
	}
}
