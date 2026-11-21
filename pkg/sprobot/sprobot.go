package sprobot

import (
	"fmt"
	"net/url"
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

// WebEndpoint is the production web server, WEB_ENDPOINT's default.
const WebEndpoint = "https://bot.espressoaf.com/"

// AvatarWebPath is the bot's avatar, served by the web server.
const AvatarWebPath = "static/sprobot.png"

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
