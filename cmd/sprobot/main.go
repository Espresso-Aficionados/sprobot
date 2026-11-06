package main

import (
	"log"

	"github.com/Espresso-Aficionados/sprobot/pkg/bot"
	"github.com/Espresso-Aficionados/sprobot/pkg/opsecrets"
)

func main() {
	token, err := opsecrets.Getenv("SPROBOT_DISCORD_TOKEN")
	if err != nil {
		log.Fatalf("Reading bot token: %v", err)
	}
	if token == "" {
		log.Fatal("Missing bot token: SPROBOT_DISCORD_TOKEN")
	}

	b, err := bot.New(token)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	if err := b.Run(); err != nil {
		log.Fatalf("Bot error: %v", err)
	}
}
