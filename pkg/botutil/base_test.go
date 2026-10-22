package botutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"

	"github.com/sadbox/sprobot/pkg/testutil"
)

// fakeGateway reports a fixed status and fails Open with openErr; nothing
// else on it is called.
type fakeGateway struct {
	gateway.Gateway
	status  gateway.Status
	openErr error
}

func (g fakeGateway) Status() gateway.Status { return g.status }

func (g fakeGateway) Open(context.Context) error { return g.openErr }

// setFatalCloseDelay shortens the hold after a fatal close for one test, and
// afterwards restores the default handling of the signals the hold catches.
func setFatalCloseDelay(t *testing.T, d time.Duration) {
	orig := fatalCloseDelay
	fatalCloseDelay = d
	t.Cleanup(func() {
		fatalCloseDelay = orig
		signal.Reset(syscall.SIGINT, syscall.SIGTERM)
	})
}

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

// A fatal close ends WaitForShutdown with the gateway error after
// fatalCloseDelay, so Run saves state and returns it; a reconnectable one
// leaves the bot running.
func TestOnGatewayCloseEndsWaitForShutdown(t *testing.T) {
	const delay = 100 * time.Millisecond
	setFatalCloseDelay(t, delay)
	b := &BaseBot{Log: testutil.DiscardLogger(), gatewayFailed: make(chan error, 1)}

	b.OnGatewayClose(nil, errors.New("websocket: close 4000: Unknown error"), true)
	if len(b.gatewayFailed) != 0 {
		t.Fatal("reconnectable close signalled a shutdown")
	}

	closeErr := errors.New("websocket: close 4014: Disallowed intent(s)")
	b.OnGatewayClose(nil, closeErr, false)
	b.OnGatewayClose(nil, closeErr, false) // a second failure must not block disgo

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- b.WaitForShutdown("Test") }()
	select {
	case err := <-done:
		if !errors.Is(err, closeErr) {
			t.Errorf("WaitForShutdown = %v, want the gateway error", err)
		}
		if waited := time.Since(start); waited < delay {
			t.Errorf("WaitForShutdown returned after %v, want at least fatalCloseDelay (%v)", waited, delay)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForShutdown did not return after a fatal gateway close")
	}
}

// Discord resets the token of a bot that identifies 1000 times a day, so a
// connection refused for good must not turn into a fast restart loop.
func TestOpenGatewayHoldsAfterFatalClose(t *testing.T) {
	const delay = 100 * time.Millisecond
	setFatalCloseDelay(t, delay)

	tests := []struct {
		name string
		err  error
		hold bool
	}{
		{"disallowed intents", errors.New("websocket: close 4014: Disallowed intent(s)"), true},
		{"concurrent reconnect won", discord.ErrGatewayAlreadyConnected, false},
		{"connected", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &BaseBot{Log: testutil.DiscardLogger(), Client: &bot.Client{Gateway: fakeGateway{openErr: tt.err}}}
			start := time.Now()
			if err := b.OpenGateway(context.Background()); !errors.Is(err, tt.err) {
				t.Errorf("OpenGateway = %v, want %v", err, tt.err)
			}
			if held := time.Since(start) >= delay; held != tt.hold {
				t.Errorf("held before returning = %v, want %v", held, tt.hold)
			}
		})
	}
}
