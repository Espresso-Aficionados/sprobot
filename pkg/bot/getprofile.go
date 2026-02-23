package bot

import (
	"context"
	"errors"
	"fmt"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

func (b *Bot) handleGet(e *events.ApplicationCommandInteractionCreate, tmpl sprobot.Template) {
	guildStr := guildIDStr(e)
	userStr := userIDStr(e)

	b.Log.Info("Processing getprofile",
		"user_id", userStr,
		"template", tmpl.Name,
		"guild_id", guildStr,
	)

	targetID := e.User().ID
	targetName := getNickOrName(e.Member())
	isSelf := true

	if data, ok := e.Data.(discord.SlashCommandInteractionData); ok {
		if user, ok := data.OptUser("name"); ok {
			targetID = user.ID
			// Try to get member for nick
			member, err := b.Client.Rest.GetMember(*e.GuildID(), targetID)
			if err == nil {
				targetName = member.EffectiveName()
			} else {
				targetName = user.Username
			}
			isSelf = false
		}
	}

	targetIDStr := fmt.Sprintf("%d", targetID)
	profile, err := b.S3.FetchProfile(context.Background(), tmpl, guildStr, targetIDStr)
	if err != nil {
		if errors.Is(err, s3client.ErrNotFound) {
			var msg string
			if isSelf {
				msg = fmt.Sprintf("Whoops! Unable to find a profile for you. To set one up run /edit%s", tmpl.ShortName)
			} else {
				msg = fmt.Sprintf("Whoops! Unable to find a profile for %s.", targetName)
			}
			respondEphemeral(e, msg)
			return
		}
		b.Log.Error("Failed to fetch profile", "error", err)
		respondEphemeral(e, "Oops! Something went wrong.")
		return
	}

	embed := buildProfileEmbed(tmpl, targetName, profile, guildStr, targetIDStr)
	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	})
}

func (b *Bot) handleGetMenu(e *events.ApplicationCommandInteractionCreate, tmpl sprobot.Template) {
	guildStr := guildIDStr(e)
	var targetUser discord.User
	var targetID snowflake.ID

	switch data := e.Data.(type) {
	case discord.UserCommandInteractionData:
		targetUser = data.TargetUser()
		targetID = targetUser.ID
	case discord.MessageCommandInteractionData:
		msg := data.TargetMessage()
		targetUser = msg.Author
		targetID = msg.Author.ID
	default:
		respondEphemeral(e, "Oops! Something went wrong.")
		return
	}

	targetIDStr := fmt.Sprintf("%d", targetID)
	targetName := targetUser.Username
	member, err := b.Client.Rest.GetMember(*e.GuildID(), targetID)
	if err == nil {
		targetName = member.EffectiveName()
	}

	profile, err := b.S3.FetchProfile(context.Background(), tmpl, guildStr, targetIDStr)
	if err != nil {
		if errors.Is(err, s3client.ErrNotFound) {
			if targetID == e.User().ID {
				respondEphemeral(e, fmt.Sprintf("Whoops! Unable to find a profile for you. To set one up run /edit%s", tmpl.ShortName))
			} else {
				respondEphemeral(e, fmt.Sprintf("Whoops! Unable to find a %s profile for %s.", tmpl.Name, targetName))
			}
			return
		}
		respondEphemeral(e, "Oops! Something went wrong.")
		return
	}

	embed := buildProfileEmbed(tmpl, targetName, profile, guildStr, targetIDStr)
	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	})
}
