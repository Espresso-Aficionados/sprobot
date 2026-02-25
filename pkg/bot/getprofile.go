package bot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
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

	// Defer since GetMember + FetchProfile may exceed the 3s deadline.
	if err := e.DeferCreateMessage(false); err != nil {
		b.Log.Error("Failed to defer getprofile response", "error", err)
		return
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	profile, err := b.S3.FetchProfile(ctx, tmpl, guildStr, targetIDStr)
	if err != nil {
		if errors.Is(err, s3client.ErrNotFound) {
			var msg string
			if isSelf {
				msg = fmt.Sprintf("Whoops! Unable to find a profile for you. To set one up run /edit%s", tmpl.ShortName)
			} else {
				msg = fmt.Sprintf("Whoops! Unable to find a profile for %s.", targetName)
			}
			b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
				Content: msg,
				Flags:   discord.MessageFlagEphemeral,
			})
			return
		}
		b.Log.Error("Failed to fetch profile", "error", err)
		b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
			Content: "Oops! Something went wrong.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	embed := buildProfileEmbed(tmpl, targetName, profile, guildStr, targetIDStr, b.S3.Bucket())
	if _, err := b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	}); err != nil {
		b.Log.Error("Failed to send profile embed", "error", err)
	}
}

func (b *Bot) handleGetMenu(e *events.ApplicationCommandInteractionCreate, tmpl sprobot.Template) {
	b.Log.Info("Processing getprofile menu",
		"user_id", userIDStr(e),
		"template", tmpl.Name,
		"guild_id", guildIDStr(e),
	)

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
		botutil.RespondEphemeral(e, "Oops! Something went wrong.")
		return
	}

	// Defer since GetMember + FetchProfile are two sequential network calls.
	if err := e.DeferCreateMessage(false); err != nil {
		b.Log.Error("Failed to defer getmenu response", "error", err)
		return
	}

	targetIDStr := fmt.Sprintf("%d", targetID)
	targetName := targetUser.Username
	member, err := b.Client.Rest.GetMember(*e.GuildID(), targetID)
	if err == nil {
		targetName = member.EffectiveName()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	profile, err := b.S3.FetchProfile(ctx, tmpl, guildStr, targetIDStr)
	if err != nil {
		if errors.Is(err, s3client.ErrNotFound) {
			var msg string
			if targetID == e.User().ID {
				msg = fmt.Sprintf("Whoops! Unable to find a profile for you. To set one up run /edit%s", tmpl.ShortName)
			} else {
				msg = fmt.Sprintf("Whoops! Unable to find a %s profile for %s.", tmpl.Name, targetName)
			}
			b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
				Content: msg,
				Flags:   discord.MessageFlagEphemeral,
			})
			return
		}
		b.Log.Error("Failed to fetch profile", "error", err)
		b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
			Content: "Oops! Something went wrong.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	embed := buildProfileEmbed(tmpl, targetName, profile, guildStr, targetIDStr, b.S3.Bucket())
	if _, err := b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	}); err != nil {
		b.Log.Error("Failed to send profile embed", "error", err)
	}
}
