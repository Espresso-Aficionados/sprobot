package s3client

import (
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingProxy is a forward proxy on loopback: plain-http requests in
// absolute form (HTTP_PROXY) answered with body, and CONNECT tunnels
// (HTTPS_PROXY) to tunnelTo whatever host the client names, as Smokescreen
// would after resolving it. With redirect set, plain-http requests are
// redirected there instead. It records the targets it was asked for.
type recordingProxy struct {
	*httptest.Server
	body     string
	tunnelTo string
	redirect string

	mu      sync.Mutex
	targets []string
}

func newRecordingProxy(t *testing.T, body, tunnelTo string) *recordingProxy {
	t.Helper()
	return (&recordingProxy{body: body, tunnelTo: tunnelTo}).start(t)
}

func (p *recordingProxy) start(t *testing.T) *recordingProxy {
	t.Helper()
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

func (p *recordingProxy) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	if r.Method == http.MethodConnect {
		p.targets = append(p.targets, "CONNECT "+r.Host)
	} else {
		p.targets = append(p.targets, r.Method+" "+r.URL.String())
	}
	p.mu.Unlock()

	if r.Method != http.MethodConnect {
		if p.redirect != "" {
			http.Redirect(w, r, p.redirect, http.StatusFound)
			return
		}
		w.Write([]byte(p.body))
		return
	}
	upstream, err := net.Dial("tcp", p.tunnelTo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	w.WriteHeader(http.StatusOK)
	conn, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	go func() { io.Copy(upstream, buf); upstream.(*net.TCPConn).CloseWrite() }()
	io.Copy(conn, upstream)
}

func (p *recordingProxy) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

func (p *recordingProxy) proxyURL(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// useProxyFor makes fetches to host (any port) use proxy; every other
// request goes direct, as NO_PROXY or a loopback URL would.
func useProxyFor(t *testing.T, host string, proxy *url.URL) {
	t.Helper()
	orig := fetchProxy
	fetchProxy = func(r *http.Request) (*url.URL, error) {
		if r.URL.Hostname() == host {
			return proxy, nil
		}
		return nil, nil
	}
	t.Cleanup(func() { fetchProxy = orig })
}

// A plain-http fetch goes to the proxy, on loopback, which the dial check
// would refuse for any other address. The URL's host is never resolved or
// dialed here (images.example doesn't resolve).
func TestFetchThroughHTTPProxy(t *testing.T) {
	p := newRecordingProxy(t, "image bytes", "")
	useProxyFor(t, "images.example", p.proxyURL(t))

	client := &http.Client{Transport: newFetchTransport(0)}
	resp, err := client.Get("http://images.example/cat.png")
	if err != nil {
		t.Fatalf("fetch through the proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "image bytes" {
		t.Errorf("body = %q", body)
	}
	if got := p.seen(); len(got) != 1 || got[0] != "GET http://images.example/cat.png" {
		t.Errorf("proxy saw %q", got)
	}
}

// An https fetch, as on bonnet (HTTPS_PROXY): a CONNECT to the proxy for the
// URL's host and port, then TLS with the URL's host, end to end through the
// tunnel.
func TestFetchThroughConnectProxy(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tls image " + r.Host))
	}))
	t.Cleanup(target.Close)
	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())
	p := newRecordingProxy(t, "", target.Listener.Addr().String())
	// httptest's certificate is valid for example.com.
	useProxyFor(t, "example.com", p.proxyURL(t))

	tr := newFetchTransport(0)
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	tr.TLSClientConfig.RootCAs = roots
	resp, err := (&http.Client{Transport: tr}).Get("https://example.com:" + port + "/cat.png")
	if err != nil {
		t.Fatalf("fetch through the CONNECT proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if want := "tls image example.com:" + port; string(body) != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	if got := p.seen(); len(got) != 1 || got[0] != "CONNECT example.com:"+port {
		t.Errorf("proxy saw %q", got)
	}
}

// Only the proxy is exempt from the dial check: a request the proxy doesn't
// take (here, a redirect from the proxied response to loopback, which
// net/http never proxies) is dialed directly and refused.
func TestFetchProxyExemptsOnlyTheProxy(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("internal"))
	}))
	t.Cleanup(internal.Close)
	p := (&recordingProxy{redirect: internal.URL + "/secret.png"}).start(t)
	useProxyFor(t, "images.example", p.proxyURL(t))

	client := &http.Client{Transport: newFetchTransport(0)}
	resp, err := client.Get("http://images.example/cat.png")
	if err == nil {
		resp.Body.Close()
		t.Fatal("redirect to a loopback address wasn't blocked")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error = %v, want the dial check's", err)
	}
	if got := p.seen(); len(got) != 1 {
		t.Errorf("proxy saw %q, want only the first request", got)
	}
}

// With no proxy (local development), fetches dial directly, checked.
func TestFetchWithoutProxyIsChecked(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(internal.Close)
	orig := fetchProxy
	fetchProxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	t.Cleanup(func() { fetchProxy = orig })

	resp, err := (&http.Client{Transport: newFetchTransport(0)}).Get(internal.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("direct fetch of a loopback address wasn't blocked")
	}
}

// The package's own fetch clients take the proxy from the environment, as
// net/http.ProxyFromEnvironment reads it (once per process, so this runs in a
// child process with HTTP_PROXY set).
func TestFetchUsesEnvironmentProxy(t *testing.T) {
	if os.Getenv("SPROBOT_FETCH_PROXY_CHILD") == "1" {
		resp, err := httpClient.Get("http://images.example/cat.png")
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "from env proxy" {
			t.Fatalf("body = %q", body)
		}
		return
	}

	p := newRecordingProxy(t, "from env proxy", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFetchUsesEnvironmentProxy$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"SPROBOT_FETCH_PROXY_CHILD=1",
		"HTTP_PROXY="+p.URL,
		"HTTPS_PROXY=",
		"NO_PROXY=",
		"http_proxy=", "https_proxy=", "no_proxy=", "REQUEST_METHOD=",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if got := p.seen(); len(got) != 1 || got[0] != "GET http://images.example/cat.png" {
		t.Errorf("proxy saw %q", got)
	}
}

func TestProxyAddr(t *testing.T) {
	for raw, want := range map[string]string{
		"http://10.89.3.3:4750":    "10.89.3.3:4750",
		"http://proxy.internal":    "proxy.internal:80",
		"https://proxy.internal":   "proxy.internal:443",
		"socks5://proxy.internal":  "proxy.internal:1080",
		"socks5h://proxy.internal": "proxy.internal:1080",
		"http://[fd00::3]:4750":    "[fd00::3]:4750",
		"http://user:pw@10.0.0.1":  "10.0.0.1:80",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := proxyAddr(u); got != want {
			t.Errorf("proxyAddr(%s) = %q, want %q", raw, got, want)
		}
	}
}
