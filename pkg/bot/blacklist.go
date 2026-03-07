package bot

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

// containsAny returns true if any of ids appears in list.
func containsAny(list []snowflake.ID, ids ...snowflake.ID) bool {
	for _, id := range ids {
		for _, v := range list {
			if id == v {
				return true
			}
		}
	}
	return false
}

// blacklistable abstracts access to a mutex-protected []snowflake.ID blacklist.
type blacklistable interface {
	lock()
	unlock()
	getBlacklist() []snowflake.ID
	setBlacklist([]snowflake.ID)
}

func (b *Bot) handleBlacklistAdd(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, bl blacklistable, persist func() error, prefix string) {
	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a channel.")
		return
	}

	b.Log.Info(prefix+" blacklist add", "user_id", e.User().ID, "guild_id", guildID, "channel_id", ch.ID)

	bl.lock()
	if containsAny(bl.getBlacklist(), ch.ID) {
		bl.unlock()
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> is already blacklisted.", ch.ID))
		return
	}
	bl.setBlacklist(append(bl.getBlacklist(), ch.ID))
	bl.unlock()

	if err := persist(); err != nil {
		b.Log.Error("Failed to persist "+prefix+" blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> added to blacklist.", ch.ID))
}

func (b *Bot) handleBlacklistRemove(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, bl blacklistable, persist func() error, prefix string) {
	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a channel.")
		return
	}

	b.Log.Info(prefix+" blacklist remove", "user_id", e.User().ID, "guild_id", guildID, "channel_id", ch.ID)

	bl.lock()
	found := false
	list := bl.getBlacklist()
	for i, id := range list {
		if id == ch.ID {
			bl.setBlacklist(append(list[:i], list[i+1:]...))
			found = true
			break
		}
	}
	bl.unlock()

	if !found {
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> is not blacklisted.", ch.ID))
		return
	}

	if err := persist(); err != nil {
		b.Log.Error("Failed to persist "+prefix+" blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> removed from blacklist.", ch.ID))
}

func (b *Bot) handleBlacklistList(e *events.ApplicationCommandInteractionCreate, bl blacklistable, prefix string) {
	b.Log.Info(prefix+" blacklist list", "user_id", e.User().ID, "guild_id", *e.GuildID())

	bl.lock()
	list := make([]snowflake.ID, len(bl.getBlacklist()))
	copy(list, bl.getBlacklist())
	bl.unlock()

	if len(list) == 0 {
		botutil.RespondEphemeral(e, "No channels are blacklisted.")
		return
	}

	var lines []string
	for _, id := range list {
		lines = append(lines, fmt.Sprintf("<#%d>", id))
	}
	botutil.RespondEphemeral(e, "**Blacklisted channels:**\n"+strings.Join(lines, "\n"))
}

func (b *Bot) handleBlacklistClear(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, bl blacklistable, persist func() error, prefix string) {
	b.Log.Info(prefix+" blacklist clear", "user_id", e.User().ID, "guild_id", guildID)

	bl.lock()
	count := len(bl.getBlacklist())
	bl.setBlacklist(nil)
	bl.unlock()

	if count == 0 {
		botutil.RespondEphemeral(e, "Blacklist is already empty.")
		return
	}

	if err := persist(); err != nil {
		b.Log.Error("Failed to persist "+prefix+" blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Cleared %d entries from the blacklist.", count))
}
