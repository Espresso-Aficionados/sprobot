package botutil

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/Espresso-Aficionados/sprobot/pkg/envconfig"
	"github.com/Espresso-Aficionados/sprobot/pkg/logsetup"
	"github.com/Espresso-Aficionados/sprobot/pkg/s3client"
	"github.com/Espresso-Aficionados/sprobot/pkg/statever"
)

// CheckGuildState adds to r every allowed guild's {prefix}/{guildID}.json
// for each prefix, read from S3 (GET only) as the bots' loads read them.
func CheckGuildState(ctx context.Context, r *statever.Report, s3 *s3client.Client, prefixes ...string) {
	for _, prefix := range prefixes {
		for _, guildID := range AllowedGuildIDs {
			name := prefix + "/" + guildID.String() + ".json"
			fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			data, err := s3.FetchGuildJSON(fetchCtx, prefix, guildID.String())
			cancel()
			if errors.Is(err, s3client.ErrNotFound) {
				r.Missing(name)
				continue
			}
			r.Add(name, data, err)
		}
	}
}

// CheckStateMain is a bot's -check-state: it returns unless -check-state was
// given. Otherwise it reads the bot's state through check with only the
// storage environment, prints what it found (statever.Report.Print) and
// exits with its code; a bad environment exits 1.
func CheckStateMain(check func(context.Context, *s3client.Client) statever.Report) {
	checkState, asJSON := statever.ParseFlags("this bot's state in S3 (and sprobot's cache dir); needs the S3 environment and network, not the Discord token")
	if !checkState {
		return
	}
	cfg, err := envconfig.LoadStorage()
	if err != nil {
		envconfig.Fatal(err)
	}
	s3, err := s3client.New(cfg)
	if err != nil {
		logsetup.Fatal("Failed to create S3 client", "error", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	code := check(ctx, s3).Print(os.Stdout, os.Stderr, asJSON)
	cancel()
	os.Exit(code)
}
