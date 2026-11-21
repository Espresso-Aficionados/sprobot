package main

import (
	"github.com/sadbox/svc/healthcheck"

	"github.com/Espresso-Aficionados/sprobot/pkg/bot"
	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/envconfig"
	"github.com/Espresso-Aficionados/sprobot/pkg/logsetup"
)

func main() {
	healthcheck.Main(botutil.HealthURL)
	logsetup.Init()
	botutil.CheckStateMain(bot.CheckState)
	// Before anything connects or writes: a bad environment exits 1 here.
	cfg, err := envconfig.LoadBot("SPROBOT")
	if err != nil {
		envconfig.Fatal(err)
	}

	b, err := bot.New(cfg)
	if err != nil {
		logsetup.Fatal("Failed to create bot", "error", err)
	}

	if err := b.Run(); err != nil {
		logsetup.Fatal("Bot error", "error", err)
	}
}
