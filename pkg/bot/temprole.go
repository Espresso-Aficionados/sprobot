package bot

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

// --- Config: which roles can be temp-assigned and for how long ---

type tempRoleConfigEntry struct {
	RoleID   snowflake.ID  `json:"role_id"`
	Duration time.Duration `json:"duration"` // stored as nanoseconds in JSON
}

type tempRoleConfigState struct {
	mu    sync.Mutex
	Roles []tempRoleConfigEntry `json:"roles"`
}

func (st *tempRoleConfigState) find(roleID snowflake.ID) (tempRoleConfigEntry, bool) {
	for _, r := range st.Roles {
		if r.RoleID == roleID {
			return r, true
		}
	}
	return tempRoleConfigEntry{}, false
}

// configuredTempRoleIDs returns a set of role IDs from the config, or nil if
// no roles are configured. Caller must NOT hold cfgSt.mu.
func (st *tempRoleConfigState) configuredRoleIDs() map[snowflake.ID]struct{} {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.Roles) == 0 {
		return nil
	}
	m := make(map[snowflake.ID]struct{}, len(st.Roles))
	for _, r := range st.Roles {
		m[r.RoleID] = struct{}{}
	}
	return m
}

var tempRoleDurations = map[string]time.Duration{
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"12h": 12 * time.Hour,
	"1d":  24 * time.Hour,
	"3d":  3 * 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"14d": 14 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

func formatDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		days := int(d / (24 * time.Hour))
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	default:
		hours := int(d / time.Hour)
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}
}

// --- Active assignments ---

type tempRoleEntry struct {
	GuildID  snowflake.ID `json:"guild_id"`
	UserID   snowflake.ID `json:"user_id"`
	RoleID   snowflake.ID `json:"role_id"`
	ExpiryAt time.Time    `json:"expiry_at"`
}

type tempRoleState struct {
	mu      sync.Mutex
	Entries []tempRoleEntry `json:"entries"`

	// removed is when the expiry loop last took each user's role away. Not
	// persisted: it only has to outlast message handlers that are running late.
	removed map[tempRoleKey]time.Time
}

type tempRoleKey struct{ UserID, RoleID snowflake.ID }

// tempRoleRemovedTTL is how long the expiry loop remembers a removal. A
// message handler can lag behind it by the poster-role history search (up to
// 30s) plus a few REST calls.
const tempRoleRemovedTTL = 10 * time.Minute

func sameTimer(userID, roleID snowflake.ID) func(tempRoleEntry) bool {
	return func(e tempRoleEntry) bool { return e.UserID == userID && e.RoleID == roleID }
}

// is reports whether e and o are the same timer, expiry included.
func (e tempRoleEntry) is(o tempRoleEntry) bool {
	return e.UserID == o.UserID && e.RoleID == o.RoleID && e.ExpiryAt.Equal(o.ExpiryAt)
}

// set records entry's timer for /temprole, replacing any timer for the same
// user and role, and returns the timers it replaced.
func (st *tempRoleState) set(entry tempRoleEntry) []tempRoleEntry {
	st.mu.Lock()
	defer st.mu.Unlock()
	var replaced []tempRoleEntry
	same := sameTimer(entry.UserID, entry.RoleID)
	st.Entries = slices.DeleteFunc(st.Entries, func(e tempRoleEntry) bool {
		if same(e) {
			replaced = append(replaced, e)
			return true
		}
		return false
	})
	st.Entries = append(st.Entries, entry)
	return replaced
}

// undo takes back a set whose grant failed: entry goes and the timers it
// replaced come back, as the user may still hold the role from before. If
// another /temprole has replaced entry meanwhile, that timer is left alone.
func (st *tempRoleState) undo(entry tempRoleEntry, replaced []tempRoleEntry) {
	st.mu.Lock()
	defer st.mu.Unlock()
	i := slices.IndexFunc(st.Entries, entry.is)
	if i < 0 {
		return
	}
	st.Entries = append(slices.Delete(st.Entries, i, i+1), replaced...)
}

// track records entry's timer for a role the user was seen holding at seen.
// It reports false and adds nothing if the user already has a timer for the
// role, even an expired one whose role is still being removed, or if the
// expiry loop removed the role after seen.
func (st *tempRoleState) track(entry tempRoleEntry, seen time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if slices.ContainsFunc(st.Entries, sameTimer(entry.UserID, entry.RoleID)) {
		return false
	}
	if seen.Before(st.removed[tempRoleKey{entry.UserID, entry.RoleID}]) {
		return false
	}
	st.Entries = append(st.Entries, entry)
	return true
}

// has reports whether entry is still the timer for its user and role.
func (st *tempRoleState) has(entry tempRoleEntry) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return slices.ContainsFunc(st.Entries, entry.is)
}

// clearRole drops every timer for roleID and returns how many there were.
func (st *tempRoleState) clearRole(roleID snowflake.ID) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	n := len(st.Entries)
	st.Entries = slices.DeleteFunc(st.Entries, func(e tempRoleEntry) bool { return e.RoleID == roleID })
	return n - len(st.Entries)
}

// expired returns the entries whose expiry has passed. They stay in the state
// until finish is called for them.
func (st *tempRoleState) expired(now time.Time) []tempRoleEntry {
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []tempRoleEntry
	for _, entry := range st.Entries {
		if now.After(entry.ExpiryAt) {
			out = append(out, entry)
		}
	}
	return out
}

// finish drops expired entry once its role is gone, and remembers that it went
// at at, so a message sent before then doesn't start a new timer. If
// /temprole replaced entry meanwhile, the new timer stays and is returned.
func (st *tempRoleState) finish(entry tempRoleEntry, at time.Time) (tempRoleEntry, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.removed == nil {
		st.removed = make(map[tempRoleKey]time.Time)
	}
	for k, t := range st.removed {
		if at.Sub(t) > tempRoleRemovedTTL {
			delete(st.removed, k)
		}
	}
	st.removed[tempRoleKey{entry.UserID, entry.RoleID}] = at

	i := slices.IndexFunc(st.Entries, sameTimer(entry.UserID, entry.RoleID))
	if i < 0 {
		return tempRoleEntry{}, false
	}
	if !st.Entries[i].is(entry) {
		return st.Entries[i], true
	}
	st.Entries = slices.Delete(st.Entries, i, i+1)
	return tempRoleEntry{}, false
}

// isUnknownMemberOrRole reports whether err says the member left or the role
// was deleted, leaving nothing to remove.
func isUnknownMemberOrRole(err error) bool {
	var restErr *rest.Error
	return errors.As(err, &restErr) &&
		(restErr.Code == rest.JSONErrorCodeUnknownMember || restErr.Code == rest.JSONErrorCodeUnknownRole)
}

// --- Expiry loop ---

// processTempRoleExpiries removes expired temp roles. An entry stays in the
// state until its role is actually gone, so a failed removal is retried on the
// next tick, and a message from the user meanwhile doesn't start a new timer.
func (b *Bot) processTempRoleExpiries() {
	now := time.Now()
	b.tempRoles.each(func(guildID snowflake.ID, st *tempRoleState) {
		expired := st.expired(now)
		if len(expired) == 0 {
			return
		}

		// Check config to skip orphaned timers for roles no longer in the whitelist
		cfgSt := b.tempRoleConfig.get(guildID)

		changed := false
		for _, entry := range expired {
			if cfgSt != nil {
				cfgSt.mu.Lock()
				_, configured := cfgSt.find(entry.RoleID)
				cfgSt.mu.Unlock()
				if !configured {
					b.Log.Info("Skipping orphaned temp role timer", "guild_id", guildID, "user_id", entry.UserID, "role_id", entry.RoleID)
					st.finish(entry, time.Now())
					changed = true
					continue
				}
			}
			// /temprole may have extended it since expired ran.
			if !st.has(entry) {
				continue
			}
			err := b.Client.Rest.RemoveMemberRole(guildID, entry.UserID, entry.RoleID, rest.WithReason("Temp role expired"))
			if err != nil && !isUnknownMemberOrRole(err) {
				b.Log.Error("Failed to remove expired temp role, will retry", "guild_id", guildID, "user_id", entry.UserID, "role_id", entry.RoleID, "error", err)
				continue
			}
			changed = true
			extended, wasExtended := st.finish(entry, time.Now())
			switch {
			case err != nil:
				b.Log.Info("Expired temp role already gone", "guild_id", guildID, "user_id", entry.UserID, "role_id", entry.RoleID, "error", err)
			case wasExtended:
				b.regrantTempRole(guildID, extended)
			default:
				b.Log.Info("Removed expired temp role", "guild_id", guildID, "user_id", entry.UserID, "role_id", entry.RoleID)
				if member, ok := b.Client.Caches.Member(guildID, entry.UserID); ok {
					b.crossPostToModLog(guildID, member.User, discord.Embed{
						Title:       "Temp Role Expired",
						Description: fmt.Sprintf("<@&%d> expired for %s", entry.RoleID, userMention(entry.UserID)),
					})
				}
			}
		}
		if !changed {
			return
		}
		if err := b.tempRoles.persist(guildID); err != nil {
			b.Log.Error("Failed to persist temp roles after expiry", "guild_id", guildID, "error", err)
		}
	})
}

// regrantTempRole grants entry's role again after the expiry loop removed it
// while /temprole was extending it: the extension's grant may have reached
// Discord before the removal did. Granting a role the user holds is a no-op.
func (b *Bot) regrantTempRole(guildID snowflake.ID, entry tempRoleEntry) {
	if reason := b.grantBlocked(guildID, entry.RoleID); reason != "" {
		b.Log.Warn("Not re-granting extended temp role", "guild_id", guildID, "user_id", entry.UserID, "role_id", entry.RoleID, "reason", reason)
		return
	}
	if err := b.Client.Rest.AddMemberRole(guildID, entry.UserID, entry.RoleID, rest.WithReason("Temp role extended")); err != nil {
		b.Log.Error("Failed to re-grant extended temp role", "guild_id", guildID, "user_id", entry.UserID, "role_id", entry.RoleID, "error", err)
		return
	}
	b.Log.Info("Re-granted extended temp role", "guild_id", guildID, "user_id", entry.UserID, "role_id", entry.RoleID)
}

// --- Automatic tracking ---

// tempRoleStateFor returns guildID's timers, creating them if needed. It
// returns nil while the guild's saved timers failed to load: a timer kept
// anywhere else would never be visited by the expiry loop.
func (b *Bot) tempRoleStateFor(guildID snowflake.ID) *tempRoleState {
	return b.tempRoles.getOrCreate(guildID)
}

// ensureTempRoleTimer starts a timer for a configured temp role that userID
// was seen holding at seen, unless one is already running or the expiry loop
// removed the role after seen. The role's permissions aren't checked: the bot
// isn't granting it here, and leaving it untracked would only make it
// permanent. Returns the expiry time and true if a timer was started.
func (b *Bot) ensureTempRoleTimer(guildID snowflake.ID, userID snowflake.ID, roleID snowflake.ID, seen time.Time) (time.Time, bool) {
	cfgSt := b.tempRoleConfig.get(guildID)
	if cfgSt == nil {
		return time.Time{}, false
	}

	cfgSt.mu.Lock()
	cfgEntry, found := cfgSt.find(roleID)
	cfgSt.mu.Unlock()
	if !found {
		return time.Time{}, false
	}

	st := b.tempRoleStateFor(guildID)
	if st == nil {
		b.Log.Error("Temp role timers not loaded; not tracking role", "guild_id", guildID, "user_id", userID, "role_id", roleID)
		return time.Time{}, false
	}
	expiry := time.Now().Add(cfgEntry.Duration)
	if !st.track(tempRoleEntry{GuildID: guildID, UserID: userID, RoleID: roleID, ExpiryAt: expiry}, seen) {
		return time.Time{}, false
	}

	b.Log.Info("Temp role timer set", "guild_id", guildID, "user_id", userID, "role_id", roleID, "expires", expiry.Format(time.RFC3339))

	if err := b.tempRoles.persist(guildID); err != nil {
		b.Log.Error("Failed to persist temp role timer", "guild_id", guildID, "error", err)
	}
	return expiry, true
}

// checkTempRolesOnMessage checks if a message author has any configured temp
// roles and creates timers for any that are untracked.
func (b *Bot) checkTempRolesOnMessage(guildID snowflake.ID, msg discord.Message) {
	if msg.Member == nil {
		return
	}
	cfgSt := b.tempRoleConfig.get(guildID)
	if cfgSt == nil {
		return
	}
	configuredRoles := cfgSt.configuredRoleIDs()
	if configuredRoles == nil {
		return
	}

	for _, memberRoleID := range msg.Member.RoleIDs {
		if _, ok := configuredRoles[memberRoleID]; ok {
			b.ensureTempRoleTimer(guildID, msg.Author.ID, memberRoleID, msg.CreatedAt)
		}
	}
}

// addedRoleIDs returns the role IDs in now that weren't in before.
func addedRoleIDs(before, now []snowflake.ID) []snowflake.ID {
	var added []snowflake.ID
	for _, id := range now {
		if !slices.Contains(before, id) {
			added = append(added, id)
		}
	}
	return added
}

// checkTempRolesOnMemberUpdate checks if any newly added roles are configured
// temp roles and creates timers for them.
func (b *Bot) checkTempRolesOnMemberUpdate(e *events.GuildMemberUpdate) {
	cfgSt := b.tempRoleConfig.get(e.GuildID)
	if cfgSt == nil {
		return
	}
	configuredRoles := cfgSt.configuredRoleIDs()
	if configuredRoles == nil {
		return
	}

	// Start a timer for each configured role the member didn't have before
	for _, newRoleID := range addedRoleIDs(e.OldMember.RoleIDs, e.Member.RoleIDs) {
		if _, configured := configuredRoles[newRoleID]; configured {
			if expiry, created := b.ensureTempRoleTimer(e.GuildID, e.Member.User.ID, newRoleID, time.Now()); created {
				b.crossPostToModLog(e.GuildID, e.Member.User, discord.Embed{
					Title:       "Temp Role Detected",
					Description: fmt.Sprintf("<@&%d> detected on %s, timer started (expires <t:%d:R>)", newRoleID, userMention(e.Member.User.ID), expiry.Unix()),
				})
			}
		}
	}
}

// --- /temprole command ---

func (b *Bot) handleTempRole(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	targetUser := data.User("user")
	if targetUser.ID == 0 {
		return
	}

	roleIDStr, ok := data.OptString("role")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a role.")
		return
	}

	roleID, err := snowflake.Parse(roleIDStr)
	if err != nil {
		botutil.RespondEphemeral(e, "Invalid role.")
		return
	}

	// Look up the role in the config whitelist
	cfgSt := b.tempRoleConfig.get(*guildID)
	if cfgSt == nil {
		botutil.RespondEphemeral(e, "No temp roles are configured. Use `/config temprole add` first.")
		return
	}

	cfgSt.mu.Lock()
	cfgEntry, found := cfgSt.find(roleID)
	cfgSt.mu.Unlock()

	if !found {
		botutil.RespondEphemeral(e, "That role is not configured as a temp role.")
		return
	}

	if reason := b.grantBlocked(*guildID, roleID); reason != "" {
		botutil.RespondEphemeral(e, reason)
		return
	}

	b.Log.Info("Temp role", "user_id", e.User().ID, "guild_id", *guildID, "target_user_id", targetUser.ID, "role_id", roleID)

	// Record the expiry BEFORE adding the role so the GuildMemberUpdate
	// event handler sees the existing timer and skips its mod-log post.
	st := b.tempRoleStateFor(*guildID)
	if st == nil {
		// The saved timers failed to load: a role granted now would never
		// expire.
		botutil.RespondEphemeral(e, "Temp roles are temporarily unavailable, so no role was given. Please try again in a few minutes.")
		return
	}
	entry := tempRoleEntry{GuildID: *guildID, UserID: targetUser.ID, RoleID: roleID, ExpiryAt: time.Now().Add(cfgEntry.Duration)}
	replaced := st.set(entry)
	expiry := entry.ExpiryAt
	b.Log.Info("Temp role timer set", "guild_id", *guildID, "user_id", targetUser.ID, "role_id", roleID, "reset", true, "expires", expiry.Format(time.RFC3339))
	if err := b.tempRoles.persist(*guildID); err != nil {
		b.Log.Error("Failed to persist temp role timer", "guild_id", *guildID, "error", err)
	}

	// Add the role
	if err := b.Client.Rest.AddMemberRole(*guildID, targetUser.ID, roleID, rest.WithReason(fmt.Sprintf("Temp role for %s by %s", formatDuration(cfgEntry.Duration), e.User().Username))); err != nil {
		b.Log.Error("Failed to add temp role", "guild_id", *guildID, "user_id", targetUser.ID, "role_id", roleID, "error", err)
		// Put back the timer this one replaced: the user may still hold the
		// role from before, and without it nothing would remove the role.
		st.undo(entry, replaced)
		if err := b.tempRoles.persist(*guildID); err != nil {
			b.Log.Error("Failed to persist temp role rollback", "guild_id", *guildID, "error", err)
		}
		botutil.RespondEphemeral(e, "Failed to add role.")
		return
	}

	b.crossPostToModLog(*guildID, targetUser, discord.Embed{
		Title:       "Temp Role Added",
		Description: fmt.Sprintf("%s gave <@&%d> to %s for %s (expires <t:%d:R>)", userMention(e.User().ID), roleID, userMention(targetUser.ID), formatDuration(cfgEntry.Duration), expiry.Unix()),
	})

	e.CreateMessage(tempRoleAnnouncement(roleID, targetUser.ID, cfgEntry.Duration, expiry))
}

// tempRoleAnnouncement is /temprole's public reply. It pings only the user who
// got the role, not everyone who has it.
func tempRoleAnnouncement(roleID, userID snowflake.ID, dur time.Duration, expiry time.Time) discord.MessageCreate {
	return discord.MessageCreate{
		Content:         fmt.Sprintf("Gave <@&%d> to %s for %s (expires <t:%d:R>).", roleID, userMention(userID), formatDuration(dur), expiry.Unix()),
		AllowedMentions: &discord.AllowedMentions{Users: []snowflake.ID{userID}},
	}
}

// --- /temprole autocomplete ---

func (b *Bot) handleTempRoleAutocomplete(e *events.AutocompleteInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	cfgSt := b.tempRoleConfig.get(*guildID)
	if cfgSt == nil {
		e.AutocompleteResult(nil)
		return
	}

	current := strings.ToLower(e.Data.String("role"))

	cfgSt.mu.Lock()
	roles := make([]tempRoleConfigEntry, len(cfgSt.Roles))
	copy(roles, cfgSt.Roles)
	cfgSt.mu.Unlock()

	var choices []discord.AutocompleteChoice
	for _, r := range roles {
		name := fmt.Sprintf("%d", r.RoleID)
		// Try to resolve the role name from cache
		if role, ok := b.Client.Caches.Role(*guildID, r.RoleID); ok {
			name = fmt.Sprintf("%s (%s)", role.Name, formatDuration(r.Duration))
		}
		if current != "" && !strings.Contains(strings.ToLower(name), current) {
			continue
		}
		choices = append(choices, discord.AutocompleteChoiceString{
			Name:  name,
			Value: fmt.Sprintf("%d", r.RoleID),
		})
		if len(choices) >= 25 {
			break
		}
	}

	e.AutocompleteResult(choices)
}

// --- /config temprole handlers ---

func (b *Bot) handleTempRoleConfig(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.tempRoleConfig.get(*guildID)
	if st == nil {
		st = &tempRoleConfigState{}
		b.tempRoleConfig.set(*guildID, st)
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "add":
		b.handleTempRoleConfigAdd(e, *guildID, st)
	case "remove":
		b.handleTempRoleConfigRemove(e, *guildID, st)
	case "list":
		b.handleTempRoleConfigList(e, *guildID, st)
	}
}

func (b *Bot) handleTempRoleConfigAdd(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *tempRoleConfigState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	role, ok := data.OptRole("role")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a role.")
		return
	}

	durationStr, ok := data.OptString("duration")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a duration.")
		return
	}

	dur, ok := tempRoleDurations[durationStr]
	if !ok {
		botutil.RespondEphemeral(e, "Invalid duration.")
		return
	}

	b.Log.Info("Temp role config add", "user_id", e.User().ID, "guild_id", guildID, "role_id", role.ID, "duration", durationStr)

	if reason := b.checkGrantableRole(e, guildID, role); reason != "" {
		botutil.RespondEphemeral(e, reason)
		return
	}

	st.mu.Lock()
	// Update existing or add new
	found := false
	for i, r := range st.Roles {
		if r.RoleID == role.ID {
			st.Roles[i].Duration = dur
			found = true
			break
		}
	}
	if !found {
		st.Roles = append(st.Roles, tempRoleConfigEntry{RoleID: role.ID, Duration: dur})
	}
	st.mu.Unlock()

	if err := b.tempRoleConfig.persist(guildID); err != nil {
		b.Log.Error("Failed to persist temp role config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Temp role <@&%d> configured with duration %s.", role.ID, formatDuration(dur)))
}

func (b *Bot) handleTempRoleConfigRemove(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *tempRoleConfigState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	role, ok := data.OptRole("role")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a role.")
		return
	}

	b.Log.Info("Temp role config remove", "user_id", e.User().ID, "guild_id", guildID, "role_id", role.ID)

	st.mu.Lock()
	filtered := st.Roles[:0]
	removed := false
	for _, r := range st.Roles {
		if r.RoleID == role.ID {
			removed = true
			continue
		}
		filtered = append(filtered, r)
	}
	st.Roles = filtered
	st.mu.Unlock()

	if !removed {
		botutil.RespondEphemeral(e, "That role is not configured as a temp role.")
		return
	}

	if err := b.tempRoleConfig.persist(guildID); err != nil {
		b.Log.Error("Failed to persist temp role config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	// Clear active timers for this role
	cleared := 0
	if roleSt := b.tempRoles.get(guildID); roleSt != nil {
		cleared = roleSt.clearRole(role.ID)
		if cleared > 0 {
			if err := b.tempRoles.persist(guildID); err != nil {
				b.Log.Error("Failed to persist temp roles after config remove", "guild_id", guildID, "error", err)
			}
		}
	}

	if cleared > 0 {
		botutil.RespondEphemeral(e, fmt.Sprintf("Removed <@&%d> from temp roles and cleared %d active timer(s).", role.ID, cleared))
	} else {
		botutil.RespondEphemeral(e, fmt.Sprintf("Removed <@&%d> from temp roles.", role.ID))
	}
}

func (b *Bot) handleTempRoleConfigList(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *tempRoleConfigState) {
	b.Log.Info("Temp role config list", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	roles := make([]tempRoleConfigEntry, len(st.Roles))
	copy(roles, st.Roles)
	st.mu.Unlock()

	if len(roles) == 0 {
		botutil.RespondEphemeral(e, "No temp roles configured.")
		return
	}

	var lines []string
	for _, r := range roles {
		lines = append(lines, fmt.Sprintf("- <@&%d> — %s", r.RoleID, formatDuration(r.Duration)))
	}
	botutil.RespondEphemeral(e, "**Configured temp roles:**\n"+strings.Join(lines, "\n"))
}
