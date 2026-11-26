package bot

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
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

// withoutID returns list without id, and whether id was in it. It builds a new
// slice rather than shifting list in place: other goroutines may be reading a
// copy of list taken under the lock.
func withoutID(list []snowflake.ID, id snowflake.ID) ([]snowflake.ID, bool) {
	i := slices.Index(list, id)
	if i < 0 {
		return list, false
	}
	return slices.Delete(slices.Clone(list), i, i+1), true
}

// blacklistable abstracts access to a mutex-protected []snowflake.ID blacklist.
type blacklistable interface {
	lock()
	unlock()
	getBlacklist() []snowflake.ID
	setBlacklist([]snowflake.ID)
}

func (b *Bot) handleBlacklistAdd(ctx context.Context, e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, bl blacklistable, persist func() error, prefix string) {
	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(ctx, e, "Please provide a channel.")
		return
	}

	b.Log.InfoContext(ctx, prefix+" blacklist add", "user_id", e.User().ID, "guild_id", guildID, "channel_id", ch.ID)

	bl.lock()
	if containsAny(bl.getBlacklist(), ch.ID) {
		bl.unlock()
		botutil.RespondEphemeral(ctx, e, fmt.Sprintf("<#%d> is already blacklisted.", ch.ID))
		return
	}
	bl.setBlacklist(append(bl.getBlacklist(), ch.ID))
	bl.unlock()

	if err := persist(); err != nil {
		b.Log.ErrorContext(ctx, "Failed to persist "+prefix+" blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(ctx, e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(ctx, e, fmt.Sprintf("<#%d> added to blacklist.", ch.ID))
}

func (b *Bot) handleBlacklistRemove(ctx context.Context, e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, bl blacklistable, persist func() error, prefix string) {
	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(ctx, e, "Please provide a channel.")
		return
	}

	b.Log.InfoContext(ctx, prefix+" blacklist remove", "user_id", e.User().ID, "guild_id", guildID, "channel_id", ch.ID)

	bl.lock()
	list, found := withoutID(bl.getBlacklist(), ch.ID)
	if found {
		bl.setBlacklist(list)
	}
	bl.unlock()

	if !found {
		botutil.RespondEphemeral(ctx, e, fmt.Sprintf("<#%d> is not blacklisted.", ch.ID))
		return
	}

	if err := persist(); err != nil {
		b.Log.ErrorContext(ctx, "Failed to persist "+prefix+" blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(ctx, e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(ctx, e, fmt.Sprintf("<#%d> removed from blacklist.", ch.ID))
}

func (b *Bot) handleBlacklistList(ctx context.Context, e *events.ApplicationCommandInteractionCreate, bl blacklistable, prefix string) {
	b.Log.InfoContext(ctx, prefix+" blacklist list", "user_id", e.User().ID, "guild_id", *e.GuildID())

	bl.lock()
	list := make([]snowflake.ID, len(bl.getBlacklist()))
	copy(list, bl.getBlacklist())
	bl.unlock()

	if len(list) == 0 {
		botutil.RespondEphemeral(ctx, e, "No channels are blacklisted.")
		return
	}

	var lines []string
	for _, id := range list {
		lines = append(lines, fmt.Sprintf("<#%d>", id))
	}
	botutil.RespondEphemeral(ctx, e, "**Blacklisted channels:**\n"+strings.Join(lines, "\n"))
}

func (b *Bot) handleBlacklistClear(ctx context.Context, e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, bl blacklistable, persist func() error, prefix string) {
	b.Log.InfoContext(ctx, prefix+" blacklist clear", "user_id", e.User().ID, "guild_id", guildID)

	bl.lock()
	count := len(bl.getBlacklist())
	bl.setBlacklist(nil)
	bl.unlock()

	if count == 0 {
		botutil.RespondEphemeral(ctx, e, "Blacklist is already empty.")
		return
	}

	if err := persist(); err != nil {
		b.Log.ErrorContext(ctx, "Failed to persist "+prefix+" blacklist", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(ctx, e, "Failed to save blacklist.")
		return
	}

	botutil.RespondEphemeral(ctx, e, fmt.Sprintf("Cleared %d entries from the blacklist.", count))
}
