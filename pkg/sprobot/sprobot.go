package sprobot

import (
	"fmt"
	"net/url"
)

const ImageField = "Gear Picture"
const WebEndpoint = "https://bot.espressoaf.com/"

func ProfileS3Path(guildID, templateName, userID string) string {
	return fmt.Sprintf("profiles/%s/%s/%s.json", guildID, templateName, userID)
}

func ProfileWebPath(guildID, templateName, userID string) string {
	return fmt.Sprintf("profiles/%s/%s/%s", guildID, url.PathEscape(templateName), userID)
}
