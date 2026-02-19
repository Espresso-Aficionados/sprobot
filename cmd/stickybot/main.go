package main

import (
	"log"
	"os"

	"github.com/sadbox/sprobot/pkg/stickybot"
)

func main() {
	token := os.Getenv("STICKYBOT_DISCORD_TOKEN")
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
