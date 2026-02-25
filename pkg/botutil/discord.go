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

// OnlyBotsAfter fetches the most recent messages in a channel and checks
// whether the bot's last message is among them and all messages after it were
// sent by bots. This prevents repost loops when multiple bots are active in
// the same channel. Returns true if lastMsgID is found in the recent history
// and only bot messages follow it (or nothing follows it). Returns false if
// any human message was posted after lastMsgID, if lastMsgID is not found in
// the recent history, or on error.
func OnlyBotsAfter(restClient rest.Rest, channelID, lastMsgID snowflake.ID, log *slog.Logger) bool {
	// Fetch the 25 most recent messages (no after/before filter).
	msgs, err := restClient.GetMessages(channelID, 0, 0, 0, 25)
	if err != nil {
		log.Error("Failed to fetch messages for bot-loop check", "channel_id", channelID, "error", err)
		return false
	}
	// msgs are returned newest-first. Walk from newest until we find our
	// message; every message before it (i.e. newer) must be from a bot.
	for _, m := range msgs {
		if m.ID == lastMsgID {
			return true // Found our message; everything newer was a bot.
		}
		if !m.Author.Bot && !m.Author.System {
			return false // A human posted after us.
		}
	}
	// Our message wasn't found in the recent history — too many messages
	// have been posted since. Allow the repost.
	return false
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
