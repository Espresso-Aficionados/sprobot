package botutil

import (
	"testing"

	"github.com/Espresso-Aficionados/sprobot/pkg/envconfig"
	"github.com/Espresso-Aficionados/sprobot/pkg/s3client"
)

// NewBaseBot hands the environment's fetch gateway to s3client: one it
// can't use fails the bot's start, never a bot that fetches directly.
func TestNewBaseBotSetsTheFetchGateway(t *testing.T) {
	t.Cleanup(func() { _ = s3client.SetFetchGateway("") })
	cfg := envconfig.Bot{
		FetchGateway: "http://10.89.3.3:4751",
		Storage:      envconfig.Storage{S3Key: "k", S3Secret: "s", S3Endpoint: "https://s3.invalid/", S3Bucket: "bucket"},
	}
	if _, err := NewBaseBot(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.FetchGateway = "10.89.3.3:4751"
	if _, err := NewBaseBot(cfg); err == nil {
		t.Error("a bot started with a fetch gateway that isn't an http URL")
	}
}
