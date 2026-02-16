package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

func (b *Bot) handleSaveImageCommand(e *events.ApplicationCommandInteractionCreate, tmpl sprobot.Template) {
	guildStr := guildIDStr(e)
	userStr := userIDStr(e)

	b.log.Info("Processing saveimage command",
		"user_id", userStr,
		"template", tmpl.Name,
		"guild_id", guildStr,
	)

	profile := make(map[string]string)
	existing, err := b.s3.FetchProfile(context.Background(), tmpl, guildStr, userStr)
	if err == nil {
		profile = existing
	} else if !errors.Is(err, s3client.ErrNotFound) {
		b.log.Error("Failed to fetch profile", "error", err)
	}

	if data, ok := e.Data.(discord.SlashCommandInteractionData); ok {
		if att, ok := data.OptAttachment("image"); ok {
			profile[tmpl.Image.Name] = att.ProxyURL
		}
	}

	_, userErr, err := b.s3.SaveProfile(context.Background(), tmpl, guildStr, userStr, profile)
	if err != nil {
		b.log.Error("Failed to save profile", "error", err)
		respondEphemeral(e, "Oops! Something went wrong.")
		return
	}
	if userErr != "" {
		respondEphemeral(e, userErr)
		return
	}

	username := getNickOrName(e.Member())
	embed := buildProfileEmbed(tmpl, username, profile, guildStr, userStr)
	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embed},
		Flags:  discord.MessageFlagEphemeral,
	})
}

func (b *Bot) handleSaveImageMenu(e *events.ApplicationCommandInteractionCreate, tmpl sprobot.Template) {
	guildStr := guildIDStr(e)
	userStr := userIDStr(e)

	b.log.Info("Processing saveimage context menu",
		"user_id", userStr,
		"template", tmpl.Name,
		"guild_id", guildStr,
	)

	data, ok := e.Data.(discord.MessageCommandInteractionData)
	if !ok {
		respondEphemeral(e, "Oops! Something went wrong.")
		return
	}
	msg := data.TargetMessage()

	profile := make(map[string]string)
	existing, err := b.s3.FetchProfile(context.Background(), tmpl, guildStr, userStr)
	if err == nil {
		profile = existing
	} else if !errors.Is(err, s3client.ErrNotFound) {
		b.log.Error("Failed to fetch profile", "error", err)
	}

	foundAttachments := 0
	videoError := ""

	for _, att := range msg.Attachments {
		if att.ContentType != nil && strings.HasPrefix(*att.ContentType, "image/") {
			foundAttachments++
			profile[tmpl.Image.Name] = att.ProxyURL
		} else if att.ContentType != nil && strings.HasPrefix(*att.ContentType, "video/") {
			videoError = fmt.Sprintf("It looks like that attachment was a video (%s), we can only use images. Discord often uses mp4s instead of gifs.", *att.ContentType)
		}
	}

	for _, embed := range msg.Embeds {
		if embed.Image != nil && embed.Image.ProxyURL != "" {
			foundAttachments++
			profile[tmpl.Image.Name] = embed.Image.ProxyURL
		} else if embed.Video != nil {
			videoError = "It looks like that attachment was a video, unfortunately we can only use images. Discord will often use a mp4 instead of a gif."
		}
	}

	if videoError != "" && foundAttachments == 0 {
		respondEphemeral(e, videoError)
		return
	}

	if foundAttachments > 1 {
		respondEphemeral(e, fmt.Sprintf("I found %d images in that post, but I'm not sure which one to use! Please make a post with just a single image.", foundAttachments))
		return
	}

	if foundAttachments == 0 {
		respondEphemeral(e, "I didn't find an image to save in that post :(")
		return
	}

	_, userErr, err := b.s3.SaveProfile(context.Background(), tmpl, guildStr, userStr, profile)
	if err != nil {
		b.log.Error("Failed to save profile", "error", err)
		respondEphemeral(e, "Oops! Something went wrong.")
		return
	}
	if userErr != "" {
		respondEphemeral(e, userErr)
		return
	}

	username := getNickOrName(e.Member())
	embed := buildProfileEmbed(tmpl, username, profile, guildStr, userStr)
	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embed},
		Flags:  discord.MessageFlagEphemeral,
	})
}
