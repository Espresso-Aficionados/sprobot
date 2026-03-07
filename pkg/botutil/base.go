package botutil

import (
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

// BaseBot holds the fields shared by all three bots.
type BaseBot struct {
	Client               *bot.Client
	S3                   *s3client.Client
	Log                  *slog.Logger
	Ready                atomic.Bool
	TestGuildIDsOverride []snowflake.ID // override for tests without a gateway
	healthcheckEndpoint  string
	httpClient           *http.Client
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
	}, nil
}

// PingHealthcheck sends a GET to the configured healthcheck endpoint.
// It is a no-op if no endpoint is configured.
func (b *BaseBot) PingHealthcheck() {
	if b.healthcheckEndpoint == "" {
		return
	}
	resp, err := b.httpClient.Get(b.healthcheckEndpoint)
	if err != nil {
		b.Log.Info("Healthcheck ping failed", "error", err)
		return
	}
	resp.Body.Close()
}

// GuildIDs returns the IDs of all guilds in the gateway cache.
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
		ids = append(ids, g.ID)
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
func (b *BaseBot) OnReady(_ *events.Ready) {
	b.Log.Info("Logged in")
	b.Ready.Store(true)
}
