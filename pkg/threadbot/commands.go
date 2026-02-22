package threadbot

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
)

const (
	cmdSlash   = "threadbot"
	subEnable  = "enable"
	subDisable = "disable"
	subList    = "list"
)

func (b *Bot) registerAllCommands() error {
	perm := discord.PermissionManageMessages

	commands := []discord.ApplicationCommandCreate{
		discord.SlashCommandCreate{
			Name:                     cmdSlash,
			Description:              "Manage active thread reminders",
			DefaultMemberPermissions: omit.NewPtr(perm),
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionSubCommand{
					Name:        subEnable,
					Description: "Enable thread reminders in the current channel",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        subDisable,
					Description: "Disable thread reminders in the current channel",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        subList,
					Description: "List all channels with thread reminders in this server",
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

	d, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}
	if d.CommandName() != cmdSlash {
		return
	}

	sub := d.SubCommandName
	if sub == nil {
		return
	}
	switch *sub {
	case subEnable:
		b.handleEnable(e)
	case subDisable:
		b.handleDisable(e)
	case subList:
		b.handleList(e)
	}
}

func (b *Bot) handleEnable(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	// Replace any existing reminder in this channel
	if b.reminders[guildID] == nil {
		b.reminders[guildID] = make(map[snowflake.ID]*threadReminder)
	}
	if old, ok := b.reminders[guildID][channelID]; ok {
		b.stopReminderGoroutine(old)
	}

	r := &threadReminder{
		ChannelID:         channelID,
		GuildID:           guildID,
		EnabledBy:         e.User().ID,
		Enabled:           true,
		MinIdleMins:       30,
		MaxIdleMins:       720,
		MsgThreshold:      30,
		TimeThresholdMins: 60,
	}

	b.reminders[guildID][channelID] = r
	b.startReminderGoroutine(r)
	b.saveRemindersForGuild(guildID)

	respondEphemeral(e, fmt.Sprintf("Thread reminders enabled in <#%d>. Idle: %d–%d min, msg threshold: %d, time threshold: %d min.", channelID, r.MinIdleMins, r.MaxIdleMins, r.MsgThreshold, r.TimeThresholdMins))
}

func (b *Bot) handleDisable(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	channels, ok := b.reminders[guildID]
	if !ok {
		respondEphemeral(e, "No thread reminder in this channel.")
		return
	}

	r, ok := channels[channelID]
	if !ok {
		respondEphemeral(e, "No thread reminder in this channel.")
		return
	}

	b.stopReminderGoroutine(r)

	// Delete the last reminder message (best-effort)
	if r.LastMessageID != 0 {
		_ = b.client.Rest.DeleteMessage(channelID, r.LastMessageID)
	}

	delete(channels, channelID)
	b.saveRemindersForGuild(guildID)
	respondEphemeral(e, "Thread reminders disabled.")
}

func (b *Bot) handleList(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()

	channels, ok := b.reminders[guildID]
	if !ok || len(channels) == 0 {
		respondEphemeral(e, "No thread reminders in this server.")
		return
	}

	var lines []string
	for _, r := range channels {
		status := "enabled"
		if !r.Enabled {
			status = "disabled"
		}
		lines = append(lines, fmt.Sprintf("<#%d> — %s", r.ChannelID, status))
	}

	respondEphemeral(e, strings.Join(lines, "\n"))
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
