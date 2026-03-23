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
	if err := e.CreateMessage(discord.MessageCreate{
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
	}); err != nil {
		b.Log.Error("Failed to post warning message", "error", err, "target_user_id", targetUser.ID, "guild_id", *guildID)
		return
	}

	// Fetch the sent message to build a link for the mod log
	var warnLink string
	if msg, err := b.Client.Rest.GetInteractionResponse(e.ApplicationID(), e.Token()); err == nil {
		warnLink = messageLink(*guildID, msg.ChannelID, msg.ID)
	}

	// Build embed for event log + mod log
	logFields := []discord.EmbedField{
		{Name: "User", Value: fmt.Sprintf("%s (`%d`)", userMention(targetUser.ID), targetUser.ID)},
		{Name: "Moderator", Value: userMention(e.User().ID), Inline: boolPtr(true)},
		{Name: "Channel", Value: channelMention(e.Channel().ID()), Inline: boolPtr(true)},
		{Name: "Reason", Value: reason},
	}
	if warnLink != "" {
		logFields = append(logFields, discord.EmbedField{Name: "Warning Message", Value: fmt.Sprintf("[Jump to message](%s)", warnLink)})
	}

	logEmbed := discord.Embed{
		Title: "Member Warned",
		Color: colorOrange,
		Author: &discord.EmbedAuthor{
			Name:    targetUser.Username,
			IconURL: targetUser.EffectiveAvatarURL(),
		},
		Fields: logFields,
	}

	b.postEventLog(*guildID, logEmbed)
	b.crossPostToModLog(*guildID, targetUser, logEmbed)

	b.Log.Info("Warn issued",
		"target_user_id", strconv.FormatInt(int64(targetUser.ID), 10),
		"moderator_user_id", userIDStr(e),
		"guild_id", guildIDStr(e),
	)
}
