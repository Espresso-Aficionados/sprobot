package botutil

import (
	"slices"

	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

// AllowedGuildIDs are the only guilds the bots serve. A bot added to any other
// guild ignores it (it registers no commands, loads no state and turns away
// interactions there) but never leaves it. Adding a guild means adding it here
// and giving it an entry in every per-guild config.
var AllowedGuildIDs = []snowflake.ID{
	726985544038612993,  // prod: Espresso Aficionados
	1013566342345019512, // dev
}

// GuildAllowed reports whether guildID is in AllowedGuildIDs.
func GuildAllowed(guildID snowflake.ID) bool {
	return slices.Contains(AllowedGuildIDs, guildID)
}

// GuildInteraction is an interaction that can be answered with a message.
type GuildInteraction interface {
	MessageResponder
	GuildID() *snowflake.ID
}

// RejectGuildNotAllowed turns away an interaction from a guild outside
// AllowedGuildIDs with an ephemeral notice and reports whether it did. Every
// interaction handler calls it first: commands registered in a guild before it
// was dropped stay registered on Discord, and must not create state there.
// Interactions outside a guild are not rejected.
func (b *BaseBot) RejectGuildNotAllowed(e GuildInteraction) bool {
	guildID := e.GuildID()
	if guildID == nil || GuildAllowed(*guildID) {
		return false
	}
	b.Log.Info("Rejecting interaction from guild that is not in botutil.AllowedGuildIDs", "guild_id", *guildID)
	RespondEphemeral(e, "This bot isn't set up for this server.")
	return true
}

// OnGuildJoin is the shared guild-join handler for all bots.
func (b *BaseBot) OnGuildJoin(e *events.GuildJoin) {
	b.warnIfNotAllowed(e.GuildID)
}

// warnIfNotAllowed logs, once per guild, that a guild outside AllowedGuildIDs
// is being ignored.
func (b *BaseBot) warnIfNotAllowed(guildID snowflake.ID) {
	if GuildAllowed(guildID) {
		return
	}
	b.ignoredGuildsMu.Lock()
	warned := b.ignoredGuilds[guildID]
	if b.ignoredGuilds == nil {
		b.ignoredGuilds = make(map[snowflake.ID]bool)
	}
	b.ignoredGuilds[guildID] = true
	b.ignoredGuildsMu.Unlock()

	if !warned {
		b.Log.Warn("Ignoring guild that is not in botutil.AllowedGuildIDs", "guild_id", guildID)
	}
}
