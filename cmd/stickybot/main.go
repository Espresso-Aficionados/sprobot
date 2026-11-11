package main

import (
	"log"
	"os"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/healthcheck"
	"github.com/Espresso-Aficionados/sprobot/pkg/opsecrets"
	"github.com/Espresso-Aficionados/sprobot/pkg/stickybot"
)

func main() {
	if healthcheck.Requested() {
		os.Exit(healthcheck.Run(botutil.HealthURL))
	}
	token, err := opsecrets.Getenv("STICKYBOT_DISCORD_TOKEN")
	if err != nil {
		log.Fatalf("Reading bot token: %v", err)
	}
	if token == "" {
		log.Fatal("Missing bot token: STICKYBOT_DISCORD_TOKEN")
	}

	b, err := stickybot.New(token)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	if err := b.Run(); err != nil {
		log.Fatalf("Bot error: %v", err)
	}
}
