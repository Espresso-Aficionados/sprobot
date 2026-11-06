package main

import (
	"log"

	"github.com/Espresso-Aficionados/sprobot/pkg/opsecrets"
	"github.com/Espresso-Aficionados/sprobot/pkg/stickybot"
)

func main() {
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
