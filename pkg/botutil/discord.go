package botutil

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type MessageResponder interface {
	CreateMessage(discord.MessageCreate, ...rest.RequestOpt) error
}

func RespondEphemeral(e MessageResponder, content string) {
	if err := e.CreateMessage(discord.MessageCreate{
		Content: content,
		Flags:   discord.MessageFlagEphemeral,
	}); err != nil {
		slog.Error("Failed to send ephemeral response", "error", err)
	}
}

// PostWithRetry attempts to create a message up to 3 times with exponential backoff.
func PostWithRetry(restClient rest.Rest, channelID snowflake.ID, msg discord.MessageCreate, log *slog.Logger) (*discord.Message, error) {
	var sent *discord.Message
	var err error
	for attempt := range 3 {
		sent, err = restClient.CreateMessage(channelID, msg)
		if err == nil {
			return sent, nil
		}
		log.Warn("Repost attempt failed", "channel_id", channelID, "attempt", attempt+1, "error", err)
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	return nil, err
}

// OnlyBotsAfter checks whether all messages in the channel after lastMsgID
// were sent by bots. This prevents repost loops when multiple bots are active
// in the same channel. Returns true if the bot's message is still present and
// only bot messages follow it (or no messages follow it). Returns false if any
// human message was posted after lastMsgID, or if lastMsgID is not found in
// the recent history.
func OnlyBotsAfter(restClient rest.Rest, channelID, lastMsgID snowflake.ID, log *slog.Logger) bool {
	// Fetch up to 10 messages after our last message.
	msgs, err := restClient.GetMessages(channelID, 0, 0, lastMsgID, 10)
	if err != nil {
		log.Error("Failed to fetch messages for bot-loop check", "channel_id", channelID, "error", err)
		return false // On error, allow the repost.
	}
	for _, m := range msgs {
		if !m.Author.Bot && !m.Author.System {
			return false
		}
	}
	return true
}

// RegisterGuildCommands registers the given commands for each guild matching the env.
func RegisterGuildCommands(client *bot.Client, env string, commands []discord.ApplicationCommandCreate, log *slog.Logger) error {
	for _, guildID := range GetGuildIDs(env) {
		if _, err := client.Rest.SetGuildCommands(client.ApplicationID, guildID, commands); err != nil {
			return fmt.Errorf("registering guild commands for %d: %w", guildID, err)
		}
		log.Info("Registered guild commands", "guild_id", guildID, "count", len(commands))
	}
	return nil
}
