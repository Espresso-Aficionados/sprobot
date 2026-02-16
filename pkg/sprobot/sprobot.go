package sprobot

import "fmt"

const ImageField = "Gear Picture"
const WebEndpoint = "http://bot.espressoaf.com/"

func ProfileS3Path(guildID, templateName, userID string) string {
	return fmt.Sprintf("profiles/%s/%s/%s.json", guildID, templateName, userID)
}
