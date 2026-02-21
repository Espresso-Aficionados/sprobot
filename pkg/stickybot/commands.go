package stickybot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
)

const (
	cmdContextMenu = "Sticky this message"
	cmdSlash       = "sticky"
	subStop        = "stop"
	subStart       = "start"
	subRemove      = "remove"
	subList        = "list"
	modalPrefix    = "sticky_config_"
	fieldMinDwell  = "min_dwell_mins"
	fieldMaxDwell  = "max_dwell_mins"
	fieldThreshold = "msg_threshold"
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

	for _, guildID := range getGuildIDs(b.env) {
		if _, err := b.client.Rest.SetGuildCommands(b.client.ApplicationID, guildID, commands); err != nil {
			return fmt.Errorf("registering guild commands for %d: %w", guildID, err)
		}
		b.log.Info("Registered guild commands", "guild_id", guildID, "count", len(commands))
	}
	return nil
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
		respondEphemeral(e, "Something went wrong.")
		return
	}
	msg := data.TargetMessage()

	// Encode channel+message IDs into the modal custom ID
	err := e.Modal(discord.ModalCreate{
		CustomID: fmt.Sprintf("%s%d_%d", modalPrefix, msg.ChannelID, msg.ID),
		Title:    "Sticky Message Settings",
		Components: []discord.LayoutComponent{
			discord.NewLabel(
				"Min dwell time (minutes before repost)",
				discord.TextInputComponent{
					CustomID: fieldMinDwell,
					Style:    discord.TextInputStyleShort,
					Value:    "15",
					Required: true,
				},
			),
			discord.NewLabel(
				"Max dwell time (force repost after mins)",
				discord.TextInputComponent{
					CustomID: fieldMaxDwell,
					Style:    discord.TextInputStyleShort,
					Value:    "30",
					Required: true,
				},
			),
			discord.NewLabel(
				"Message threshold (msgs before repost)",
				discord.TextInputComponent{
					CustomID: fieldThreshold,
					Style:    discord.TextInputStyleShort,
					Value:    "4",
					Required: true,
				},
			),
		},
	})
	if err != nil {
		b.log.Error("Failed to show sticky config modal", "error", err)
	}
}

func (b *Bot) handleStickyConfigModal(e *events.ModalSubmitInteractionCreate) {
	guildID := *e.GuildID()

	parts := strings.SplitN(strings.TrimPrefix(e.Data.CustomID, modalPrefix), "_", 2)
	if len(parts) != 2 {
		respondEphemeral(e, "Something went wrong.")
		return
	}
	channelID, _ := snowflake.Parse(parts[0])
	messageID, _ := snowflake.Parse(parts[1])

	minDwellStr := e.Data.Text(fieldMinDwell)
	maxDwellStr := e.Data.Text(fieldMaxDwell)
	threshStr := e.Data.Text(fieldThreshold)

	minDwell, err := strconv.Atoi(minDwellStr)
	if err != nil || minDwell < 0 {
		respondEphemeral(e, "Min dwell must be a non-negative number.")
		return
	}
	maxDwell, err := strconv.Atoi(maxDwellStr)
	if err != nil || maxDwell < 1 {
		respondEphemeral(e, "Max dwell must be a positive number.")
		return
	}
	if maxDwell <= minDwell {
		respondEphemeral(e, "Max dwell must be greater than min dwell.")
		return
	}
	threshold, err := strconv.Atoi(threshStr)
	if err != nil || threshold < 1 {
		respondEphemeral(e, "Threshold must be a positive number.")
		return
	}

	// Defer since fetching + re-hosting may take a moment
	e.DeferCreateMessage(true)

	// Fetch the original message
	msg, err := b.client.Rest.GetMessage(channelID, messageID)
	if err != nil {
		b.log.Error("Failed to fetch target message", "error", err)
		b.followup(e, "Failed to fetch the target message.")
		return
	}

	// Re-host attachments to S3
	ctx := context.Background()
	guildStr := fmt.Sprintf("%d", guildID)
	var fileURLs []string
	for _, att := range msg.Attachments {
		s3URL, err := b.s3.SaveStickyFile(ctx, guildStr, att.ProxyURL)
		if err != nil {
			b.log.Error("Failed to re-host attachment", "error", err, "url", att.ProxyURL)
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
		ChannelID:    channelID,
		GuildID:      guildID,
		Content:      msg.Content,
		Embeds:       embeds,
		FileURLs:     fileURLs,
		CreatedBy:    e.User().ID,
		Active:       true,
		MinDwellMins: minDwell,
		MaxDwellMins: maxDwell,
		MsgThreshold: threshold,
	}

	// Post the sticky immediately
	sent, err := b.client.Rest.CreateMessage(channelID, discord.MessageCreate{
		Content: s.Content,
		Embeds:  s.Embeds,
	})
	if err != nil {
		b.log.Error("Failed to post initial sticky", "error", err)
		b.followup(e, "Failed to post the sticky message.")
		return
	}

	s.LastMessageID = sent.ID

	// Replace any existing sticky in this channel
	if b.stickies[guildID] == nil {
		b.stickies[guildID] = make(map[snowflake.ID]*stickyMessage)
	}
	if old, ok := b.stickies[guildID][channelID]; ok {
		b.stopStickyGoroutine(old)
		if old.LastMessageID != 0 {
			_ = b.client.Rest.DeleteMessage(channelID, old.LastMessageID)
		}
	}
	b.stickies[guildID][channelID] = s
	b.startStickyGoroutine(s)

	// Save to S3 immediately
	b.saveStickiesForGuild(guildID)

	b.followup(e, fmt.Sprintf("Sticky created in <#%d>! Dwell: %d–%d min, threshold: %d messages.", channelID, minDwell, maxDwell, threshold))
}

func (b *Bot) handleStickyStop(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	s := b.getSticky(guildID, channelID)
	if s == nil {
		respondEphemeral(e, "No sticky in this channel.")
		return
	}

	b.stopStickyGoroutine(s)
	s.Active = false

	b.saveStickiesForGuild(guildID)
	respondEphemeral(e, "Sticky paused.")
}

func (b *Bot) handleStickyStart(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	s := b.getSticky(guildID, channelID)
	if s == nil {
		respondEphemeral(e, "No sticky in this channel.")
		return
	}
	if s.Active {
		respondEphemeral(e, "Sticky is already active.")
		return
	}

	s.Active = true
	b.startStickyGoroutine(s)

	b.saveStickiesForGuild(guildID)
	respondEphemeral(e, "Sticky resumed.")
}

func (b *Bot) handleStickyRemove(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	channels, ok := b.stickies[guildID]
	if !ok {
		respondEphemeral(e, "No sticky in this channel.")
		return
	}

	s, ok := channels[channelID]
	if !ok {
		respondEphemeral(e, "No sticky in this channel.")
		return
	}

	b.stopStickyGoroutine(s)

	// Delete the current sticky message (best-effort)
	if s.LastMessageID != 0 {
		_ = b.client.Rest.DeleteMessage(channelID, s.LastMessageID)
	}

	delete(channels, channelID)
	b.saveStickiesForGuild(guildID)
	respondEphemeral(e, "Sticky removed.")
}

func (b *Bot) handleStickyList(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()

	channels, ok := b.stickies[guildID]
	if !ok || len(channels) == 0 {
		respondEphemeral(e, "No stickies in this server.")
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

	respondEphemeral(e, strings.Join(lines, "\n"))
}

func (b *Bot) getSticky(guildID, channelID snowflake.ID) *stickyMessage {
	channels, ok := b.stickies[guildID]
	if !ok {
		return nil
	}
	return channels[channelID]
}

type messageResponder interface {
	CreateMessage(discord.MessageCreate, ...rest.RequestOpt) error
}

func respondEphemeral(e messageResponder, content string) {
	e.CreateMessage(discord.MessageCreate{
		Content: content,
		Flags:   discord.MessageFlagEphemeral,
	})
}

func (b *Bot) followup(e *events.ModalSubmitInteractionCreate, content string) {
	b.client.Rest.CreateFollowupMessage(b.client.ApplicationID, e.Token(), discord.MessageCreate{
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
