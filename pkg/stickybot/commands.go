package stickybot

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

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
	fieldBuffer        = "buffer"
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

	return botutil.RegisterGuildCommands(b.Client, b.GuildIDs(), commands, b.Log)
}

func (b *Bot) onCommand(e *events.ApplicationCommandInteractionCreate) {
	if e.GuildID() == nil || b.RejectGuildNotAllowed(e) {
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
			if sub == nil || b.rejectUnloadedGuild(e) {
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

const notLoadedReply = "This server's stickies couldn't be loaded from storage. Try again in a minute."

// rejectUnloadedGuild turns away a command in a guild whose stickies failed
// to load and still don't after a retry, and reports whether it did.
func (b *Bot) rejectUnloadedGuild(e *events.ApplicationCommandInteractionCreate) bool {
	// Leave time to answer within the interaction's 3 seconds.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if b.guildLoaded(ctx, *e.GuildID()) {
		return false
	}
	botutil.RespondEphemeral(e, notLoadedReply)
	return true
}

func (b *Bot) onModal(e *events.ModalSubmitInteractionCreate) {
	if e.GuildID() == nil || b.RejectGuildNotAllowed(e) {
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
	b.pending.Put(msg)

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
			discord.NewLabel(
				"Buffer (skip repost if in last N msgs)",
				discord.TextInputComponent{
					CustomID: fieldBuffer,
					Style:    discord.TextInputStyleShort,
					Value:    "5",
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
	bufferStr := e.Data.Text(fieldBuffer)

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
	buffer, err := strconv.Atoi(bufferStr)
	if err != nil || buffer < 0 {
		botutil.RespondEphemeral(e, "Buffer must be a non-negative number.")
		return
	}
	if errMsg := validateStickyParams(minIdle, maxIdle, threshold, timeThreshold, buffer); errMsg != "" {
		botutil.RespondEphemeral(e, errMsg)
		return
	}
	// Defer since fetching + re-hosting may take a moment
	if err := e.DeferCreateMessage(true); err != nil {
		b.Log.Error("Failed to defer sticky config response", "error", err)
		return
	}

	// Use the message captured at context-menu time (resolved interaction data
	// includes content without the message content intent; it is a snapshot —
	// edits made while the modal is open are not picked up). Fall back to REST
	// for modals submitted across a bot restart.
	msg, ok := b.pending.Get(messageID)
	if !ok {
		fetched, err := b.Client.Rest.GetMessage(channelID, messageID)
		if err != nil {
			b.Log.Error("Failed to fetch target message", "error", err)
			botutil.FollowupEphemeral(b.Client, e.Token(), "Failed to fetch the target message.")
			return
		}
		msg = *fetched
	}
	if msg.Content == "" && len(msg.Embeds) == 0 && len(msg.Attachments) == 0 {
		if ok {
			botutil.FollowupEphemeral(b.Client, e.Token(), "This message has no text, embeds, or attachments to sticky.")
		} else {
			// REST strips content fields without the message content intent;
			// the context-menu resolved data is exempt.
			botutil.FollowupEphemeral(b.Client, e.Token(), "Couldn't read the target message — please run \"Sticky this message\" again.")
		}
		return
	}

	s := &stickyMessage{
		ChannelID:         channelID,
		GuildID:           guildID,
		CreatedBy:         e.User().ID,
		Active:            true,
		MinIdleMins:       minIdle,
		MaxIdleMins:       maxIdle,
		MsgThreshold:      threshold,
		TimeThresholdMins: timeThreshold,
		Buffer:            buffer,
	}
	botutil.FollowupEphemeral(b.Client, e.Token(), b.createSticky(s, msg))
}

// createSticky fills in s from msg, re-hosting msg's attachments to S3, and
// posts it as its channel's sticky. It returns the reply for the moderator.
func (b *Bot) createSticky(s *stickyMessage, msg discord.Message) string {
	ctx, cancel := context.WithTimeout(context.Background(), botutil.DeferredWorkBudget)
	defer cancel()
	if !b.guildLoaded(ctx, s.GuildID) {
		return notLoadedReply
	}

	// Saved files follow the message's own embeds, as image embeds.
	s.Content = msg.Content
	s.Embeds = slices.Clone(msg.Embeds)
	var failed []string
	for _, att := range msg.Attachments {
		key, err := b.S3.SaveStickyFile(ctx, s.GuildID.String(), att.ProxyURL)
		if err != nil {
			b.Log.Error("Failed to re-host attachment", "error", err, "url", att.ProxyURL)
			failed = append(failed, att.Filename)
			continue
		}
		s.FileURLs = append(s.FileURLs, key)
		s.Embeds = append(s.Embeds, discord.Embed{Image: &discord.EmbedResource{URL: key}})
	}
	if s.Content == "" && len(s.Embeds) == 0 {
		return "Couldn't save the message's attachments, so there is nothing to sticky."
	}

	if err := b.installSticky(s); err != nil {
		b.Log.Error("Failed to post initial sticky", "error", err)
		return "Failed to post the sticky message."
	}
	b.Log.Info("Sticky created", "user_id", s.CreatedBy, "guild_id", s.GuildID, "channel_id", s.ChannelID, "min_idle", s.MinIdleMins, "max_idle", s.MaxIdleMins, "msg_threshold", s.MsgThreshold, "time_threshold", s.TimeThresholdMins, "buffer", s.Buffer)
	return createdReply(s, failed)
}

// createdReply confirms a new sticky to the moderator, naming the attachments
// that couldn't be saved and so are left out of it.
func createdReply(s *stickyMessage, failed []string) string {
	reply := fmt.Sprintf("Sticky created in <#%d>! Idle: %d–%d min, msg threshold: %d, time threshold: %d min.", s.ChannelID, s.MinIdleMins, s.MaxIdleMins, s.MsgThreshold, s.TimeThresholdMins)
	if len(failed) > 0 {
		// Code spans, so underscores in file names don't turn into italics.
		reply += "\nCouldn't save these attachments, so the sticky leaves them out: `" + strings.Join(failed, "`, `") + "`"
	}
	return reply
}

// installSticky posts s and makes it its channel's sticky, retiring any
// sticky already there. If the post fails, s's saved files are deleted, as
// nothing will refer to them. s's guild must be loaded (see guildLoaded).
func (b *Bot) installSticky(s *stickyMessage) error {
	sent, err := b.Client.Rest.CreateMessage(s.ChannelID, s.messageCreate(b.S3.FileURL))
	if err != nil {
		b.deleteStickyFiles(s)
		return err
	}
	s.LastMessageID = sent.ID

	b.mu.Lock()
	old := b.stickies[s.GuildID][s.ChannelID]
	b.stickies[s.GuildID][s.ChannelID] = s
	b.startStickyGoroutine(s)
	b.mu.Unlock()

	if old != nil {
		b.retireSticky(old)
	}
	b.saveStickiesForGuild(s.GuildID)
	return nil
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
	h := s.handle // this loop, not one a /sticky start begins meanwhile
	b.mu.Unlock()

	// Respond first: stopping waits for any repost in flight, which can
	// outlast the interaction's 3-second deadline.
	botutil.RespondEphemeral(e, "Sticky paused.")
	h.Stop()
	b.saveStickiesForGuild(guildID)
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
	b.startStickyGoroutine(s)
	b.mu.Unlock()

	b.saveStickiesForGuild(guildID)
	botutil.RespondEphemeral(e, "Sticky resumed.")
}

func (b *Bot) handleStickyRemove(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	b.Log.Info("Sticky remove", "user_id", e.User().ID, "guild_id", guildID, "channel_id", channelID)

	s := b.takeSticky(guildID, channelID)
	if s == nil {
		botutil.RespondEphemeral(e, "No sticky in this channel.")
		return
	}
	// Respond first: stopping waits for any repost in flight, which can
	// outlast the interaction's 3-second deadline.
	botutil.RespondEphemeral(e, "Sticky removed.")
	b.retireSticky(s)
	b.saveStickiesForGuild(guildID)
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

// getSticky returns channelID's sticky, or nil. The caller holds b.mu.
func (b *Bot) getSticky(guildID, channelID snowflake.ID) *stickyMessage {
	return b.stickies[guildID][channelID]
}

// takeSticky removes channelID's sticky from b.stickies and returns it, or nil
// if the channel has none.
func (b *Bot) takeSticky(guildID, channelID snowflake.ID) *stickyMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.getSticky(guildID, channelID)
	if s != nil {
		delete(b.stickies[guildID], channelID)
	}
	return s
}

// validateStickyParams checks cross-field constraints for sticky message settings.
// Returns an error message if invalid, or empty string if OK.
func validateStickyParams(minIdle, maxIdle, threshold, timeThreshold, buffer int) string {
	if maxIdle <= minIdle {
		return fmt.Sprintf("Max idle (%d) must be greater than min idle (%d).", maxIdle, minIdle)
	}
	if threshold > 0 && threshold <= buffer {
		return fmt.Sprintf("Message threshold (%d) must be greater than buffer (%d).", threshold, buffer)
	}
	if threshold == 0 && timeThreshold == 0 {
		return "At least one of message threshold or time threshold must be greater than 0."
	}
	return ""
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
