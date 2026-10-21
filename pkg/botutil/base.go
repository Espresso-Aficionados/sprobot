package botutil

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

// PrivacyPolicyStatus is the custom status shown by all bots, pointing at the
// privacy policy required for Discord's privileged-intent review.
const PrivacyPolicyStatus = "Privacy: github.com/Espresso-Aficionados/sprobot/blob/main/PRIVACY.md"

// BaseBot holds the fields shared by all three bots.
type BaseBot struct {
	Client               *bot.Client
	S3                   *s3client.Client
	Log                  *slog.Logger
	Ready                atomic.Bool
	TestGuildIDsOverride []snowflake.ID // override for tests without a gateway
	healthcheckEndpoint  string
	httpClient           *http.Client
	gatewayFailed        chan error // receives the error when the gateway closes for good

	ignoredGuildsMu sync.Mutex
	ignoredGuilds   map[snowflake.ID]bool // guilds outside AllowedGuildIDs already warned about
}

// NewBaseBot creates a BaseBot by reading env vars and initializing S3.
// envVarPrefix is the prefix for env vars (e.g. "SPROBOT").
// The healthcheck endpoint is read from {envVarPrefix}_HEALTHCHECK_ENDPOINT.
func NewBaseBot(envVarPrefix string) (*BaseBot, error) {
	s3, err := s3client.New()
	if err != nil {
		return nil, err
	}

	return &BaseBot{
		S3:                  s3,
		Log:                 slog.Default(),
		healthcheckEndpoint: os.Getenv(envVarPrefix + "_HEALTHCHECK_ENDPOINT"),
		httpClient:          &http.Client{Timeout: 10 * time.Second},
		gatewayFailed:       make(chan error, 1),
	}, nil
}

// PingHealthcheck sends a GET to the configured healthcheck endpoint, but only
// while the gateway is connected and ready, so a bot that has lost Discord
// stops reporting healthy. It is a no-op if no endpoint is configured.
func (b *BaseBot) PingHealthcheck() {
	if b.healthcheckEndpoint == "" {
		return
	}
	if status := b.gatewayStatus(); status != gateway.StatusReady {
		b.Log.Warn("Skipping healthcheck ping, gateway not ready", "status", status)
		return
	}
	resp, err := b.httpClient.Get(b.healthcheckEndpoint)
	if err != nil {
		b.Log.Info("Healthcheck ping failed", "error", err)
		return
	}
	resp.Body.Close()
}

func (b *BaseBot) gatewayStatus() gateway.Status {
	if b.Client == nil || b.Client.Gateway == nil {
		return gateway.StatusUnconnected
	}
	return b.Client.Gateway.Status()
}

// OnGatewayClose is the gateway close handler for all bots. disgo calls it
// when it stops reconnecting, e.g. on close code 4004 (bad token) or 4014
// (disallowed intents). That bot would otherwise sit offline, so it shuts
// down: WaitForShutdown returns the error, the bot saves its state, and the
// process exits non-zero for the container to restart it.
func (b *BaseBot) OnGatewayClose(_ gateway.Gateway, err error, reconnect bool) {
	if !gatewayCloseFatal(err, reconnect) {
		b.Log.Warn("Gateway closed", "error", err, "reconnect", reconnect)
		return
	}
	b.Log.Error("Gateway closed and will not reconnect, shutting down", "error", err)
	select {
	case b.gatewayFailed <- err:
	default: // a failure is already pending
	}
}

// gatewayCloseFatal reports whether a gateway close leaves the bot offline
// for good. disgo retries reconnectable closes itself. ErrGatewayAlreadyConnected
// means a concurrent reconnect won, and net.ErrClosed means we closed the
// connection ourselves, as on shutdown.
func gatewayCloseFatal(err error, reconnect bool) bool {
	if reconnect {
		return false
	}
	return !errors.Is(err, discord.ErrGatewayAlreadyConnected) && !errors.Is(err, net.ErrClosed)
}

// GuildIDs returns the IDs of the guilds in the gateway cache that are in
// AllowedGuildIDs: the guilds to register commands and load state for.
// In tests without a gateway, set TestGuildIDsOverride.
func (b *BaseBot) GuildIDs() []snowflake.ID {
	if b.TestGuildIDsOverride != nil {
		return b.TestGuildIDsOverride
	}
	if b.Client == nil {
		return nil
	}
	var ids []snowflake.ID
	for g := range b.Client.Caches.Guilds() {
		if GuildAllowed(g.ID) {
			ids = append(ids, g.ID)
		}
	}
	return ids
}

// WaitForGuilds blocks until all guilds are ready or the timeout expires.
func (b *BaseBot) WaitForGuilds(timeout time.Duration) {
	deadline := time.After(timeout)
	for {
		if len(b.Client.Caches.UnreadyGuildIDs()) == 0 {
			b.Log.Info("All guilds ready", "count", b.Client.Caches.GuildsLen())
			return
		}
		select {
		case <-deadline:
			b.Log.Warn("Timed out waiting for guilds", "unready", len(b.Client.Caches.UnreadyGuildIDs()))
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// OnReady is the shared ready handler for all bots.
func (b *BaseBot) OnReady(e *events.Ready) {
	b.Log.Info("Logged in")
	for _, g := range e.Guilds {
		b.warnIfNotAllowed(g.ID)
	}
	b.Ready.Store(true)
}
