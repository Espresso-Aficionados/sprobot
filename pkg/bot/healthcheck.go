package bot

import (
	"net/http"
	"os"
	"time"
)

func (b *Bot) healthcheckLoop() {
	// Wait for the bot to be ready
	for !b.ready.Load() {
		time.Sleep(1 * time.Second)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		b.pingHealthcheck()
	}
}

func (b *Bot) pingHealthcheck() {
	endpoint := os.Getenv("SPROBOT_HEALTHCHECK_ENDPOINT")
	if endpoint == "" {
		b.log.Info("Please set SPROBOT_HEALTHCHECK_ENDPOINT to enable healthcheck reporting")
		return
	}
	b.log.Info("Pinging healthcheck endpoint")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		b.log.Info("Ping failed", "error", err)
		return
	}
	resp.Body.Close()
}
