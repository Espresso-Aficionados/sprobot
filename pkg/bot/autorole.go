package bot

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

type autoRoleState struct {
	mu     sync.Mutex
	RoleID snowflake.ID `json:"role_id"`

	retry retryAfter // users whose grant on message failed
}

func defaultAutoRoleConfig() map[snowflake.ID]snowflake.ID {
	return map[snowflake.ID]snowflake.ID{
		726985544038612993:  727179134341480508,
		1013566342345019512: 1475339084292685998,
	}
}

func (b *Bot) onMemberJoin(e *events.GuildMemberJoin) {
	b.logMemberJoin(e.GuildID, e.Member)
	b.sendWelcomeDM(e.GuildID, e.Member.User.ID)

	st := b.autoRole.get(e.GuildID)
	if st == nil {
		return
	}
	st.mu.Lock()
	roleID := st.RoleID
	st.mu.Unlock()
	if roleID == 0 {
		return
	}
	if reason := b.grantBlocked(e.GuildID, roleID); reason != "" {
		b.Log.Error("Refusing to assign auto-role on join", "user_id", e.Member.User.ID, "guild_id", e.GuildID, "role_id", roleID, "reason", reason)
		return
	}
	if err := b.Client.Rest.AddMemberRole(e.GuildID, e.Member.User.ID, roleID, rest.WithReason("Auto-role on member join")); err != nil {
		b.Log.Error("Failed to assign auto-role on join", "user_id", e.Member.User.ID, "guild_id", e.GuildID, "error", err)
	}
}

func (b *Bot) ensureAutoRole(guildID snowflake.ID, msg discord.Message) {
	st := b.autoRole.get(guildID)
	if st == nil {
		return
	}
	if msg.Member == nil {
		return
	}
	userID := msg.Author.ID
	now := time.Now()
	st.mu.Lock()
	roleID := st.RoleID
	ready := st.retry.ready(userID, now)
	st.mu.Unlock()
	if roleID == 0 || !ready || slices.Contains(msg.Member.RoleIDs, roleID) {
		return
	}

	var err error
	if reason := b.grantBlocked(guildID, roleID); reason != "" {
		err = errors.New(reason)
	} else {
		err = b.Client.Rest.AddMemberRole(guildID, userID, roleID, rest.WithReason("Auto-role on first message"))
	}
	st.mu.Lock()
	if err != nil {
		st.retry.fail(userID, now, roleGrantRetry)
	} else {
		st.retry.clear(userID)
	}
	st.mu.Unlock()
	if err != nil {
		b.Log.Error("Failed to assign auto-role on message", "user_id", userID, "guild_id", guildID, "role_id", roleID, "error", err)
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

	st := b.autoRole.getOrCreate(*guildID)
	if st == nil {
		botutil.RespondEphemeral(e, "The autorole config couldn't be loaded, so it can't be changed right now. Please try again in a few minutes.")
		return
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

	if reason := b.checkGrantableRole(e, guildID, role); reason != "" {
		botutil.RespondEphemeral(e, reason)
		return
	}

	st.mu.Lock()
	st.RoleID = role.ID
	st.retry = retryAfter{}
	st.mu.Unlock()

	if err := b.autoRole.persist(guildID); err != nil {
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

	if err := b.autoRole.persist(guildID); err != nil {
		b.Log.Error("Failed to persist autorole config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Auto-role disabled.")
}
