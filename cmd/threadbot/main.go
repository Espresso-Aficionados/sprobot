package main

import (
	"github.com/sadbox/svc/healthcheck"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/envconfig"
	"github.com/Espresso-Aficionados/sprobot/pkg/logsetup"
	"github.com/Espresso-Aficionados/sprobot/pkg/threadbot"
)

func main() {
	healthcheck.Main(botutil.HealthURL)
	logsetup.Init()
	botutil.CheckStateMain(threadbot.CheckState)
	// Before anything connects or writes: a bad environment exits 1 here.
	cfg, err := envconfig.LoadBot("THREADBOT")
	if err != nil {
		envconfig.Fatal(err)
	}

	shutdownTelemetry := botutil.StartTelemetry("threadbot")

	b, err := threadbot.New(cfg)
	if err != nil {
		shutdownTelemetry()
		logsetup.Fatal("Failed to create bot", "error", err)
	}

	err = b.Run()
	shutdownTelemetry()
	if err != nil {
		logsetup.Fatal("Bot error", "error", err)
	}
}
