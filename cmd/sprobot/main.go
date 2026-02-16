package main

import (
	"log"
	"os"

	"github.com/sadbox/sprobot/pkg/bot"
)

func main() {
	token := os.Getenv("SPROBOT_DISCORD_TOKEN")
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
