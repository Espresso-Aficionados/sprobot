package main

import (
	"github.com/sadbox/svc/healthcheck"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/logsetup"
	"github.com/Espresso-Aficionados/sprobot/pkg/opsecrets"
	"github.com/Espresso-Aficionados/sprobot/pkg/threadbot"
)

func main() {
	healthcheck.Main(botutil.HealthURL)
	logsetup.Init()
	token, err := opsecrets.Getenv("THREADBOT_DISCORD_TOKEN")
	if err != nil {
		logsetup.Fatal("Reading bot token", "error", err)
	}
	if token == "" {
		logsetup.Fatal("Missing bot token: THREADBOT_DISCORD_TOKEN")
	}

	b, err := threadbot.New(token)
	if err != nil {
		logsetup.Fatal("Failed to create bot", "error", err)
	}

	if err := b.Run(); err != nil {
		logsetup.Fatal("Bot error", "error", err)
	}
}
