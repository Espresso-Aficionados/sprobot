package stickybot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

const (
	cmdContextMenu     = "Sticky this message"
	cmdSlash           = "sticky"
	subStop            = "stop"
	subStart           = "start"
	subRemove          = "remove"
	subList            = "list"
	modalPrefix        = "sticky_config_"
	fieldMinIdle       = "min_idle_mins"
	fieldMaxIdle       = "max_idle_mins"
	fieldThreshold     = "msg_threshold"
	fieldTimeThreshold = "time_threshold_mins"
)

func (b *Bot) registerAllCommands() error {
	perm := discord.PermissionManageMessages

	commands := []discord.ApplicationCommandCreate{
		discord.MessageCommandCreate{
			Name:                     cmdContextMenu,
			DefaultMemberPermissions: omit.NewPtr(perm),
		},
		discord.SlashCommandCreate{
			Name:                     cmdSlash,
			Description:              "Manage sticky messages",
			DefaultMemberPermissions: omit.NewPtr(perm),
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionSubCommand{
					Name:        subStop,
					Description: "Pause the sticky in the current channel",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        subStart,
					Description: "Resume the sticky in the current channel",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        subRemove,
					Description: "Permanently delete the sticky in the current channel",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        subList,
					Description: "List all stickies in this server",
				},
			},
		},
	}

	return botutil.RegisterGuildCommands(b.Client, b.Env, commands, b.Log)
}

func (b *Bot) onCommand(e *events.ApplicationCommandInteractionCreate) {
	if e.GuildID() == nil {
		return
	}

	switch d := e.Data.(type) {
	case discord.MessageCommandInteractionData:
		if d.CommandName() == cmdContextMenu {
			b.handleStickyMenu(e)
		}
	case discord.SlashCommandInteractionData:
		if d.CommandName() == cmdSlash {
			sub := d.SubCommandName
			if sub == nil {
				return
			}
			switch *sub {
			case subStop:
				b.handleStickyStop(e)
			case subStart:
				b.handleStickyStart(e)
			case subRemove:
				b.handleStickyRemove(e)
			case subList:
				b.handleStickyList(e)
			}
		}
	}
}

func (b *Bot) onModal(e *events.ModalSubmitInteractionCreate) {
	if e.GuildID() == nil {
		return
	}
	if strings.HasPrefix(e.Data.CustomID, modalPrefix) {
		b.handleStickyConfigModal(e)
	}
}

func (b *Bot) handleStickyMenu(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.MessageCommandInteractionData)
	if !ok {
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}
	msg := data.TargetMessage()

	b.Log.Info("Sticky menu opened", "user_id", e.User().ID, "guild_id", *e.GuildID(), "channel_id", msg.ChannelID, "message_id", msg.ID)

	// Encode channel+message IDs into the modal custom ID
	err := e.Modal(discord.ModalCreate{
		CustomID: fmt.Sprintf("%s%d_%d", modalPrefix, msg.ChannelID, msg.ID),
		Title:    "Sticky Message Settings",
		Components: []discord.LayoutComponent{
			discord.NewLabel(
				"Min idle time (quiet mins before repost)",
				discord.TextInputComponent{
					CustomID: fieldMinIdle,
					Style:    discord.TextInputStyleShort,
					Value:    "15",
					Required: true,
				},
			),
			discord.NewLabel(
				"Max idle time (force repost after mins)",
				discord.TextInputComponent{
					CustomID: fieldMaxIdle,
					Style:    discord.TextInputStyleShort,
					Value:    "30",
					Required: true,
				},
			),
			discord.NewLabel(
				"Message threshold (msgs to arm idle)",
				discord.TextInputComponent{
					CustomID: fieldThreshold,
					Style:    discord.TextInputStyleShort,
					Value:    "30",
					Required: true,
				},
			),
			discord.NewLabel(
				"Time threshold (mins to arm idle)",
				discord.TextInputComponent{
					CustomID: fieldTimeThreshold,
					Style:    discord.TextInputStyleShort,
					Value:    "10",
					Required: true,
				},
			),
		},
	})
	if err != nil {
		b.Log.Error("Failed to show sticky config modal", "error", err)
	}
}

func (b *Bot) handleStickyConfigModal(e *events.ModalSubmitInteractionCreate) {
	guildID := *e.GuildID()

	parts := strings.SplitN(strings.TrimPrefix(e.Data.CustomID, modalPrefix), "_", 2)
	if len(parts) != 2 {
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}
	channelID, err := snowflake.Parse(parts[0])
	if err != nil {
		b.Log.Error("Invalid channel ID in sticky config modal", "value", parts[0], "error", err)
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}
	messageID, err := snowflake.Parse(parts[1])
	if err != nil {
		b.Log.Error("Invalid message ID in sticky config modal", "value", parts[1], "error", err)
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}

	minIdleStr := e.Data.Text(fieldMinIdle)
	maxIdleStr := e.Data.Text(fieldMaxIdle)
	threshStr := e.Data.Text(fieldThreshold)
	timeThreshStr := e.Data.Text(fieldTimeThreshold)

	minIdle, err := strconv.Atoi(minIdleStr)
	if err != nil || minIdle < 0 {
		botutil.RespondEphemeral(e, "Min idle must be a non-negative number.")
		return
	}
	maxIdle, err := strconv.Atoi(maxIdleStr)
	if err != nil || maxIdle < 1 {
		botutil.RespondEphemeral(e, "Max idle must be a positive number.")
		return
	}
	if maxIdle <= minIdle {
		botutil.RespondEphemeral(e, "Max idle must be greater than min idle.")
		return
	}
	threshold, err := strconv.Atoi(threshStr)
	if err != nil || threshold < 0 {
		botutil.RespondEphemeral(e, "Message threshold must be a non-negative number.")
		return
	}
	timeThreshold, err := strconv.Atoi(timeThreshStr)
	if err != nil || timeThreshold < 0 {
		botutil.RespondEphemeral(e, "Time threshold must be a non-negative number.")
		return
	}
	if threshold == 0 && timeThreshold == 0 {
		botutil.RespondEphemeral(e, "At least one of message threshold or time threshold must be greater than 0.")
		return
	}
	// Defer since fetching + re-hosting may take a moment
	if err := e.DeferCreateMessage(true); err != nil {
		b.Log.Error("Failed to defer sticky config response", "error", err)
		return
	}

	// Fetch the original message
	msg, err := b.Client.Rest.GetMessage(channelID, messageID)
	if err != nil {
		b.Log.Error("Failed to fetch target message", "error", err)
		b.followup(e, "Failed to fetch the target message.")
		return
	}

	// Re-host attachments to S3
	ctx := context.Background()
	guildStr := fmt.Sprintf("%d", guildID)
	var fileURLs []string
	for _, att := range msg.Attachments {
		s3URL, err := b.S3.SaveStickyFile(ctx, guildStr, att.ProxyURL)
		if err != nil {
			b.Log.Error("Failed to re-host attachment", "error", err, "url", att.ProxyURL)
			continue
		}
		fileURLs = append(fileURLs, s3URL)
	}

	// Build embeds: copy original embeds, then add file URLs as image embeds
	embeds := make([]discord.Embed, len(msg.Embeds))
	copy(embeds, msg.Embeds)
	for _, u := range fileURLs {
		embeds = append(embeds, discord.Embed{
			Image: &discord.EmbedResource{URL: u},
		})
	}

	s := &stickyMessage{
		ChannelID:         channelID,
		GuildID:           guildID,
		Content:           msg.Content,
		Embeds:            embeds,
		FileURLs:          fileURLs,
		CreatedBy:         e.User().ID,
		Active:            true,
		MinIdleMins:       minIdle,
		MaxIdleMins:       maxIdle,
		MsgThreshold:      threshold,
		TimeThresholdMins: timeThreshold,
	}

	// Post the sticky immediately
	sent, err := b.Client.Rest.CreateMessage(channelID, discord.MessageCreate{
		Content: s.Content,
		Embeds:  s.Embeds,
	})
	if err != nil {
		b.Log.Error("Failed to post initial sticky", "error", err)
		b.followup(e, "Failed to post the sticky message.")
		return
	}

	s.LastMessageID = sent.ID

	// Replace any existing sticky in this channel
	b.mu.Lock()
	if b.stickies[guildID] == nil {
		b.stickies[guildID] = make(map[snowflake.ID]*stickyMessage)
	}
	old := b.stickies[guildID][channelID]
	b.stickies[guildID][channelID] = s
	b.mu.Unlock()

	if old != nil {
		b.stopStickyGoroutine(old)
		if old.LastMessageID != 0 {
			_ = b.Client.Rest.DeleteMessage(channelID, old.LastMessageID)
		}
	}
	b.startStickyGoroutine(s)

	// Save to S3 immediately
	b.saveStickiesForGuild(guildID)

	b.Log.Info("Sticky created", "user_id", e.User().ID, "guild_id", guildID, "channel_id", channelID, "min_idle", minIdle, "max_idle", maxIdle, "msg_threshold", threshold, "time_threshold", timeThreshold)

	b.followup(e, fmt.Sprintf("Sticky created in <#%d>! Idle: %d–%d min, msg threshold: %d, time threshold: %d min.", channelID, minIdle, maxIdle, threshold, timeThreshold))
}

func (b *Bot) handleStickyStop(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	b.Log.Info("Sticky stop", "user_id", e.User().ID, "guild_id", guildID, "channel_id", channelID)

	b.mu.Lock()
	s := b.getSticky(guildID, channelID)
	if s == nil {
		b.mu.Unlock()
		botutil.RespondEphemeral(e, "No sticky in this channel.")
		return
	}
	s.Active = false
	b.mu.Unlock()

	b.stopStickyGoroutine(s)
	b.saveStickiesForGuild(guildID)
	botutil.RespondEphemeral(e, "Sticky paused.")
}

func (b *Bot) handleStickyStart(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	b.Log.Info("Sticky start", "user_id", e.User().ID, "guild_id", guildID, "channel_id", channelID)

	b.mu.Lock()
	s := b.getSticky(guildID, channelID)
	if s == nil {
		b.mu.Unlock()
		botutil.RespondEphemeral(e, "No sticky in this channel.")
		return
	}
	if s.Active {
		b.mu.Unlock()
		b.Log.Info("Sticky already active", "user_id", e.User().ID, "guild_id", guildID, "channel_id", channelID)
		botutil.RespondEphemeral(e, "Sticky is already active.")
		return
	}
	s.Active = true
	b.mu.Unlock()

	b.startStickyGoroutine(s)
	b.saveStickiesForGuild(guildID)
	botutil.RespondEphemeral(e, "Sticky resumed.")
}

func (b *Bot) handleStickyRemove(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	b.Log.Info("Sticky remove", "user_id", e.User().ID, "guild_id", guildID, "channel_id", channelID)

	b.mu.Lock()
	channels, ok := b.stickies[guildID]
	if !ok {
		b.mu.Unlock()
		botutil.RespondEphemeral(e, "No sticky in this channel.")
		return
	}
	s, ok := channels[channelID]
	if !ok {
		b.mu.Unlock()
		botutil.RespondEphemeral(e, "No sticky in this channel.")
		return
	}
	delete(channels, channelID)
	b.mu.Unlock()

	b.stopStickyGoroutine(s)
	if s.LastMessageID != 0 {
		_ = b.Client.Rest.DeleteMessage(channelID, s.LastMessageID)
	}
	b.saveStickiesForGuild(guildID)
	botutil.RespondEphemeral(e, "Sticky removed.")
}

func (b *Bot) handleStickyList(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()

	b.Log.Info("Sticky list", "user_id", e.User().ID, "guild_id", guildID)

	b.mu.Lock()
	channels, ok := b.stickies[guildID]
	if !ok || len(channels) == 0 {
		b.mu.Unlock()
		botutil.RespondEphemeral(e, "No stickies in this server.")
		return
	}
	var lines []string
	for _, s := range channels {
		status := "active"
		if !s.Active {
			status = "paused"
		}
		previewStr := truncatePreview(s.Content, 50)
		lines = append(lines, fmt.Sprintf("<#%d> — %s — %q", s.ChannelID, status, previewStr))
	}
	b.mu.Unlock()

	botutil.RespondEphemeral(e, strings.Join(lines, "\n"))
}

func (b *Bot) getSticky(guildID, channelID snowflake.ID) *stickyMessage {
	channels, ok := b.stickies[guildID]
	if !ok {
		return nil
	}
	return channels[channelID]
}

func (b *Bot) followup(e *events.ModalSubmitInteractionCreate, content string) {
	b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
		Content: content,
		Flags:   discord.MessageFlagEphemeral,
	})
}

// truncatePreview truncates a string to maxRunes without splitting Unicode
// characters or Discord markup tokens like <:name:id>, <a:name:id>, <@id>, <#id>.
func truncatePreview(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	runes = runes[:maxRunes]
	// If we cut inside a <...> token, back up to before the '<'
	preview := string(runes)
	lastOpen := strings.LastIndex(preview, "<")
	if lastOpen != -1 && !strings.Contains(preview[lastOpen:], ">") {
		preview = preview[:lastOpen]
	}
	return preview + "..."
}
