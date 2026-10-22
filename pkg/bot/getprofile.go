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
	b.Log.Info("Processing getprofile",
		"user_id", userIDStr(e),
		"template", tmpl.Name,
		"guild_id", guildIDStr(e),
	)

	// Defer since FetchProfile may exceed the 3s deadline.
	if err := e.DeferCreateMessage(false); err != nil {
		b.Log.Error("Failed to defer getprofile response", "error", err)
		return
	}

	targetID := e.User().ID
	targetName := getNickOrName(e.Member())
	if data, ok := e.Data.(discord.SlashCommandInteractionData); ok {
		if user, ok := data.OptUser("name"); ok {
			targetID = user.ID
			targetName = user.EffectiveName()
			// The interaction resolves the chosen member, nickname included.
			if member, ok := data.OptMember("name"); ok {
				targetName = member.EffectiveName()
			}
		}
	}

	b.sendProfile(e, tmpl, targetID, targetName)
}

func (b *Bot) handleGetMenu(e *events.ApplicationCommandInteractionCreate, tmpl sprobot.Template) {
	b.Log.Info("Processing getprofile menu",
		"user_id", userIDStr(e),
		"template", tmpl.Name,
		"guild_id", guildIDStr(e),
	)

	var targetID snowflake.ID
	var targetName string
	fetchMember := false
	switch data := e.Data.(type) {
	case discord.UserCommandInteractionData:
		user := data.TargetUser()
		targetID = user.ID
		targetName = user.EffectiveName()
		if member, ok := data.Resolved.Members[targetID]; ok {
			targetName = member.EffectiveName()
		}
	case discord.MessageCommandInteractionData:
		// A message target carries its author but not their membership.
		author := data.TargetMessage().Author
		targetID = author.ID
		targetName = author.EffectiveName()
		fetchMember = true
	default:
		botutil.RespondEphemeral(e, "Oops! Something went wrong.")
		return
	}

	// Defer since GetMember + FetchProfile may exceed the 3s deadline.
	if err := e.DeferCreateMessage(false); err != nil {
		b.Log.Error("Failed to defer getmenu response", "error", err)
		return
	}

	if fetchMember {
		if member, err := b.Client.Rest.GetMember(*e.GuildID(), targetID); err == nil {
			targetName = member.EffectiveName()
		}
	}

	b.sendProfile(e, tmpl, targetID, targetName)
}

// sendProfile answers a deferred profile lookup with targetID's profile, or,
// privately, with why there isn't one to show.
func (b *Bot) sendProfile(e *events.ApplicationCommandInteractionCreate, tmpl sprobot.Template, targetID snowflake.ID, targetName string) {
	guildStr := guildIDStr(e)
	targetIDStr := targetID.String()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	profile, err := b.S3.FetchProfile(ctx, tmpl, guildStr, targetIDStr)
	if err != nil {
		var msg string
		switch {
		case !errors.Is(err, s3client.ErrNotFound):
			b.Log.Error("Failed to fetch profile", "error", err)
			msg = "Oops! Something went wrong."
		case targetID == e.User().ID:
			msg = fmt.Sprintf("Whoops! Unable to find a profile for you. To set one up run /edit%s", tmpl.ShortName)
		default:
			msg = fmt.Sprintf("Whoops! Unable to find a %s profile for %s.", tmpl.Name, targetName)
		}
		b.followupPrivately(e.Token(), msg)
		return
	}

	embed := buildProfileEmbed(tmpl, targetName, profile, guildStr, targetIDStr, b.S3)
	if _, err := b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	}); err != nil {
		b.Log.Error("Failed to send profile embed", "error", err)
	}
}

// followupPrivately answers a publicly deferred interaction with an
// ephemeral message. The first follow-up replaces the deferred response and
// keeps its visibility whatever its own flags say, so the public placeholder
// is deleted first.
func (b *Bot) followupPrivately(token, content string) {
	if err := b.Client.Rest.DeleteInteractionResponse(b.Client.ApplicationID, token); err != nil {
		b.Log.Error("Failed to delete deferred response", "error", err)
	}
	botutil.FollowupEphemeral(b.Client, token, content)
}
