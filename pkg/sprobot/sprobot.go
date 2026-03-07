package sprobot

import (
	"fmt"
	"net/url"
	"os"
)

const ImageField = "Gear Picture"
const WebEndpoint = "https://bot.espressoaf.com/"

// WebEndpointFromEnv returns the web endpoint URL.
// It reads the WEB_ENDPOINT env var, defaulting to the production URL.
func WebEndpointFromEnv() string {
	if v := os.Getenv("WEB_ENDPOINT"); v != "" {
		return v
	}
	return WebEndpoint
}

func ProfileS3Path(guildID, templateName, userID string) string {
	return fmt.Sprintf("profiles/%s/%s/%s.json", guildID, templateName, userID)
}

func ProfileWebPath(guildID, templateName, userID string) string {
	return fmt.Sprintf("profiles/%s/%s/%s", guildID, url.PathEscape(templateName), userID)
}
