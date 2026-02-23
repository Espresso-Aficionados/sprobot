package bot

import (
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

func getAutoRoleID(env string) snowflake.ID {
	switch env {
	case "prod":
		return 727179134341480508
	case "dev":
		return 1475339084292685998
	default:
		return 0
	}
}

func (b *Bot) onMemberJoin(e *events.GuildMemberJoin) {
	b.logMemberJoin(e.GuildID, e.Member)

	roleID := b.autoRoleID
	if roleID == 0 {
		return
	}
	if err := b.Client.Rest.AddMemberRole(e.GuildID, e.Member.User.ID, roleID); err != nil {
		b.Log.Error("Failed to assign auto-role on join", "user_id", e.Member.User.ID, "guild_id", e.GuildID, "error", err)
	}
}

func (b *Bot) ensureAutoRole(guildID snowflake.ID, msg discord.Message) {
	roleID := b.autoRoleID
	if roleID == 0 {
		return
	}
	if msg.Member == nil {
		return
	}
	for _, r := range msg.Member.RoleIDs {
		if r == roleID {
			return
		}
	}
	if err := b.Client.Rest.AddMemberRole(guildID, msg.Author.ID, roleID); err != nil {
		b.Log.Error("Failed to assign auto-role on message", "user_id", msg.Author.ID, "guild_id", guildID, "error", err)
	}
}
