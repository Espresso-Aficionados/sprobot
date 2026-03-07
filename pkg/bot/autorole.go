package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
)

type autoRoleState struct {
	mu     sync.Mutex
	RoleID snowflake.ID `json:"role_id"`
}

func defaultAutoRoleConfig() map[snowflake.ID]snowflake.ID {
	return map[snowflake.ID]snowflake.ID{
		726985544038612993:  727179134341480508,
		1013566342345019512: 1475339084292685998,
	}
}

func (b *Bot) loadAutoRole() {
	ctx := context.Background()
	defaults := defaultAutoRoleConfig()
	for _, guildID := range b.GuildIDs() {
		st := &autoRoleState{}

		data, err := b.S3.FetchGuildJSON(ctx, "autorole", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			if roleID, ok := defaults[guildID]; ok {
				st.RoleID = roleID
			}
			b.Log.Info("No existing autorole config, using defaults", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load autorole config", "guild_id", guildID, "error", err)
			if roleID, ok := defaults[guildID]; ok {
				st.RoleID = roleID
			}
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode autorole config", "guild_id", guildID, "error", err)
			}
		}

		b.autoRole[guildID] = st
		b.Log.Info("Loaded autorole config", "guild_id", guildID, "role_id", st.RoleID)
	}
}

func (b *Bot) persistAutoRole(guildID snowflake.ID, st *autoRoleState) error {
	st.mu.Lock()
	data, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return b.S3.SaveGuildJSON(context.Background(), "autorole", fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) saveAutoRole() {
	for guildID, st := range b.autoRole {
		if err := b.persistAutoRole(guildID, st); err != nil {
			b.Log.Error("Failed to save autorole config", "guild_id", guildID, "error", err)
		}
	}
}

func (b *Bot) onMemberJoin(e *events.GuildMemberJoin) {
	b.logMemberJoin(e.GuildID, e.Member)
	b.sendWelcomeDM(e.GuildID, e.Member.User.ID)

	st := b.autoRole[e.GuildID]
	if st == nil {
		return
	}
	st.mu.Lock()
	roleID := st.RoleID
	st.mu.Unlock()
	if roleID == 0 {
		return
	}
	if err := b.Client.Rest.AddMemberRole(e.GuildID, e.Member.User.ID, roleID, rest.WithReason("Auto-role on member join")); err != nil {
		b.Log.Error("Failed to assign auto-role on join", "user_id", e.Member.User.ID, "guild_id", e.GuildID, "error", err)
	}
}

func (b *Bot) ensureAutoRole(guildID snowflake.ID, msg discord.Message) {
	st := b.autoRole[guildID]
	if st == nil {
		return
	}
	st.mu.Lock()
	roleID := st.RoleID
	st.mu.Unlock()
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
	if err := b.Client.Rest.AddMemberRole(guildID, msg.Author.ID, roleID, rest.WithReason("Auto-role on first message")); err != nil {
		b.Log.Error("Failed to assign auto-role on message", "user_id", msg.Author.ID, "guild_id", guildID, "error", err)
	}
}

// --- /config autorole handlers ---

func (b *Bot) handleAutoRoleConfig(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.autoRole[*guildID]
	if st == nil {
		st = &autoRoleState{}
		b.autoRole[*guildID] = st
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "set":
		b.handleAutoRoleConfigSet(e, *guildID, st)
	case "show":
		b.handleAutoRoleConfigShow(e, st)
	case "clear":
		b.handleAutoRoleConfigClear(e, *guildID, st)
	}
}

func (b *Bot) handleAutoRoleConfigSet(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *autoRoleState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	role, ok := data.OptRole("role")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a role.")
		return
	}

	b.Log.Info("Autorole config set", "user_id", e.User().ID, "guild_id", guildID, "role_id", role.ID)

	st.mu.Lock()
	st.RoleID = role.ID
	st.mu.Unlock()

	if err := b.persistAutoRole(guildID, st); err != nil {
		b.Log.Error("Failed to persist autorole config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Auto-role set to <@&%d>.", role.ID))
}

func (b *Bot) handleAutoRoleConfigShow(e *events.ApplicationCommandInteractionCreate, st *autoRoleState) {
	b.Log.Info("Autorole config show", "user_id", e.User().ID, "guild_id", *e.GuildID())

	st.mu.Lock()
	roleID := st.RoleID
	st.mu.Unlock()

	if roleID == 0 {
		botutil.RespondEphemeral(e, "**Auto-role:** Not set")
		return
	}
	botutil.RespondEphemeral(e, fmt.Sprintf("**Auto-role:** <@&%d>", roleID))
}

func (b *Bot) handleAutoRoleConfigClear(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *autoRoleState) {
	b.Log.Info("Autorole config clear", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	st.RoleID = 0
	st.mu.Unlock()

	if err := b.persistAutoRole(guildID, st); err != nil {
		b.Log.Error("Failed to persist autorole config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Auto-role disabled.")
}
