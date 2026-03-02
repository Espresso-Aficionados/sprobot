package bot

import (
	"context"

	"github.com/disgoorg/disgo/discord"

	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

const footerIconURL = "https://profile-bot.us-southeast-1.linodeobjects.com/76916743.gif"

func buildProfileEmbed(tmpl sprobot.Template, username string, profile map[string]string, guildID, userID string, s3 *s3client.Client) discord.Embed {
	profileURL := buildProfileURL(tmpl, guildID, userID)

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
		img = s3.PresignExisting(context.Background(), img)
		embed.Image = &discord.EmbedResource{
			URL: img,
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

func buildProfileURL(tmpl sprobot.Template, guildID, userID string) string {
	return sprobot.WebEndpoint + sprobot.ProfileWebPath(guildID, tmpl.Name, userID)
}

func rgbToInt(r, g, b int) int {
	return (r << 16) | (g << 8) | b
}
