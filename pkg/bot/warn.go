package bot

import (
	"fmt"
	"strconv"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

func (b *Bot) handleWarn(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	targetUser := data.User("user")
	if targetUser.ID == 0 {
		return
	}

	reason := data.String("reason")

	// Post public warning embed in the channel
	e.CreateMessage(discord.MessageCreate{
		Content: userMention(targetUser.ID),
		Embeds: []discord.Embed{{
			Title:       "Warning",
			Color:       colorOrange,
			Description: fmt.Sprintf("You have been warned and this has been saved to your permanent record.\n\n**Reason:** %s", reason),
			Footer: &discord.EmbedFooter{
				Text:    fmt.Sprintf("Issued by @%s", e.User().Username),
				IconURL: e.User().EffectiveAvatarURL(),
			},
		}},
	})

	// Build embed for event log + mod log
	logEmbed := discord.Embed{
		Title: "Member Warned",
		Color: colorOrange,
		Author: &discord.EmbedAuthor{
			Name:    targetUser.Username,
			IconURL: targetUser.EffectiveAvatarURL(),
		},
		Fields: []discord.EmbedField{
			{Name: "User", Value: fmt.Sprintf("%s (`%d`)", userMention(targetUser.ID), targetUser.ID)},
			{Name: "Moderator", Value: userMention(e.User().ID), Inline: boolPtr(true)},
			{Name: "Channel", Value: channelMention(e.Channel().ID()), Inline: boolPtr(true)},
			{Name: "Reason", Value: reason},
		},
	}

	b.postEventLog(*guildID, logEmbed)
	b.crossPostToModLog(*guildID, targetUser, logEmbed)

	b.Log.Info("Warn issued",
		"target_user_id", strconv.FormatInt(int64(targetUser.ID), 10),
		"moderator_user_id", userIDStr(e),
		"guild_id", guildIDStr(e),
	)
}
