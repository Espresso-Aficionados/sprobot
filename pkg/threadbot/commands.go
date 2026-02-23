package threadbot

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

const (
	cmdSlash       = "threadbot"
	cmdListThreads = "threads"
	subEnable      = "enable"
	subDisable     = "disable"
	subList        = "list"

	optMinIdle       = "min_idle"
	optMaxIdle       = "max_idle"
	optMsgThreshold  = "msg_threshold"
	optTimeThreshold = "time_threshold"
)

func intPtr(v int) *int { return &v }

func isThread(ct discord.ChannelType) bool {
	return ct == discord.ChannelTypeGuildPublicThread ||
		ct == discord.ChannelTypeGuildPrivateThread ||
		ct == discord.ChannelTypeGuildNewsThread
}

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
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionInt{
							Name:        optMinIdle,
							Description: "Quiet minutes before repost (default 30)",
							MinValue:    intPtr(0),
						},
						discord.ApplicationCommandOptionInt{
							Name:        optMaxIdle,
							Description: "Force repost after minutes (default 720)",
							MinValue:    intPtr(1),
						},
						discord.ApplicationCommandOptionInt{
							Name:        optMsgThreshold,
							Description: "Messages to arm idle watch (default 500)",
							MinValue:    intPtr(0),
						},
						discord.ApplicationCommandOptionInt{
							Name:        optTimeThreshold,
							Description: "Minutes to arm idle watch (default 720)",
							MinValue:    intPtr(0),
						},
					},
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
		discord.SlashCommandCreate{
			Name:        cmdListThreads,
			Description: "Show active threads in the current channel",
		},
	}

	return botutil.RegisterGuildCommands(b.Client, b.Env, commands, b.Log)
}

func (b *Bot) onCommand(e *events.ApplicationCommandInteractionCreate) {
	if e.GuildID() == nil {
		return
	}

	d, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	switch d.CommandName() {
	case cmdListThreads:
		if isThread(e.Channel().Type()) {
			botutil.RespondEphemeral(e, "This command only works in channels — it lists the threads under a channel.")
			return
		}
		b.handleThreads(e)
	case cmdSlash:
		if isThread(e.Channel().Type()) {
			botutil.RespondEphemeral(e, "This command can only be used in channels, not threads.")
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
}

func (b *Bot) handleEnable(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()
	data := e.Data.(discord.SlashCommandInteractionData)

	minIdle := 30
	if v, ok := data.OptInt(optMinIdle); ok {
		minIdle = v
	}
	maxIdle := 720
	if v, ok := data.OptInt(optMaxIdle); ok {
		maxIdle = v
	}
	msgThreshold := 500
	if v, ok := data.OptInt(optMsgThreshold); ok {
		msgThreshold = v
	}
	timeThreshold := 720
	if v, ok := data.OptInt(optTimeThreshold); ok {
		timeThreshold = v
	}

	if maxIdle <= minIdle {
		botutil.RespondEphemeral(e, fmt.Sprintf("max_idle (%d) must be greater than min_idle (%d).", maxIdle, minIdle))
		return
	}
	if msgThreshold == 0 && timeThreshold == 0 {
		botutil.RespondEphemeral(e, "At least one of msg_threshold or time_threshold must be > 0.")
		return
	}
	r := &threadReminder{
		ChannelID:         channelID,
		GuildID:           guildID,
		EnabledBy:         e.User().ID,
		Enabled:           true,
		MinIdleMins:       minIdle,
		MaxIdleMins:       maxIdle,
		MsgThreshold:      msgThreshold,
		TimeThresholdMins: timeThreshold,
	}

	// Replace any existing reminder in this channel
	b.mu.Lock()
	if b.reminders[guildID] == nil {
		b.reminders[guildID] = make(map[snowflake.ID]*threadReminder)
	}
	old := b.reminders[guildID][channelID]
	b.reminders[guildID][channelID] = r
	b.mu.Unlock()

	if old != nil {
		b.stopReminderGoroutine(old)
	}
	b.startReminderGoroutine(r)
	b.saveRemindersForGuild(guildID)

	botutil.RespondEphemeral(e, fmt.Sprintf("Thread reminders enabled in <#%d>. Idle: %d–%d min, msg threshold: %d, time threshold: %d min.", channelID, r.MinIdleMins, r.MaxIdleMins, r.MsgThreshold, r.TimeThresholdMins))
}

func (b *Bot) handleDisable(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	b.mu.Lock()
	channels, ok := b.reminders[guildID]
	if !ok {
		b.mu.Unlock()
		botutil.RespondEphemeral(e, "No thread reminder in this channel.")
		return
	}
	r, ok := channels[channelID]
	if !ok {
		b.mu.Unlock()
		botutil.RespondEphemeral(e, "No thread reminder in this channel.")
		return
	}
	delete(channels, channelID)
	b.mu.Unlock()

	b.stopReminderGoroutine(r)
	if r.LastMessageID != 0 {
		_ = b.Client.Rest.DeleteMessage(channelID, r.LastMessageID)
	}
	b.saveRemindersForGuild(guildID)
	botutil.RespondEphemeral(e, "Thread reminders disabled.")
}

func (b *Bot) handleThreads(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()

	embed := b.buildThreadEmbed(guildID, channelID)
	if embed == nil {
		botutil.RespondEphemeral(e, "No active threads in this channel.")
		return
	}

	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{*embed},
		Flags:  discord.MessageFlagEphemeral,
	})
}

func (b *Bot) handleList(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()

	b.mu.Lock()
	channels, ok := b.reminders[guildID]
	if !ok || len(channels) == 0 {
		b.mu.Unlock()
		botutil.RespondEphemeral(e, "No thread reminders in this server.")
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
	b.mu.Unlock()

	botutil.RespondEphemeral(e, strings.Join(lines, "\n"))
}
