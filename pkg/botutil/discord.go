package botutil

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// DeferredWorkBudget bounds slow work (such as saving large files) done after
// deferring an interaction. The interaction token, needed for the follow-up
// message, expires 15 minutes after the interaction was created.
const DeferredWorkBudget = 12 * time.Minute

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

// postRetryDelay is the backoff step between PostWithRetry attempts, a
// variable so tests can shorten it.
var postRetryDelay = 2 * time.Second

// PostWithRetry creates a message, making up to 3 attempts with increasing
// backoff. Every attempt carries the same nonce with enforce_nonce set, so if
// an earlier attempt created the message but its response was lost (a timeout
// or 5xx), Discord returns that message instead of posting a duplicate. 4xx
// responses are not retried: they fail the same way again, and disgo already
// waits out 429s.
func PostWithRetry(restClient rest.Rest, channelID snowflake.ID, msg discord.MessageCreate, log *slog.Logger) (*discord.Message, error) {
	msg = msg.WithNonce(newNonce()).WithEnforceNonce(true)
	var sent *discord.Message
	var err error
	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * postRetryDelay)
		}
		sent, err = restClient.CreateMessage(channelID, msg)
		if err == nil {
			return sent, nil
		}
		log.Warn("Repost attempt failed", "channel_id", channelID, "attempt", attempt+1, "error", err)
		if isClientError(err) {
			break
		}
	}
	return nil, err
}

// newNonce returns a random message nonce. Discord allows up to 25 characters.
func newNonce() string {
	b := make([]byte, 12)
	rand.Read(b) // never fails since Go 1.24
	return hex.EncodeToString(b)
}

// isClientError reports whether err is a 4xx response from Discord.
func isClientError(err error) bool {
	var restErr *rest.Error
	if !errors.As(err, &restErr) || restErr.Response == nil {
		return false
	}
	return restErr.Response.StatusCode >= 400 && restErr.Response.StatusCode < 500
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

// RegisterGuildCommands registers the given commands for each of the provided guilds.
func RegisterGuildCommands(client *bot.Client, guildIDs []snowflake.ID, commands []discord.ApplicationCommandCreate, log *slog.Logger) error {
	for _, guildID := range guildIDs {
		if _, err := client.Rest.SetGuildCommands(client.ApplicationID, guildID, commands); err != nil {
			return fmt.Errorf("registering guild commands for %d: %w", guildID, err)
		}
		log.Info("Registered guild commands", "guild_id", guildID, "count", len(commands))
	}
	return nil
}
