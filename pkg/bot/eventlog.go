package bot

import (
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

// Embed colors for event log entries.
const (
	colorRed     = 0xED4245
	colorYellow  = 0xFEE75C
	colorGreen   = 0x57F287
	colorOrange  = 0xE67E22
	colorBlue    = 0x3498DB
	colorDarkRed = 0x992D22
	colorTeal    = 0x1ABC9C
)

type eventLogChannelConfig struct {
	ChannelID snowflake.ID
}

func getEventLogConfig(env string) map[snowflake.ID]eventLogChannelConfig {
	switch env {
	case "prod":
		return map[snowflake.ID]eventLogChannelConfig{
			726985544038612993: {ChannelID: 835704010161258526},
		}
	case "dev":
		return map[snowflake.ID]eventLogChannelConfig{
			1013566342345019512: {ChannelID: 1015659489610960987},
		}
	default:
		return nil
	}
}

func (b *Bot) postEventLog(guildID snowflake.ID, embed discord.Embed) {
	cfg, ok := b.eventLogConfig[guildID]
	if !ok || cfg.ChannelID == 0 {
		return
	}
	embed.Timestamp = timePtr(time.Now())
	if _, err := b.Client.Rest.CreateMessage(cfg.ChannelID, discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	}); err != nil {
		b.Log.Error("Failed to post event log", "guild_id", guildID, "error", err)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

func channelMention(id snowflake.ID) string { return fmt.Sprintf("<#%d>", id) }
func userMention(id snowflake.ID) string    { return fmt.Sprintf("<@%d>", id) }

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// isEventLogChannel returns true if the channel is this guild's event log channel.
func (b *Bot) isEventLogChannel(guildID, channelID snowflake.ID) bool {
	cfg, ok := b.eventLogConfig[guildID]
	return ok && cfg.ChannelID != 0 && cfg.ChannelID == channelID
}

// --- Message events ---

func (b *Bot) onMessageDelete(e *events.GuildMessageDelete) {
	if b.isEventLogChannel(e.GuildID, e.ChannelID) {
		return
	}
	msg := e.Message
	if msg.Author.Bot {
		return
	}

	embed := discord.Embed{
		Title: "Message Deleted",
		Color: colorRed,
		Fields: []discord.EmbedField{
			{Name: "Channel", Value: channelMention(e.ChannelID), Inline: boolPtr(true)},
		},
	}
	if msg.Author.ID != 0 {
		embed.Author = &discord.EmbedAuthor{
			Name:    msg.Author.Username,
			IconURL: msg.Author.EffectiveAvatarURL(),
		}
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Author", Value: userMention(msg.Author.ID), Inline: boolPtr(true),
		})
	}
	if msg.Content != "" {
		embed.Description = truncate(msg.Content, 4000)
	} else {
		embed.Description = "*Message content not cached*"
	}
	b.postEventLog(e.GuildID, embed)
}

func (b *Bot) onMessageUpdate(e *events.GuildMessageUpdate) {
	if b.isEventLogChannel(e.GuildID, e.ChannelID) {
		return
	}
	if e.Message.Author.Bot {
		return
	}
	// Skip if old message wasn't in cache (zero value) — we can't tell if
	// content actually changed, so avoid false "edited" logs from embed
	// unfurls, link previews, etc.
	if e.OldMessage.Author.ID == 0 {
		return
	}
	// Skip embed-only updates (content unchanged)
	if e.OldMessage.Content == e.Message.Content {
		return
	}

	embed := discord.Embed{
		Title: "Message Edited",
		Color: colorYellow,
		Fields: []discord.EmbedField{
			{Name: "Channel", Value: channelMention(e.ChannelID), Inline: boolPtr(true)},
		},
	}
	if e.Message.Author.ID != 0 {
		embed.Author = &discord.EmbedAuthor{
			Name:    e.Message.Author.Username,
			IconURL: e.Message.Author.EffectiveAvatarURL(),
		}
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Author", Value: userMention(e.Message.Author.ID), Inline: boolPtr(true),
		})
	}
	link := messageLink(
		fmt.Sprintf("%d", e.GuildID),
		fmt.Sprintf("%d", e.ChannelID),
		fmt.Sprintf("%d", e.MessageID),
	)
	embed.Fields = append(embed.Fields, discord.EmbedField{
		Name: "Link", Value: fmt.Sprintf("[Jump to message](%s)", link), Inline: boolPtr(true),
	})
	if e.OldMessage.Content != "" {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Before", Value: "```\n" + truncate(e.OldMessage.Content, 1016) + "\n```",
		})
	}
	embed.Fields = append(embed.Fields, discord.EmbedField{
		Name: "After", Value: "```\n" + truncate(e.Message.Content, 1016) + "\n```",
	})
	b.postEventLog(e.GuildID, embed)
}

// --- Member events ---

func (b *Bot) logMemberJoin(guildID snowflake.ID, member discord.Member) {
	accountAge := time.Since(member.User.CreatedAt())
	embed := discord.Embed{
		Title:       "Member Joined",
		Color:       colorGreen,
		Description: fmt.Sprintf("%s joined the server", userMention(member.User.ID)),
		Author: &discord.EmbedAuthor{
			Name:    member.User.Username,
			IconURL: member.User.EffectiveAvatarURL(),
		},
		Fields: []discord.EmbedField{
			{Name: "Account Age", Value: formatDuration(accountAge), Inline: boolPtr(true)},
			{Name: "User ID", Value: fmt.Sprintf("`%d`", member.User.ID), Inline: boolPtr(true)},
		},
	}
	b.postEventLog(guildID, embed)
}

func (b *Bot) onMemberLeave(e *events.GuildMemberLeave) {
	embed := discord.Embed{
		Title:       "Member Left",
		Color:       colorOrange,
		Description: fmt.Sprintf("%s left the server", userMention(e.User.ID)),
		Author: &discord.EmbedAuthor{
			Name:    e.User.Username,
			IconURL: e.User.EffectiveAvatarURL(),
		},
		Fields: []discord.EmbedField{
			{Name: "Account Age", Value: formatDuration(time.Since(e.User.CreatedAt())), Inline: boolPtr(true)},
			{Name: "User ID", Value: fmt.Sprintf("`%d`", e.User.ID), Inline: boolPtr(true)},
		},
	}
	b.postEventLog(e.GuildID, embed)
}

func (b *Bot) onMemberUpdate(e *events.GuildMemberUpdate) {
	// Skip if old member wasn't in cache (zero value)
	if e.OldMember.User.ID == 0 {
		return
	}

	var fields []discord.EmbedField
	title := "Member Updated"

	if derefStr(e.OldMember.Nick) != derefStr(e.Member.Nick) {
		title = "Nickname Changed"
		oldDisplay := derefStr(e.OldMember.Nick)
		if oldDisplay == "" {
			oldDisplay = e.Member.User.Username
		}
		newDisplay := derefStr(e.Member.Nick)
		if newDisplay == "" {
			newDisplay = e.Member.User.Username
		}
		fields = append(fields,
			discord.EmbedField{Name: "Before", Value: oldDisplay, Inline: boolPtr(true)},
			discord.EmbedField{Name: "After", Value: newDisplay, Inline: boolPtr(true)},
		)
	}

	// Role changes are logged via the audit log handler (handleMemberRoleUpdateEntry)
	// and timeouts via handleMemberUpdateEntry — both include moderator + reason.

	if len(fields) == 0 {
		return
	}

	fields = append(fields, discord.EmbedField{
		Name: "User", Value: userMention(e.Member.User.ID), Inline: boolPtr(true),
	})

	embed := discord.Embed{
		Title: title,
		Color: colorBlue,
		Author: &discord.EmbedAuthor{
			Name:    e.Member.User.Username,
			IconURL: e.Member.User.EffectiveAvatarURL(),
		},
		Fields: fields,
	}
	b.postEventLog(e.GuildID, embed)
}

// --- Moderation events ---
// Ban/unban are handled entirely by the audit log handler (handleBanEntry)
// which includes moderator + reason and cross-posts to the mod log.

func (b *Bot) onAuditLogEntry(e *events.GuildAuditLogEntryCreate) {
	entry := e.AuditLogEntry

	switch entry.ActionType {
	case discord.AuditLogEventMemberKick:
		b.handleKickEntry(e.GuildID, entry)
	case discord.AuditLogEventMemberBanAdd, discord.AuditLogEventMemberBanRemove:
		b.handleBanEntry(e.GuildID, entry)
	case discord.AuditLogEventMemberUpdate:
		b.handleMemberUpdateEntry(e.GuildID, entry)
	case discord.AuditLogEventMemberRoleUpdate:
		b.handleMemberRoleUpdateEntry(e.GuildID, entry)
	case discord.AuditLogEventMessageDelete:
		b.handleMessageDeleteEntry(e.GuildID, entry)
	case discord.AuditLogEventChannelUpdate, discord.AuditLogEventChannelDelete:
		b.handleChannelAuditEntry(e.GuildID, entry)
	case discord.AuditLogEventChannelOverwriteCreate,
		discord.AuditLogEventChannelOverwriteUpdate,
		discord.AuditLogEventChannelOverwriteDelete:
		b.handleChannelOverwriteEntry(e.GuildID, entry)
	case discord.AuditLogEventRoleCreate, discord.AuditLogEventRoleUpdate, discord.AuditLogEventRoleDelete:
		b.handleRoleAuditEntry(e.GuildID, entry)
	case discord.AuditLogEventGuildUpdate:
		b.handleGuildUpdateEntry(e.GuildID, entry)
	case discord.AuditLogThreadUpdate:
		b.handleThreadAuditEntry(e.GuildID, entry)
	}
}

func (b *Bot) handleKickEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	b.postModAction(guildID, entry, "Member Kicked", colorRed, true)
}

func (b *Bot) handleBanEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	isBan := entry.ActionType == discord.AuditLogEventMemberBanAdd
	if isBan {
		b.postModAction(guildID, entry, "Member Banned", colorDarkRed, true)
	} else {
		b.postModAction(guildID, entry, "Member Unbanned", colorTeal, false)
	}
}

func (b *Bot) handleMemberUpdateEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	// Only handle timeout changes
	isTimeout := false
	for _, change := range entry.Changes {
		if change.Key == discord.AuditLogChangeKeyCommunicationDisabledUntil {
			isTimeout = true
			break
		}
	}
	if !isTimeout {
		return
	}

	b.postModAction(guildID, entry, "Member Timed Out", colorOrange, true)
}

// postModAction builds and posts a mod action embed with moderator, reason,
// target user info, and optional cross-post to the mod log.
func (b *Bot) postModAction(guildID snowflake.ID, entry discord.AuditLogEntry, title string, color int, crossPost bool) {
	embed := discord.Embed{
		Title: title,
		Color: color,
		Fields: []discord.EmbedField{
			{Name: "User ID", Value: fmt.Sprintf("`%d`", *entry.TargetID)},
		},
	}
	if entry.UserID != 0 {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Moderator", Value: userMention(entry.UserID), Inline: boolPtr(true),
		})
	}
	if entry.Reason != nil && *entry.Reason != "" {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Reason", Value: *entry.Reason,
		})
	}

	var targetUser *discord.User
	if entry.TargetID != nil {
		if user, err := b.Client.Rest.GetUser(*entry.TargetID); err == nil {
			targetUser = user
			embed.Author = &discord.EmbedAuthor{
				Name:    user.Username,
				IconURL: user.EffectiveAvatarURL(),
			}
			embed.Fields[0] = discord.EmbedField{
				Name: "User", Value: fmt.Sprintf("%s (`%d`)", userMention(user.ID), user.ID),
			}
		}
	}

	b.postEventLog(guildID, embed)

	if crossPost && targetUser != nil {
		b.crossPostToModLog(guildID, *targetUser, embed)
	}
}

func (b *Bot) handleMemberRoleUpdateEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	if entry.TargetID == nil {
		return
	}

	var added, removed []discord.PartialRole
	for _, change := range entry.Changes {
		switch change.Key {
		case discord.AuditLogChangeKeyRoleAdd:
			_ = change.UnmarshalNewValue(&added)
		case discord.AuditLogChangeKeyRoleRemove:
			_ = change.UnmarshalNewValue(&removed)
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return
	}

	embed := discord.Embed{
		Title: "Member Roles Updated",
		Color: colorBlue,
		Fields: []discord.EmbedField{
			{Name: "User", Value: fmt.Sprintf("%s (`%d`)", userMention(*entry.TargetID), *entry.TargetID)},
		},
	}
	if len(added) > 0 {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Roles Added", Value: formatPartialRoleMentions(added),
		})
	}
	if len(removed) > 0 {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Roles Removed", Value: formatPartialRoleMentions(removed),
		})
	}
	appendAuditFields(&embed, entry)

	if user, err := b.Client.Rest.GetUser(*entry.TargetID); err == nil {
		embed.Author = &discord.EmbedAuthor{
			Name:    user.Username,
			IconURL: user.EffectiveAvatarURL(),
		}
	}

	b.postEventLog(guildID, embed)
}

func (b *Bot) handleMessageDeleteEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	// Only fires for mod-deletes (not self-deletes).
	if entry.UserID == 0 {
		return
	}

	embed := discord.Embed{
		Title: "Message Deleted by Moderator",
		Color: colorRed,
	}
	if entry.Options != nil && entry.Options.ChannelID != nil {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Channel", Value: channelMention(*entry.Options.ChannelID), Inline: boolPtr(true),
		})
	}
	if entry.TargetID != nil {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Message Author", Value: userMention(*entry.TargetID), Inline: boolPtr(true),
		})
	}
	appendAuditFields(&embed, entry)
	b.postEventLog(guildID, embed)
}

func (b *Bot) handleChannelAuditEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	var title string
	var color int
	switch entry.ActionType {
	case discord.AuditLogEventChannelUpdate:
		title = "Channel Updated"
		color = colorYellow
	case discord.AuditLogEventChannelDelete:
		title = "Channel Deleted"
		color = colorRed
	}

	embed := discord.Embed{
		Title: title,
		Color: color,
	}
	if entry.TargetID != nil {
		if entry.ActionType == discord.AuditLogEventChannelUpdate {
			embed.Fields = append(embed.Fields, discord.EmbedField{
				Name: "Channel", Value: channelMention(*entry.TargetID), Inline: boolPtr(true),
			})
		} else {
			embed.Fields = append(embed.Fields, discord.EmbedField{
				Name: "Channel ID", Value: fmt.Sprintf("`%d`", *entry.TargetID), Inline: boolPtr(true),
			})
		}
	}
	// For deletes, extract name and type from audit log changes.
	if entry.ActionType == discord.AuditLogEventChannelDelete {
		for _, change := range entry.Changes {
			switch change.Key {
			case discord.AuditLogChangeKeyName:
				var name string
				if change.UnmarshalOldValue(&name) == nil && name != "" {
					embed.Fields = append(embed.Fields, discord.EmbedField{
						Name: "Name", Value: name, Inline: boolPtr(true),
					})
				}
			case discord.AuditLogChangeKeyType:
				var chType discord.ChannelType
				if change.UnmarshalOldValue(&chType) == nil {
					embed.Fields = append(embed.Fields, discord.EmbedField{
						Name: "Type", Value: channelTypeName(chType), Inline: boolPtr(true),
					})
				}
			}
		}
	}
	appendAuditFields(&embed, entry)
	b.postEventLog(guildID, embed)
}

func (b *Bot) handleRoleAuditEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	var title string
	var color int
	switch entry.ActionType {
	case discord.AuditLogEventRoleCreate:
		title = "Role Created"
		color = colorGreen
	case discord.AuditLogEventRoleUpdate:
		title = "Role Updated"
		color = colorYellow
	case discord.AuditLogEventRoleDelete:
		title = "Role Deleted"
		color = colorRed
	}

	embed := discord.Embed{
		Title: title,
		Color: color,
	}

	// Try to extract role name from changes
	var roleName string
	for _, change := range entry.Changes {
		if change.Key == discord.AuditLogChangeKeyName {
			// For create/update, new_value has the name; for delete, old_value
			if entry.ActionType == discord.AuditLogEventRoleDelete {
				_ = change.UnmarshalOldValue(&roleName)
			} else {
				_ = change.UnmarshalNewValue(&roleName)
			}
			break
		}
	}
	if roleName != "" {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Role", Value: roleName, Inline: boolPtr(true),
		})
	} else if entry.TargetID != nil {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Role", Value: fmt.Sprintf("<@&%d>", *entry.TargetID), Inline: boolPtr(true),
		})
	}
	appendAuditFields(&embed, entry)
	b.postEventLog(guildID, embed)
}

func (b *Bot) handleGuildUpdateEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	embed := discord.Embed{
		Title: "Server Updated",
		Color: colorYellow,
	}
	appendAuditFields(&embed, entry)
	b.postEventLog(guildID, embed)
}

func (b *Bot) handleThreadAuditEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	embed := discord.Embed{
		Title: "Thread Updated",
		Color: colorYellow,
	}
	if entry.TargetID != nil {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Thread", Value: channelMention(*entry.TargetID), Inline: boolPtr(true),
		})
	}
	appendAuditFields(&embed, entry)
	b.postEventLog(guildID, embed)
}

// appendAuditFields appends "Responsible Moderator" and "Reason" fields from an audit log entry.
func appendAuditFields(embed *discord.Embed, entry discord.AuditLogEntry) {
	if entry.UserID != 0 {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Responsible Moderator", Value: userMention(entry.UserID), Inline: boolPtr(true),
		})
	}
	if entry.Reason != nil && *entry.Reason != "" {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Reason", Value: *entry.Reason,
		})
	}
}

func (b *Bot) handleChannelOverwriteEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	if entry.TargetID == nil {
		return
	}
	channelID := *entry.TargetID

	// Determine action label
	var action string
	switch entry.ActionType {
	case discord.AuditLogEventChannelOverwriteCreate:
		action = "Created"
	case discord.AuditLogEventChannelOverwriteUpdate:
		action = "Updated"
	case discord.AuditLogEventChannelOverwriteDelete:
		action = "Deleted"
	}

	// Get channel name for the title
	channelName := fmt.Sprintf("%d", channelID)
	if ch, err := b.Client.Rest.GetChannel(channelID); err == nil {
		channelName = ch.Name()
	}

	// Determine target (role or member)
	var targetMention string
	if entry.Options != nil {
		id := ""
		if entry.Options.ID != nil {
			id = *entry.Options.ID
		}
		if entry.Options.Type != nil && *entry.Options.Type == "0" {
			// Role
			if entry.Options.RoleName != nil && *entry.Options.RoleName != "" {
				targetMention = "@" + *entry.Options.RoleName
			} else {
				targetMention = fmt.Sprintf("<@&%s>", id)
			}
		} else {
			// Member
			targetMention = fmt.Sprintf("<@%s>", id)
		}
	}

	// Extract old/new allow/deny from changes
	var oldAllow, oldDeny, newAllow, newDeny discord.Permissions
	for _, change := range entry.Changes {
		switch change.Key {
		case discord.AuditLogChangeKeyAllow:
			_ = change.UnmarshalOldValue(&oldAllow)
			_ = change.UnmarshalNewValue(&newAllow)
		case discord.AuditLogChangeKeyDeny:
			_ = change.UnmarshalOldValue(&oldDeny)
			_ = change.UnmarshalNewValue(&newDeny)
		}
	}

	// Build permission diff description
	var desc strings.Builder
	desc.WriteString("Permissions:\n")
	if targetMention != "" {
		desc.WriteString("⬋ ")
		desc.WriteString(targetMention)
		desc.WriteString("\n")
	}
	desc.WriteString(formatPermissionDiff(entry.ActionType, oldAllow, oldDeny, newAllow, newDeny))

	embed := discord.Embed{
		Title:       fmt.Sprintf("Channel Permissions %s: %s", action, channelName),
		Color:       colorYellow,
		Description: desc.String(),
		Fields: []discord.EmbedField{
			{Name: "Channel", Value: channelMention(channelID), Inline: boolPtr(true)},
		},
	}
	appendAuditFields(&embed, entry)
	b.postEventLog(guildID, embed)
}

// channelPermissionBits is an ordered list of channel-relevant permission bits and their display names.
var channelPermissionBits = []struct {
	Bit  discord.Permissions
	Name string
}{
	{discord.PermissionViewChannel, "View Channel"},
	{discord.PermissionManageChannels, "Manage Channels"},
	{discord.PermissionManageRoles, "Manage Permissions"},
	{discord.PermissionCreateInstantInvite, "Create Invite"},
	{discord.PermissionSendMessages, "Send Messages"},
	{discord.PermissionSendMessagesInThreads, "Send Messages in Threads"},
	{discord.PermissionCreatePublicThreads, "Create Public Threads"},
	{discord.PermissionCreatePrivateThreads, "Create Private Threads"},
	{discord.PermissionEmbedLinks, "Embed Links"},
	{discord.PermissionAttachFiles, "Attach Files"},
	{discord.PermissionAddReactions, "Add Reactions"},
	{discord.PermissionUseExternalEmojis, "Use External Emojis"},
	{discord.PermissionUseExternalStickers, "Use External Stickers"},
	{discord.PermissionMentionEveryone, "Mention Everyone"},
	{discord.PermissionManageMessages, "Manage Messages"},
	{discord.PermissionManageThreads, "Manage Threads"},
	{discord.PermissionReadMessageHistory, "Read Message History"},
	{discord.PermissionSendTTSMessages, "Send TTS Messages"},
	{discord.PermissionSendVoiceMessages, "Send Voice Messages"},
	{discord.PermissionSendPolls, "Create Polls"},
	{discord.PermissionUseApplicationCommands, "Use Application Commands"},
	{discord.PermissionConnect, "Connect"},
	{discord.PermissionSpeak, "Speak"},
	{discord.PermissionStream, "Video"},
	{discord.PermissionUseSoundboard, "Use Soundboard"},
	{discord.PermissionUseExternalSounds, "Use External Sounds"},
	{discord.PermissionUseVAD, "Use Voice Activity"},
	{discord.PermissionPrioritySpeaker, "Priority Speaker"},
	{discord.PermissionMuteMembers, "Mute Members"},
	{discord.PermissionDeafenMembers, "Deafen Members"},
	{discord.PermissionMoveMembers, "Move Members"},
	{discord.PermissionManageEvents, "Manage Events"},
	{discord.PermissionUseEmbeddedActivities, "Use Activities"},
}

func formatPermissionDiff(action discord.AuditLogEvent, oldAllow, oldDeny, newAllow, newDeny discord.Permissions) string {
	var lines []string
	for _, p := range channelPermissionBits {
		switch action {
		case discord.AuditLogEventChannelOverwriteCreate:
			if newAllow.Has(p.Bit) {
				lines = append(lines, "✅ "+p.Name)
			} else if newDeny.Has(p.Bit) {
				lines = append(lines, "❌ "+p.Name)
			}
		case discord.AuditLogEventChannelOverwriteDelete:
			if oldAllow.Has(p.Bit) || oldDeny.Has(p.Bit) {
				lines = append(lines, "↩️ "+p.Name)
			}
		case discord.AuditLogEventChannelOverwriteUpdate:
			wasAllowed := oldAllow.Has(p.Bit)
			wasDenied := oldDeny.Has(p.Bit)
			nowAllowed := newAllow.Has(p.Bit)
			nowDenied := newDeny.Has(p.Bit)
			if nowAllowed && !wasAllowed {
				lines = append(lines, "✅ "+p.Name)
			} else if nowDenied && !wasDenied {
				lines = append(lines, "❌ "+p.Name)
			} else if (wasAllowed && !nowAllowed) || (wasDenied && !nowDenied) {
				lines = append(lines, "↩️ "+p.Name)
			}
		}
	}
	if len(lines) == 0 {
		return "*No recognizable permission changes*"
	}
	return strings.Join(lines, "\n")
}

func (b *Bot) crossPostToModLog(guildID snowflake.ID, user discord.User, embed discord.Embed) {
	modLogConfig := getModLogCfg(b.Env)
	if modLogConfig == nil {
		return
	}
	thread := b.findOrCreateModLogThread(modLogConfig.ChannelID, user)
	if thread == nil {
		return
	}
	if _, err := b.Client.Rest.CreateMessage(thread.ID(), discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	}); err != nil {
		b.Log.Error("Failed to cross-post to mod log thread", "user_id", user.ID, "error", err)
	}
}

// --- Channel events ---

func (b *Bot) onChannelCreate(e *events.GuildChannelCreate) {
	embed := discord.Embed{
		Title: "Channel Created",
		Color: colorGreen,
		Fields: []discord.EmbedField{
			{Name: "Channel", Value: channelMention(e.ChannelID), Inline: boolPtr(true)},
			{Name: "Name", Value: e.Channel.Name(), Inline: boolPtr(true)},
			{Name: "Type", Value: channelTypeName(e.Channel.Type()), Inline: boolPtr(true)},
		},
	}
	b.postEventLog(e.GuildID, embed)
}

func (b *Bot) onChannelUpdate(e *events.GuildChannelUpdate) {
	var fields []discord.EmbedField
	if e.OldChannel != nil {
		if e.OldChannel.Name() != e.Channel.Name() {
			fields = append(fields,
				discord.EmbedField{Name: "Old Name", Value: e.OldChannel.Name(), Inline: boolPtr(true)},
				discord.EmbedField{Name: "New Name", Value: e.Channel.Name(), Inline: boolPtr(true)},
			)
		}
		if oldMC, ok := e.OldChannel.(discord.GuildMessageChannel); ok {
			if newMC, ok := e.Channel.(discord.GuildMessageChannel); ok {
				if derefStr(oldMC.Topic()) != derefStr(newMC.Topic()) {
					fields = append(fields,
						discord.EmbedField{Name: "Old Topic", Value: orDash(derefStr(oldMC.Topic())), Inline: boolPtr(true)},
						discord.EmbedField{Name: "New Topic", Value: orDash(derefStr(newMC.Topic())), Inline: boolPtr(true)},
					)
				}
				if oldMC.NSFW() != newMC.NSFW() {
					fields = append(fields, discord.EmbedField{
						Name: "NSFW", Value: fmt.Sprintf("%t → %t", oldMC.NSFW(), newMC.NSFW()),
					})
				}
				if oldMC.RateLimitPerUser() != newMC.RateLimitPerUser() {
					fields = append(fields, discord.EmbedField{
						Name: "Slowmode", Value: fmt.Sprintf("%ds → %ds", oldMC.RateLimitPerUser(), newMC.RateLimitPerUser()),
					})
				}
			}
		}
	}
	// If nothing changed besides permission overwrites, skip — the audit log handler covers those.
	if len(fields) == 0 {
		return
	}

	embed := discord.Embed{
		Title:  "Channel Updated",
		Color:  colorYellow,
		Fields: fields,
	}
	b.postEventLog(e.GuildID, embed)
}

// Channel delete is handled by the audit log handler (handleChannelAuditEntry)
// which includes moderator + reason and extracts name/type from changes.

// --- Thread events ---

func (b *Bot) onThreadCreate(e *events.ThreadCreate) {
	embed := discord.Embed{
		Title: "Thread Created",
		Color: colorGreen,
		Fields: []discord.EmbedField{
			{Name: "Thread", Value: channelMention(e.ThreadID), Inline: boolPtr(true)},
			{Name: "Name", Value: e.Thread.Name(), Inline: boolPtr(true)},
			{Name: "Parent", Value: channelMention(e.ParentID), Inline: boolPtr(true)},
			{Name: "Creator", Value: userMention(e.Thread.OwnerID), Inline: boolPtr(true)},
		},
	}
	b.postEventLog(e.GuildID, embed)
}

func (b *Bot) onThreadUpdate(e *events.ThreadUpdate) {
	var fields []discord.EmbedField
	if e.OldThread.Name() != e.Thread.Name() {
		fields = append(fields,
			discord.EmbedField{Name: "Old Name", Value: e.OldThread.Name(), Inline: boolPtr(true)},
			discord.EmbedField{Name: "New Name", Value: e.Thread.Name(), Inline: boolPtr(true)},
		)
	}
	if e.OldThread.ThreadMetadata.Archived != e.Thread.ThreadMetadata.Archived {
		fields = append(fields, discord.EmbedField{
			Name: "Archived", Value: fmt.Sprintf("%t → %t", e.OldThread.ThreadMetadata.Archived, e.Thread.ThreadMetadata.Archived), Inline: boolPtr(true),
		})
	}
	if e.OldThread.ThreadMetadata.Locked != e.Thread.ThreadMetadata.Locked {
		fields = append(fields, discord.EmbedField{
			Name: "Locked", Value: fmt.Sprintf("%t → %t", e.OldThread.ThreadMetadata.Locked, e.Thread.ThreadMetadata.Locked), Inline: boolPtr(true),
		})
	}
	if e.OldThread.ThreadMetadata.AutoArchiveDuration != e.Thread.ThreadMetadata.AutoArchiveDuration {
		fields = append(fields, discord.EmbedField{
			Name: "Auto-Archive", Value: fmt.Sprintf("%d min → %d min", e.OldThread.ThreadMetadata.AutoArchiveDuration, e.Thread.ThreadMetadata.AutoArchiveDuration), Inline: boolPtr(true),
		})
	}
	if e.OldThread.ThreadMetadata.Invitable != e.Thread.ThreadMetadata.Invitable {
		fields = append(fields, discord.EmbedField{
			Name: "Invitable", Value: fmt.Sprintf("%t → %t", e.OldThread.ThreadMetadata.Invitable, e.Thread.ThreadMetadata.Invitable), Inline: boolPtr(true),
		})
	}
	if len(fields) == 0 {
		return
	}

	fields = append(fields,
		discord.EmbedField{Name: "Thread", Value: channelMention(e.ThreadID), Inline: boolPtr(true)},
		discord.EmbedField{Name: "Parent", Value: channelMention(e.ParentID), Inline: boolPtr(true)},
	)
	embed := discord.Embed{
		Title:  "Thread Updated",
		Color:  colorYellow,
		Fields: fields,
	}
	b.postEventLog(e.GuildID, embed)
}

func (b *Bot) onThreadDelete(e *events.ThreadDelete) {
	embed := discord.Embed{
		Title: "Thread Deleted",
		Color: colorRed,
		Fields: []discord.EmbedField{
			{Name: "Name", Value: e.Thread.Name(), Inline: boolPtr(true)},
			{Name: "Parent", Value: channelMention(e.ParentID), Inline: boolPtr(true)},
		},
	}
	b.postEventLog(e.GuildID, embed)
}

// --- Role events ---
// Role create and delete are handled by the audit log handler (handleRoleAuditEntry)
// which includes moderator + reason info.

func (b *Bot) onRoleUpdate(e *events.RoleUpdate) {
	var fields []discord.EmbedField
	if e.OldRole.Name != e.Role.Name {
		fields = append(fields,
			discord.EmbedField{Name: "Old Name", Value: e.OldRole.Name, Inline: boolPtr(true)},
			discord.EmbedField{Name: "New Name", Value: e.Role.Name, Inline: boolPtr(true)},
		)
	}
	if e.OldRole.Color != e.Role.Color {
		fields = append(fields,
			discord.EmbedField{Name: "Old Color", Value: fmt.Sprintf("#%06X", e.OldRole.Color), Inline: boolPtr(true)},
			discord.EmbedField{Name: "New Color", Value: fmt.Sprintf("#%06X", e.Role.Color), Inline: boolPtr(true)},
		)
	}
	// Skip permissions diff when old is 0 — likely a stale cache artifact from
	// role repositioning. Real permission changes are still caught by the audit
	// log handler (handleRoleAuditEntry) with moderator info.
	if e.OldRole.Permissions != e.Role.Permissions && e.OldRole.Permissions != 0 {
		fields = append(fields,
			discord.EmbedField{Name: "Old Permissions", Value: fmt.Sprintf("`%d`", e.OldRole.Permissions), Inline: boolPtr(true)},
			discord.EmbedField{Name: "New Permissions", Value: fmt.Sprintf("`%d`", e.Role.Permissions), Inline: boolPtr(true)},
		)
	}
	if e.OldRole.Hoist != e.Role.Hoist {
		fields = append(fields, discord.EmbedField{
			Name: "Hoisted", Value: fmt.Sprintf("%t → %t", e.OldRole.Hoist, e.Role.Hoist),
		})
	}
	if e.OldRole.Mentionable != e.Role.Mentionable {
		fields = append(fields, discord.EmbedField{
			Name: "Mentionable", Value: fmt.Sprintf("%t → %t", e.OldRole.Mentionable, e.Role.Mentionable),
		})
	}
	if derefStr(e.OldRole.Icon) != derefStr(e.Role.Icon) {
		fields = append(fields, discord.EmbedField{
			Name: "Icon Changed", Value: "Yes",
		})
	}
	if derefStr(e.OldRole.Emoji) != derefStr(e.Role.Emoji) {
		old := derefStr(e.OldRole.Emoji)
		if old == "" {
			old = "-"
		}
		new := derefStr(e.Role.Emoji)
		if new == "" {
			new = "-"
		}
		fields = append(fields,
			discord.EmbedField{Name: "Old Emoji", Value: old, Inline: boolPtr(true)},
			discord.EmbedField{Name: "New Emoji", Value: new, Inline: boolPtr(true)},
		)
	}
	if len(fields) == 0 {
		return
	}

	embed := discord.Embed{
		Title:  "Role Updated",
		Color:  colorYellow,
		Fields: fields,
	}
	b.postEventLog(e.GuildID, embed)
}

// --- Server events ---

func (b *Bot) onGuildUpdate(e *events.GuildUpdate) {
	var fields []discord.EmbedField
	if e.OldGuild.Name != e.Guild.Name {
		fields = append(fields,
			discord.EmbedField{Name: "Old Name", Value: e.OldGuild.Name, Inline: boolPtr(true)},
			discord.EmbedField{Name: "New Name", Value: e.Guild.Name, Inline: boolPtr(true)},
		)
	}
	if derefStr(e.OldGuild.Description) != derefStr(e.Guild.Description) {
		fields = append(fields,
			discord.EmbedField{Name: "Old Description", Value: orDash(derefStr(e.OldGuild.Description)), Inline: boolPtr(true)},
			discord.EmbedField{Name: "New Description", Value: orDash(derefStr(e.Guild.Description)), Inline: boolPtr(true)},
		)
	}
	if derefStr(e.OldGuild.Icon) != derefStr(e.Guild.Icon) {
		fields = append(fields, discord.EmbedField{
			Name: "Icon Changed", Value: "Yes",
		})
	}
	if derefStr(e.OldGuild.Banner) != derefStr(e.Guild.Banner) {
		fields = append(fields, discord.EmbedField{
			Name: "Banner Changed", Value: "Yes",
		})
	}
	if derefStr(e.OldGuild.Splash) != derefStr(e.Guild.Splash) {
		fields = append(fields, discord.EmbedField{
			Name: "Splash Changed", Value: "Yes",
		})
	}
	if e.OldGuild.VerificationLevel != e.Guild.VerificationLevel {
		fields = append(fields, discord.EmbedField{
			Name: "Verification Level", Value: fmt.Sprintf("%d → %d", e.OldGuild.VerificationLevel, e.Guild.VerificationLevel),
		})
	}
	if e.OldGuild.ExplicitContentFilter != e.Guild.ExplicitContentFilter {
		fields = append(fields, discord.EmbedField{
			Name: "Explicit Content Filter", Value: fmt.Sprintf("%d → %d", e.OldGuild.ExplicitContentFilter, e.Guild.ExplicitContentFilter),
		})
	}
	if e.OldGuild.DefaultMessageNotifications != e.Guild.DefaultMessageNotifications {
		fields = append(fields, discord.EmbedField{
			Name: "Default Notifications", Value: fmt.Sprintf("%d → %d", e.OldGuild.DefaultMessageNotifications, e.Guild.DefaultMessageNotifications),
		})
	}
	if e.OldGuild.AfkTimeout != e.Guild.AfkTimeout {
		fields = append(fields, discord.EmbedField{
			Name: "AFK Timeout", Value: fmt.Sprintf("%ds → %ds", e.OldGuild.AfkTimeout, e.Guild.AfkTimeout),
		})
	}
	if derefSnowflake(e.OldGuild.AfkChannelID) != derefSnowflake(e.Guild.AfkChannelID) {
		fields = append(fields, discord.EmbedField{
			Name: "AFK Channel", Value: fmt.Sprintf("%s → %s", channelMentionOrDash(e.OldGuild.AfkChannelID), channelMentionOrDash(e.Guild.AfkChannelID)),
		})
	}
	if derefSnowflake(e.OldGuild.SystemChannelID) != derefSnowflake(e.Guild.SystemChannelID) {
		fields = append(fields, discord.EmbedField{
			Name: "System Channel", Value: fmt.Sprintf("%s → %s", channelMentionOrDash(e.OldGuild.SystemChannelID), channelMentionOrDash(e.Guild.SystemChannelID)),
		})
	}
	if derefSnowflake(e.OldGuild.RulesChannelID) != derefSnowflake(e.Guild.RulesChannelID) {
		fields = append(fields, discord.EmbedField{
			Name: "Rules Channel", Value: fmt.Sprintf("%s → %s", channelMentionOrDash(e.OldGuild.RulesChannelID), channelMentionOrDash(e.Guild.RulesChannelID)),
		})
	}
	if e.OldGuild.PremiumProgressBarEnabled != e.Guild.PremiumProgressBarEnabled {
		fields = append(fields, discord.EmbedField{
			Name: "Boost Progress Bar", Value: fmt.Sprintf("%t → %t", e.OldGuild.PremiumProgressBarEnabled, e.Guild.PremiumProgressBarEnabled),
		})
	}
	if len(fields) == 0 {
		return
	}

	embed := discord.Embed{
		Title:  "Server Updated",
		Color:  colorYellow,
		Fields: fields,
	}
	b.postEventLog(e.GuildID, embed)
}

// --- Helpers ---

func boolPtr(v bool) *bool { return &v }

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefSnowflake(p *snowflake.ID) snowflake.ID {
	if p == nil {
		return 0
	}
	return *p
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func channelMentionOrDash(p *snowflake.ID) string {
	if p == nil || *p == 0 {
		return "-"
	}
	return channelMention(*p)
}

func formatPartialRoleMentions(roles []discord.PartialRole) string {
	mentions := make([]string, len(roles))
	for i, r := range roles {
		mentions[i] = fmt.Sprintf("<@&%d>", r.ID)
	}
	return strings.Join(mentions, ", ")
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days > 365 {
		years := days / 365
		return fmt.Sprintf("%d years, %d days", years, days%365)
	}
	if days > 0 {
		return fmt.Sprintf("%d days", days)
	}
	hours := int(d.Hours())
	if hours > 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}

func channelTypeName(t discord.ChannelType) string {
	switch t {
	case discord.ChannelTypeGuildText:
		return "Text"
	case discord.ChannelTypeGuildVoice:
		return "Voice"
	case discord.ChannelTypeGuildCategory:
		return "Category"
	case discord.ChannelTypeGuildNews:
		return "Announcement"
	case discord.ChannelTypeGuildStageVoice:
		return "Stage"
	case discord.ChannelTypeGuildForum:
		return "Forum"
	case discord.ChannelTypeGuildMedia:
		return "Media"
	default:
		return fmt.Sprintf("Type %d", t)
	}
}
