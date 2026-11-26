package botutil

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// The gateway's websocket reaches Discord through egress, an HTTP CONNECT
// proxy, the way disgo dials it: gorilla's DefaultDialer, whose Proxy is
// http.ProxyFromEnvironment (HTTPS_PROXY). gorilla/websocket is pinned to an
// untagged commit (GO-2026-6278) that rewrote that dial and proxy path; this
// is what it must keep doing.
func TestGatewayWebsocketThroughConnectProxy(t *testing.T) {
	if reflect.ValueOf(websocket.DefaultDialer.Proxy).Pointer() != reflect.ValueOf(http.ProxyFromEnvironment).Pointer() {
		t.Fatal("websocket.DefaultDialer no longer takes its proxy from the environment: the gateway would skip egress")
	}

	upgrader := websocket.Upgrader{}
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			kind, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(kind, msg); err != nil {
				return
			}
		}
	}))
	defer api.Close()

	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		connects.Add(1)
		up, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		down, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			_ = up.Close()
			return
		}
		go func() { _, _ = io.Copy(up, down); _ = up.Close() }()
		_, _ = io.Copy(down, up)
		_ = down.Close()
	}))
	defer proxy.Close()

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	d := *websocket.DefaultDialer
	d.Proxy = http.ProxyURL(proxyURL)
	d.TLSClientConfig = &tls.Config{RootCAs: api.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	d.HandshakeTimeout = 5 * time.Second

	c, resp, err := d.DialContext(t.Context(), "wss"+api.URL[len("https"):]+"/?v=10&encoding=json", nil)
	if err != nil {
		t.Fatalf("dial through the proxy: %v", err)
	}
	defer c.Close()
	_ = resp.Body.Close() // the handshake's: the connection is c's
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status %d", resp.StatusCode)
	}
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"op":1,"d":null}`)); err != nil {
		t.Fatal(err)
	}
	_, got, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"op":1,"d":null}` {
		t.Errorf("echo %q", got)
	}
	if n := connects.Load(); n != 1 {
		t.Errorf("%d CONNECTs through the proxy, want 1", n)
	}
}
