package main

import (
	"github.com/sadbox/svc/healthcheck"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/envconfig"
	"github.com/Espresso-Aficionados/sprobot/pkg/logsetup"
	"github.com/Espresso-Aficionados/sprobot/pkg/stickybot"
)

func main() {
	healthcheck.Main(botutil.HealthURL)
	logsetup.Init()
	botutil.CheckStateMain(stickybot.CheckState)
	// Before anything connects or writes: a bad environment exits 1 here.
	cfg, err := envconfig.LoadBot("STICKYBOT")
	if err != nil {
		envconfig.Fatal(err)
	}

	b, err := stickybot.New(cfg)
	if err != nil {
		logsetup.Fatal("Failed to create bot", "error", err)
	}

	if err := b.Run(); err != nil {
		logsetup.Fatal("Bot error", "error", err)
	}
}
