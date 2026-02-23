package bot

import (
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

const autoRoleID snowflake.ID = 727179134341480508

func (b *Bot) onMemberJoin(e *events.GuildMemberJoin) {
	if b.Env != "prod" {
		return
	}
	if err := b.Client.Rest.AddMemberRole(e.GuildID, e.Member.User.ID, autoRoleID); err != nil {
		b.Log.Error("Failed to assign auto-role on join", "user_id", e.Member.User.ID, "guild_id", e.GuildID, "error", err)
	}
}

func (b *Bot) ensureAutoRole(guildID snowflake.ID, msg discord.Message) {
	if b.Env != "prod" {
		return
	}
	if msg.Member == nil {
		return
	}
	for _, roleID := range msg.Member.RoleIDs {
		if roleID == autoRoleID {
			return
		}
	}
	if err := b.Client.Rest.AddMemberRole(guildID, msg.Author.ID, autoRoleID); err != nil {
		b.Log.Error("Failed to assign auto-role on message", "user_id", msg.Author.ID, "guild_id", guildID, "error", err)
	}
}
