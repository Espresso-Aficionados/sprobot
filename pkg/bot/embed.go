package bot

import (
	"math/rand/v2"
	"net/url"

	"github.com/disgoorg/disgo/discord"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

const footerIconURL = "https://profile-bot.us-southeast-1.linodeobjects.com/76916743.gif"

func buildProfileEmbed(tmpl sprobot.Template, username string, profile map[string]string, guildID, userID, bucket string) discord.Embed {
	profileURL := buildProfileURL(tmpl, guildID, userID, bucket)

	embed := discord.Embed{
		Title: tmpl.Name + " for " + username,
		URL:   profileURL,
		Color: rgbToInt(103, 71, 54),
	}

	for _, field := range tmpl.Fields {
		val, ok := profile[field.Name]
		if !ok || val == "" {
			continue
		}
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name:  field.Name,
			Value: val,
		})
	}

	if img, ok := profile[tmpl.Image.Name]; ok && img != "" {
		embed.Image = &discord.EmbedResource{
			URL: img + "?" + randomLetters(10),
		}
	} else {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name:  "Want to add a profile image?",
			Value: "Check out the guide at https://espressoaf.com/guides/sprobot.html#saving-a-profile-image-via-right-click",
		})
	}

	embed.Footer = &discord.EmbedFooter{
		Text:    "sprobot",
		IconURL: footerIconURL,
	}

	return embed
}

func buildProfileURL(tmpl sprobot.Template, guildID, userID, bucket string) string {
	s3Path := "profiles/" + guildID + "/" + tmpl.Name + "/" + userID + ".json"
	return sprobot.WebEndpoint + url.PathEscape(bucket+"/"+s3Path)
}

func rgbToInt(r, g, b int) int {
	return (r << 16) | (g << 8) | b
}

func randomLetters(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = letters[rand.IntN(len(letters))]
	}
	return string(buf)
}
