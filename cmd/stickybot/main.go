package main

import (
	"os"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/healthcheck"
	"github.com/Espresso-Aficionados/sprobot/pkg/logsetup"
	"github.com/Espresso-Aficionados/sprobot/pkg/opsecrets"
	"github.com/Espresso-Aficionados/sprobot/pkg/stickybot"
)

func main() {
	if healthcheck.Requested() {
		os.Exit(healthcheck.Run(botutil.HealthURL))
	}
	logsetup.Init()
	token, err := opsecrets.Getenv("STICKYBOT_DISCORD_TOKEN")
	if err != nil {
		logsetup.Fatal("Reading bot token", "error", err)
	}
	if token == "" {
		logsetup.Fatal("Missing bot token: STICKYBOT_DISCORD_TOKEN")
	}

	b, err := stickybot.New(token)
	if err != nil {
		logsetup.Fatal("Failed to create bot", "error", err)
	}

	if err := b.Run(); err != nil {
		logsetup.Fatal("Bot error", "error", err)
	}
}
