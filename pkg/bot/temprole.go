package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
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

func (b *Bot) loadTempRoleConfig() {
	ctx := context.Background()
	for _, guildID := range b.GuildIDs() {
		st := &tempRoleConfigState{}

		data, err := b.S3.FetchGuildJSON(ctx, "temproleconfig", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing temp role config", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load temp role config", "guild_id", guildID, "error", err)
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode temp role config", "guild_id", guildID, "error", err)
			}
		}

		b.tempRoleConfig[guildID] = st
		b.Log.Info("Loaded temp role config", "guild_id", guildID, "roles", len(st.Roles))
	}
}

func (b *Bot) persistTempRoleConfig(guildID snowflake.ID, st *tempRoleConfigState) error {
	st.mu.Lock()
	data, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return b.S3.SaveGuildJSON(context.Background(), "temproleconfig", fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) saveTempRoleConfig() {
	for guildID, st := range b.tempRoleConfig {
		if err := b.persistTempRoleConfig(guildID, st); err != nil {
			b.Log.Error("Failed to save temp role config", "guild_id", guildID, "error", err)
		}
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
}

func (b *Bot) loadTempRoles() {
	ctx := context.Background()
	for _, guildID := range b.GuildIDs() {
		st := &tempRoleState{}

		data, err := b.S3.FetchGuildJSON(ctx, "temproles", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing temp roles, starting fresh", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load temp roles", "guild_id", guildID, "error", err)
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode temp roles", "guild_id", guildID, "error", err)
			}
		}

		b.tempRoles[guildID] = st
		b.Log.Info("Loaded temp roles", "guild_id", guildID, "count", len(st.Entries))
	}
}

func (b *Bot) persistTempRoles(guildID snowflake.ID, st *tempRoleState) error {
	st.mu.Lock()
	data, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return b.S3.SaveGuildJSON(context.Background(), "temproles", fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) saveTempRoles() {
	for guildID, st := range b.tempRoles {
		if err := b.persistTempRoles(guildID, st); err != nil {
			b.Log.Error("Failed to save temp roles", "guild_id", guildID, "error", err)
		}
	}
}

// --- Expiry loop ---

func (b *Bot) tempRoleLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-ticker.C:
			b.processTempRoleExpiries()
		}
	}
}

func (b *Bot) processTempRoleExpiries() {
	now := time.Now()
	for guildID, st := range b.tempRoles {
		st.mu.Lock()
		var remaining []tempRoleEntry
		var expired []tempRoleEntry
		for _, entry := range st.Entries {
			if now.After(entry.ExpiryAt) {
				expired = append(expired, entry)
			} else {
				remaining = append(remaining, entry)
			}
		}
		if len(expired) == 0 {
			st.mu.Unlock()
			continue
		}
		st.Entries = remaining
		st.mu.Unlock()

		for _, entry := range expired {
			if err := b.Client.Rest.RemoveMemberRole(guildID, entry.UserID, entry.RoleID, rest.WithReason("Temp role expired")); err != nil {
				b.Log.Error("Failed to remove expired temp role", "guild_id", guildID, "user_id", entry.UserID, "role_id", entry.RoleID, "error", err)
			} else {
				b.Log.Info("Removed expired temp role", "guild_id", guildID, "user_id", entry.UserID, "role_id", entry.RoleID)
			}
		}

		if err := b.persistTempRoles(guildID, st); err != nil {
			b.Log.Error("Failed to persist temp roles after expiry", "guild_id", guildID, "error", err)
		}
	}
}

// --- Automatic tracking ---

// ensureTempRoleTimer creates a timer for a user+role if the role is in the
// config whitelist. If resetTimer is false, existing timers are left alone.
// If resetTimer is true, any existing timer is replaced with a fresh one.
// Returns the expiry time and true if a timer was created or reset.
func (b *Bot) ensureTempRoleTimer(guildID snowflake.ID, userID snowflake.ID, roleID snowflake.ID, resetTimer bool) (time.Time, bool) {
	cfgSt := b.tempRoleConfig[guildID]
	if cfgSt == nil {
		return time.Time{}, false
	}

	cfgSt.mu.Lock()
	cfgEntry, found := cfgSt.find(roleID)
	cfgSt.mu.Unlock()
	if !found {
		return time.Time{}, false
	}

	// Block roles with Manage Messages
	if role, ok := b.Client.Caches.Role(guildID, roleID); ok {
		if role.Permissions&discord.PermissionManageMessages != 0 {
			return time.Time{}, false
		}
	}

	st := b.tempRoles[guildID]
	if st == nil {
		st = &tempRoleState{}
		b.tempRoles[guildID] = st
	}

	expiry := time.Now().Add(cfgEntry.Duration)

	st.mu.Lock()
	if resetTimer {
		// Remove existing entry for this user+role so we can replace it
		filtered := st.Entries[:0]
		for _, entry := range st.Entries {
			if !(entry.UserID == userID && entry.RoleID == roleID) {
				filtered = append(filtered, entry)
			}
		}
		st.Entries = filtered
	} else {
		// Check if a timer already exists
		for _, entry := range st.Entries {
			if entry.UserID == userID && entry.RoleID == roleID {
				st.mu.Unlock()
				return time.Time{}, false
			}
		}
	}
	st.Entries = append(st.Entries, tempRoleEntry{
		GuildID:  guildID,
		UserID:   userID,
		RoleID:   roleID,
		ExpiryAt: expiry,
	})
	st.mu.Unlock()

	b.Log.Info("Temp role timer set", "guild_id", guildID, "user_id", userID, "role_id", roleID, "reset", resetTimer, "expires", expiry.Format(time.RFC3339))

	if err := b.persistTempRoles(guildID, st); err != nil {
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
	cfgSt := b.tempRoleConfig[guildID]
	if cfgSt == nil {
		return
	}
	cfgSt.mu.Lock()
	roles := make([]tempRoleConfigEntry, len(cfgSt.Roles))
	copy(roles, cfgSt.Roles)
	cfgSt.mu.Unlock()
	if len(roles) == 0 {
		return
	}

	// Build a set of configured role IDs for fast lookup
	configuredRoles := make(map[snowflake.ID]struct{}, len(roles))
	for _, r := range roles {
		configuredRoles[r.RoleID] = struct{}{}
	}

	for _, memberRoleID := range msg.Member.RoleIDs {
		if _, ok := configuredRoles[memberRoleID]; ok {
			b.ensureTempRoleTimer(guildID, msg.Author.ID, memberRoleID, false)
		}
	}
}

// checkTempRolesOnMemberUpdate checks if any newly added roles are configured
// temp roles and creates timers for them.
func (b *Bot) checkTempRolesOnMemberUpdate(e *events.GuildMemberUpdate) {
	cfgSt := b.tempRoleConfig[e.GuildID]
	if cfgSt == nil {
		return
	}
	cfgSt.mu.Lock()
	roles := make([]tempRoleConfigEntry, len(cfgSt.Roles))
	copy(roles, cfgSt.Roles)
	cfgSt.mu.Unlock()
	if len(roles) == 0 {
		return
	}

	configuredRoles := make(map[snowflake.ID]struct{}, len(roles))
	for _, r := range roles {
		configuredRoles[r.RoleID] = struct{}{}
	}

	// Build set of old role IDs
	oldRoles := make(map[snowflake.ID]struct{}, len(e.OldMember.RoleIDs))
	for _, id := range e.OldMember.RoleIDs {
		oldRoles[id] = struct{}{}
	}

	// Check each new role — if it's configured and wasn't there before, start timer
	for _, newRoleID := range e.Member.RoleIDs {
		if _, wasOld := oldRoles[newRoleID]; wasOld {
			continue
		}
		if _, configured := configuredRoles[newRoleID]; configured {
			b.ensureTempRoleTimer(e.GuildID, e.Member.User.ID, newRoleID, false)
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
	cfgSt := b.tempRoleConfig[*guildID]
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

	// Block roles with Manage Messages
	if role, ok := b.Client.Caches.Role(*guildID, roleID); ok {
		if role.Permissions&discord.PermissionManageMessages != 0 {
			botutil.RespondEphemeral(e, "Cannot assign a role with Manage Messages as a temp role.")
			return
		}
	}

	b.Log.Info("Temp role", "user_id", e.User().ID, "guild_id", *guildID, "target_user_id", targetUser.ID, "role_id", roleID)

	// Add the role
	if err := b.Client.Rest.AddMemberRole(*guildID, targetUser.ID, roleID, rest.WithReason(fmt.Sprintf("Temp role for %s by %s", formatDuration(cfgEntry.Duration), e.User().Username))); err != nil {
		b.Log.Error("Failed to add temp role", "guild_id", *guildID, "user_id", targetUser.ID, "role_id", roleID, "error", err)
		botutil.RespondEphemeral(e, "Failed to add role.")
		return
	}

	// Record the expiry (reset timer if one already exists)
	expiry, _ := b.ensureTempRoleTimer(*guildID, targetUser.ID, roleID, true)

	botutil.RespondEphemeral(e, fmt.Sprintf("Gave <@&%d> to %s for %s (expires <t:%d:R>).", roleID, userMention(targetUser.ID), formatDuration(cfgEntry.Duration), expiry.Unix()))
}

// --- /temprole autocomplete ---

func (b *Bot) handleTempRoleAutocomplete(e *events.AutocompleteInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	cfgSt := b.tempRoleConfig[*guildID]
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

	st := b.tempRoleConfig[*guildID]
	if st == nil {
		st = &tempRoleConfigState{}
		b.tempRoleConfig[*guildID] = st
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

	if role.Permissions&discord.PermissionManageMessages != 0 {
		botutil.RespondEphemeral(e, "Cannot configure a role with Manage Messages as a temp role.")
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

	if err := b.persistTempRoleConfig(guildID, st); err != nil {
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

	if err := b.persistTempRoleConfig(guildID, st); err != nil {
		b.Log.Error("Failed to persist temp role config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Removed <@&%d> from temp roles.", role.ID))
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
