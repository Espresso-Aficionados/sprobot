package botutil

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"

	"github.com/sadbox/sprobot/pkg/testutil"
)

// fakeGateway reports a fixed status; nothing else on it is called.
type fakeGateway struct {
	gateway.Gateway
	status gateway.Status
}

func (g fakeGateway) Status() gateway.Status { return g.status }

func TestPingHealthcheckOnlyWhenGatewayReady(t *testing.T) {
	var pings atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { pings.Add(1) }))
	defer srv.Close()

	tests := []struct {
		name   string
		client *bot.Client
		want   int32
	}{
		{"ready", &bot.Client{Gateway: fakeGateway{status: gateway.StatusReady}}, 1},
		{"disconnected", &bot.Client{Gateway: fakeGateway{status: gateway.StatusDisconnected}}, 0},
		{"resuming", &bot.Client{Gateway: fakeGateway{status: gateway.StatusResuming}}, 0},
		{"no gateway", &bot.Client{}, 0},
		{"no client", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pings.Store(0)
			b := &BaseBot{
				Client:              tt.client,
				Log:                 testutil.DiscardLogger(),
				healthcheckEndpoint: srv.URL,
				httpClient:          srv.Client(),
			}
			b.PingHealthcheck()
			if got := pings.Load(); got != tt.want {
				t.Errorf("pings = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGatewayCloseFatal(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		reconnect bool
		want      bool
	}{
		{"disallowed intents", errors.New("websocket: close 4014: Disallowed intent(s)"), false, true},
		{"authentication failed", errors.New("websocket: close 4004: Authentication failed"), false, true},
		{"reconnectable close", errors.New("websocket: close 4000: Unknown error"), true, false},
		{"concurrent reconnect won", discord.ErrGatewayAlreadyConnected, false, false},
		{"closed by us", fmt.Errorf("read: %w", net.ErrClosed), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gatewayCloseFatal(tt.err, tt.reconnect); got != tt.want {
				t.Errorf("gatewayCloseFatal(%v, %v) = %v, want %v", tt.err, tt.reconnect, got, tt.want)
			}
		})
	}
}

// A fatal close ends WaitForShutdown with the gateway error, so Run saves
// state and returns it; a reconnectable one leaves the bot running.
func TestOnGatewayCloseEndsWaitForShutdown(t *testing.T) {
	b := &BaseBot{Log: testutil.DiscardLogger(), gatewayFailed: make(chan error, 1)}

	b.OnGatewayClose(nil, errors.New("websocket: close 4000: Unknown error"), true)
	if len(b.gatewayFailed) != 0 {
		t.Fatal("reconnectable close signalled a shutdown")
	}

	closeErr := errors.New("websocket: close 4014: Disallowed intent(s)")
	b.OnGatewayClose(nil, closeErr, false)
	b.OnGatewayClose(nil, closeErr, false) // a second failure must not block disgo

	done := make(chan error, 1)
	go func() { done <- b.WaitForShutdown("Test") }()
	select {
	case err := <-done:
		if !errors.Is(err, closeErr) {
			t.Errorf("WaitForShutdown = %v, want the gateway error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForShutdown did not return after a fatal gateway close")
	}
}
