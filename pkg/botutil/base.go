package botutil

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"

	"github.com/sadbox/sprobot/pkg/s3client"
)

// BaseBot holds the fields shared by all three bots.
type BaseBot struct {
	Client              *bot.Client
	S3                  *s3client.Client
	Env                 string
	Log                 *slog.Logger
	Ready               atomic.Bool
	healthcheckEndpoint string
}

// NewBaseBot creates a BaseBot by reading the given env var and initializing S3.
// envVar is the environment variable for the bot's env (e.g. "SPROBOT_ENV").
// The healthcheck endpoint is read from {PREFIX}_HEALTHCHECK_ENDPOINT, where
// PREFIX is envVar with the "_ENV" suffix removed.
func NewBaseBot(envVar string) (*BaseBot, error) {
	s3, err := s3client.New()
	if err != nil {
		return nil, err
	}

	prefix := strings.TrimSuffix(envVar, "_ENV")
	healthcheckVar := prefix + "_HEALTHCHECK_ENDPOINT"

	return &BaseBot{
		S3:                  s3,
		Env:                 os.Getenv(envVar),
		Log:                 slog.Default(),
		healthcheckEndpoint: os.Getenv(healthcheckVar),
	}, nil
}

// PingHealthcheck sends a GET to the configured healthcheck endpoint.
// It is a no-op in dev or if no endpoint is configured.
func (b *BaseBot) PingHealthcheck() {
	if b.Env != "prod" || b.healthcheckEndpoint == "" {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(b.healthcheckEndpoint)
	if err != nil {
		b.Log.Info("Healthcheck ping failed", "error", err)
		return
	}
	resp.Body.Close()
}

// OnReady is the shared ready handler for all bots.
func (b *BaseBot) OnReady(_ *events.Ready) {
	b.Log.Info("Logged in")
	b.Ready.Store(true)
}
