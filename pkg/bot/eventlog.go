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
	oldNick := e.OldMember.Nick
	newNick := e.Member.Nick
	if oldNick == newNick {
		return
	}
	oldDisplay := oldNick
	if oldDisplay == nil || *oldDisplay == "" {
		oldDisplay = &e.Member.User.Username
	}
	newDisplay := newNick
	if newDisplay == nil || *newDisplay == "" {
		newDisplay = &e.Member.User.Username
	}

	embed := discord.Embed{
		Title: "Nickname Changed",
		Color: colorBlue,
		Author: &discord.EmbedAuthor{
			Name:    e.Member.User.Username,
			IconURL: e.Member.User.EffectiveAvatarURL(),
		},
		Fields: []discord.EmbedField{
			{Name: "Before", Value: *oldDisplay, Inline: boolPtr(true)},
			{Name: "After", Value: *newDisplay, Inline: boolPtr(true)},
			{Name: "User", Value: userMention(e.Member.User.ID), Inline: boolPtr(true)},
		},
	}
	b.postEventLog(e.GuildID, embed)
}

// --- Moderation events ---

func (b *Bot) onGuildBan(e *events.GuildBan) {
	embed := discord.Embed{
		Title: "Member Banned",
		Color: colorDarkRed,
		Author: &discord.EmbedAuthor{
			Name:    e.User.Username,
			IconURL: e.User.EffectiveAvatarURL(),
		},
		Fields: []discord.EmbedField{
			{Name: "User", Value: fmt.Sprintf("%s (`%d`)", userMention(e.User.ID), e.User.ID)},
		},
	}
	b.postEventLog(e.GuildID, embed)

	// Cross-post to mod log forum thread
	b.crossPostToModLog(e.GuildID, e.User, embed)
}

func (b *Bot) onGuildUnban(e *events.GuildUnban) {
	embed := discord.Embed{
		Title: "Member Unbanned",
		Color: colorTeal,
		Author: &discord.EmbedAuthor{
			Name:    e.User.Username,
			IconURL: e.User.EffectiveAvatarURL(),
		},
		Fields: []discord.EmbedField{
			{Name: "User", Value: fmt.Sprintf("%s (`%d`)", userMention(e.User.ID), e.User.ID)},
		},
	}
	b.postEventLog(e.GuildID, embed)
}

func (b *Bot) onAuditLogEntry(e *events.GuildAuditLogEntryCreate) {
	entry := e.AuditLogEntry

	switch entry.ActionType {
	case discord.AuditLogEventMemberKick:
		b.handleKickEntry(e.GuildID, entry)
	case discord.AuditLogEventMemberUpdate:
		b.handleMemberUpdateEntry(e.GuildID, entry)
	case discord.AuditLogEventChannelOverwriteCreate,
		discord.AuditLogEventChannelOverwriteUpdate,
		discord.AuditLogEventChannelOverwriteDelete:
		b.handleChannelOverwriteEntry(e.GuildID, entry)
	}
}

func (b *Bot) handleKickEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	embed := discord.Embed{
		Title: "Member Kicked",
		Color: colorRed,
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

	// Try to get target user info for the embed author
	if entry.TargetID != nil {
		if user, err := b.Client.Rest.GetUser(*entry.TargetID); err == nil {
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

	// Cross-post to mod log forum thread
	if entry.TargetID != nil {
		if user, err := b.Client.Rest.GetUser(*entry.TargetID); err == nil {
			b.crossPostToModLog(guildID, *user, embed)
		}
	}
}

func (b *Bot) handleMemberUpdateEntry(guildID snowflake.ID, entry discord.AuditLogEntry) {
	// Check if this is a timeout (communication_disabled_until change)
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

	embed := discord.Embed{
		Title: "Member Timed Out",
		Color: colorOrange,
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

	// Try to get target user info
	if entry.TargetID != nil {
		if user, err := b.Client.Rest.GetUser(*entry.TargetID); err == nil {
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

	// Cross-post to mod log forum thread
	if entry.TargetID != nil {
		if user, err := b.Client.Rest.GetUser(*entry.TargetID); err == nil {
			b.crossPostToModLog(guildID, *user, embed)
		}
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
	if entry.UserID != 0 {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Responsible Moderator", Value: userMention(entry.UserID), Inline: boolPtr(true),
		})
	}
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
	newName := e.Channel.Name()
	if e.OldChannel != nil {
		oldName := e.OldChannel.Name()
		if oldName != newName {
			fields = append(fields,
				discord.EmbedField{Name: "Old Name", Value: oldName, Inline: boolPtr(true)},
				discord.EmbedField{Name: "New Name", Value: newName, Inline: boolPtr(true)},
			)
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

func (b *Bot) onChannelDelete(e *events.GuildChannelDelete) {
	embed := discord.Embed{
		Title: "Channel Deleted",
		Color: colorRed,
		Fields: []discord.EmbedField{
			{Name: "Name", Value: e.Channel.Name(), Inline: boolPtr(true)},
			{Name: "Type", Value: channelTypeName(e.Channel.Type()), Inline: boolPtr(true)},
		},
	}
	b.postEventLog(e.GuildID, embed)
}

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

func (b *Bot) onRoleCreate(e *events.RoleCreate) {
	embed := discord.Embed{
		Title: "Role Created",
		Color: colorGreen,
		Fields: []discord.EmbedField{
			{Name: "Name", Value: e.Role.Name, Inline: boolPtr(true)},
			{Name: "Color", Value: fmt.Sprintf("#%06X", e.Role.Color), Inline: boolPtr(true)},
		},
	}
	b.postEventLog(e.GuildID, embed)
}

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
	if len(fields) == 0 {
		fields = append(fields, discord.EmbedField{
			Name: "Role", Value: e.Role.Name,
		})
	}

	embed := discord.Embed{
		Title:  "Role Updated",
		Color:  colorYellow,
		Fields: fields,
	}
	b.postEventLog(e.GuildID, embed)
}

func (b *Bot) onRoleDelete(e *events.RoleDelete) {
	embed := discord.Embed{
		Title: "Role Deleted",
		Color: colorRed,
		Fields: []discord.EmbedField{
			{Name: "Name", Value: e.Role.Name, Inline: boolPtr(true)},
		},
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
	if len(fields) == 0 {
		fields = append(fields, discord.EmbedField{
			Name: "Server", Value: e.Guild.Name,
		})
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
