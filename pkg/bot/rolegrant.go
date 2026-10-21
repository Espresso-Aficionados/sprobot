package bot

import (
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

// The bot grants roles on its own: the auto-role, the marketplace poster role
// and temp roles. It can grant any role below its own highest role, and the
// commands that configure these are open to anyone with Manage Messages, so a
// role is checked when it is configured and again each time it is granted.

// dangerousRolePermissions are the permissions a role the bot grants may not
// carry: anything that moderates, configures the server or pings everyone.
var dangerousRolePermissions = []struct {
	Bit  discord.Permissions
	Name string
}{
	{discord.PermissionAdministrator, "Administrator"},
	{discord.PermissionManageGuild, "Manage Server"},
	{discord.PermissionManageRoles, "Manage Roles"},
	{discord.PermissionManageChannels, "Manage Channels"},
	{discord.PermissionManageWebhooks, "Manage Webhooks"},
	{discord.PermissionBanMembers, "Ban Members"},
	{discord.PermissionKickMembers, "Kick Members"},
	{discord.PermissionModerateMembers, "Timeout Members"},
	{discord.PermissionManageMessages, "Manage Messages"},
	{discord.PermissionManageThreads, "Manage Threads"},
	{discord.PermissionManageNicknames, "Manage Nicknames"},
	{discord.PermissionManageGuildExpressions, "Manage Expressions"},
	{discord.PermissionManageEvents, "Manage Events"},
	{discord.PermissionMentionEveryone, "Mention @everyone"},
	{discord.PermissionViewAuditLog, "View Audit Log"},
}

// roleGrantBlocked returns why the bot must never grant role, or "" if it may.
func roleGrantBlocked(guildID snowflake.ID, role discord.Role) string {
	if role.ID == guildID {
		return "@everyone can't be granted."
	}
	if role.Managed {
		return fmt.Sprintf("<@&%d> is managed by an integration, so it can't be granted.", role.ID)
	}
	var names []string
	for _, p := range dangerousRolePermissions {
		if role.Permissions.Has(p.Bit) {
			names = append(names, p.Name)
		}
	}
	if len(names) > 0 {
		return fmt.Sprintf("<@&%d> has moderator permissions (%s), so the bot won't grant it.", role.ID, strings.Join(names, ", "))
	}
	return ""
}

// roleAbove reports whether a sits above b in the server's role list. Discord
// breaks position ties by ID, the older role ranking higher.
func roleAbove(a, b discord.Role) bool {
	if a.Position != b.Position {
		return a.Position > b.Position
	}
	return a.ID < b.ID
}

// highestRole returns the highest of roles, or @everyone if there are none.
func highestRole(guildID snowflake.ID, roles []discord.Role) discord.Role {
	top := discord.Role{ID: guildID, Position: 0}
	for _, r := range roles {
		if roleAbove(r, top) {
			top = r
		}
	}
	return top
}

// roleGrantConfigurer is who is configuring a role for the bot to grant.
type roleGrantConfigurer struct {
	BotTop     discord.Role        // the bot's highest role
	IsOwner    bool                // the invoker owns the server
	Perms      discord.Permissions // the invoker's server-wide permissions
	InvokerTop discord.Role        // the invoker's highest role
}

// validateGrantableRole returns why role can't be configured for the bot to
// grant, or "" if it can. Beyond roleGrantBlocked, the bot must be able to
// grant it, and the invoker must be able to grant it by hand: Manage Roles and
// a higher role, unless they own the server.
func validateGrantableRole(guildID snowflake.ID, role discord.Role, c roleGrantConfigurer) string {
	if reason := roleGrantBlocked(guildID, role); reason != "" {
		return reason
	}
	if !roleAbove(c.BotTop, role) {
		return fmt.Sprintf("<@&%d> isn't below the bot's highest role, so the bot can't grant it.", role.ID)
	}
	if c.IsOwner {
		return ""
	}
	if !c.Perms.Has(discord.PermissionManageRoles) && !c.Perms.Has(discord.PermissionAdministrator) {
		return "You need the Manage Roles permission to choose a role the bot grants."
	}
	if !roleAbove(c.InvokerTop, role) {
		return fmt.Sprintf("<@&%d> isn't below your highest role.", role.ID)
	}
	return ""
}

// checkGrantableRole runs validateGrantableRole for a /config interaction,
// taking the bot's and the invoker's roles from the role cache.
func (b *Bot) checkGrantableRole(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, role discord.Role) string {
	caches := b.Client.Caches
	self, ok := caches.SelfMember(guildID)
	if !ok {
		// The member cache is capped and may have evicted the bot's own entry.
		selfUser, ok := caches.SelfUser()
		if !ok {
			return "Couldn't look up the bot's roles. Try again in a minute."
		}
		m, err := b.Client.Rest.GetMember(guildID, selfUser.ID)
		if err != nil {
			b.Log.Error("Failed to fetch the bot's own member", "guild_id", guildID, "error", err)
			return "Couldn't look up the bot's roles. Try again in a minute."
		}
		self = *m
	}
	invoker := e.Member()
	if invoker == nil {
		return "This only works in a server."
	}
	c := roleGrantConfigurer{
		BotTop:     highestRole(guildID, caches.MemberRoles(self)),
		Perms:      caches.MemberPermissions(invoker.Member),
		InvokerTop: highestRole(guildID, caches.MemberRoles(invoker.Member)),
	}
	if guild, ok := caches.Guild(guildID); ok {
		c.IsOwner = guild.OwnerID == invoker.User.ID
	}
	return validateGrantableRole(guildID, role, c)
}

// grantBlocked is roleGrantBlocked at grant time, for a role that may have
// been edited since it was configured. A role missing from the role cache is
// refused: the bot can't tell what it would be handing out.
func (b *Bot) grantBlocked(guildID, roleID snowflake.ID) string {
	role, ok := b.Client.Caches.Role(guildID, roleID)
	if !ok {
		return fmt.Sprintf("<@&%d> isn't in the bot's role cache, so it can't be checked.", roleID)
	}
	return roleGrantBlocked(guildID, role)
}

// roleGrantRetry is how long the bot waits before retrying a role grant for a
// user after it failed or was refused.
const roleGrantRetry = 10 * time.Minute

// retryAfter remembers, per user, when a call that failed may be tried again,
// so one that keeps failing (a 403 from the role hierarchy, a search index
// that isn't ready) isn't repeated on every message. It is guarded by its
// owner's mutex and not persisted: after a restart each user is retried once.
type retryAfter struct {
	until map[snowflake.ID]time.Time
}

func (r *retryAfter) ready(userID snowflake.ID, now time.Time) bool {
	return !now.Before(r.until[userID])
}

func (r *retryAfter) fail(userID snowflake.ID, now time.Time, wait time.Duration) {
	if r.until == nil {
		r.until = make(map[snowflake.ID]time.Time)
	}
	// Drop entries that have run out, so users who never post again don't
	// pile up.
	if len(r.until) >= 1000 {
		for id, t := range r.until {
			if !now.Before(t) {
				delete(r.until, id)
			}
		}
	}
	r.until[userID] = now.Add(wait)
}

func (r *retryAfter) clear(userID snowflake.ID) {
	delete(r.until, userID)
}
