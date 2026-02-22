package main

import (
	"log"
	"os"

	"github.com/sadbox/sprobot/pkg/threadbot"
)

func main() {
	token := os.Getenv("THREADBOT_DISCORD_TOKEN")
	if token == "" {
		log.Fatal("Missing bot token: THREADBOT_DISCORD_TOKEN")
	}

	b, err := threadbot.New(token)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	if err := b.Run(); err != nil {
		log.Fatalf("Bot error: %v", err)
	}
}
