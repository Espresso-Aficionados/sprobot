package sprobot

import (
	"fmt"
	"net/url"
	"os"
)

const ImageField = "Gear Picture"

// Reserved profile keys, stored alongside template fields in the profile JSON
// but never rendered as fields.
const (
	// DisplayNameKey holds the member's display name as of their last save.
	DisplayNameKey = "_display_name"
	// ImageVersionKey changes on every image upload so the stable image URL
	// changes with it and Discord's media proxy never serves a stale copy.
	ImageVersionKey = "_image_version"
)

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

// ProfileImageWebPath is the stable, non-expiring image route for a profile.
// ext (e.g. ".png") is cosmetic: the route serves the current image regardless.
func ProfileImageWebPath(guildID, templateName, userID, ext string) string {
	return ProfileWebPath(guildID, templateName, userID) + "/image" + ext
}
