package threadbot

import (
	"fmt"
	"slices"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

const (
	cmdSlash         = "threadbot"
	cmdListThreads   = "threads"
	subEnable        = "enable"
	subDisable       = "disable"
	subList          = "list"
	subRefreshCounts = "refreshcounts"
	subFetchAll      = "fetchall"
	subHide          = "hide"
	subUnhide        = "unhide"
	subHidden        = "hidden"

	optMinIdle       = "min_idle"
	optMaxIdle       = "max_idle"
	optMsgThreshold  = "msg_threshold"
	optTimeThreshold = "time_threshold"
	optPublic        = "public"
	optBuffer        = "buffer"
	optThread        = "thread"
)

var threadChannelTypes = []discord.ChannelType{
	discord.ChannelTypeGuildPublicThread,
	discord.ChannelTypeGuildPrivateThread,
	discord.ChannelTypeGuildNewsThread,
}

// maxBuffer is the most messages Get Channel Messages returns, so the most a
// reminder's buffer can check.
const maxBuffer = 100

func intPtr(v int) *int { return &v }

func isThread(ct discord.ChannelType) bool {
	return slices.Contains(threadChannelTypes, ct)
}

// validateReminderParams checks cross-field constraints for thread reminder settings.
// Returns an error message if invalid, or empty string if OK.
func validateReminderParams(minIdle, maxIdle, msgThreshold, timeThreshold, buffer int) string {
	if buffer > maxBuffer {
		return fmt.Sprintf("buffer (%d) can be at most %d.", buffer, maxBuffer)
	}
	if maxIdle <= minIdle {
		return fmt.Sprintf("max_idle (%d) must be greater than min_idle (%d).", maxIdle, minIdle)
	}
	if msgThreshold > 0 && msgThreshold <= buffer {
		return fmt.Sprintf("msg_threshold (%d) must be greater than buffer (%d).", msgThreshold, buffer)
	}
	if msgThreshold == 0 && timeThreshold == 0 {
		return "At least one of msg_threshold or time_threshold must be > 0."
	}
	return ""
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
						discord.ApplicationCommandOptionInt{
							Name:        optBuffer,
							Description: "Skip repost if in last N messages (default 5)",
							MinValue:    intPtr(0),
							MaxValue:    intPtr(maxBuffer),
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
				discord.ApplicationCommandOptionSubCommand{
					Name:        subRefreshCounts,
					Description: "Refresh cached thread member counts for this server",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        subFetchAll,
					Description: "Show all active threads across the entire server",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionBool{
							Name:        optPublic,
							Description: "Post the result publicly in the channel (default: hidden)",
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        subHide,
					Description: "Never promote a thread in reminders or /threads",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionChannel{
							Name:         optThread,
							Description:  "Thread to hide",
							Required:     true,
							ChannelTypes: threadChannelTypes,
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        subUnhide,
					Description: "Allow a hidden thread to be promoted again",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionChannel{
							Name:         optThread,
							Description:  "Thread to unhide",
							Required:     true,
							ChannelTypes: threadChannelTypes,
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        subHidden,
					Description: "List threads hidden from reminders and /threads",
				},
			},
		},
		discord.SlashCommandCreate{
			Name:        cmdListThreads,
			Description: "Show active threads in the current channel",
		},
	}

	return botutil.RegisterGuildCommands(b.Client, b.GuildIDs(), commands, b.Log)
}

func (b *Bot) onCommand(e *events.ApplicationCommandInteractionCreate) {
	if e.GuildID() == nil || b.RejectGuildNotAllowed(e) {
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
		case subRefreshCounts:
			b.handleRefreshCounts(e)
		case subFetchAll:
			b.handleFetchAll(e)
		case subHide:
			b.handleHide(e, true)
		case subUnhide:
			b.handleHide(e, false)
		case subHidden:
			b.handleHiddenList(e)
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
	buffer := 5
	if v, ok := data.OptInt(optBuffer); ok {
		buffer = v
	}

	if errMsg := validateReminderParams(minIdle, maxIdle, msgThreshold, timeThreshold, buffer); errMsg != "" {
		b.Log.Info("Enable rejected", "user_id", e.User().ID, "guild_id", guildID, "channel_id", channelID, "reason", errMsg)
		botutil.RespondEphemeral(e, errMsg)
		return
	}
	reply, ok := b.remindersLoaded(e, guildID)
	if !ok {
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
		Buffer:            buffer,
	}

	// Replace any existing reminder in this channel
	b.mu.Lock()
	old := b.reminders[guildID][channelID]
	b.reminders[guildID][channelID] = r
	b.mu.Unlock()

	// Respond first: stopping the old reminder waits for any repost in
	// flight, which can outlast the interaction's 3-second deadline.
	reply(fmt.Sprintf("Thread reminders enabled in <#%d>. Idle: %d–%d min, msg threshold: %d, time threshold: %d min, buffer: %d msgs.", channelID, r.MinIdleMins, r.MaxIdleMins, r.MsgThreshold, r.TimeThresholdMins, r.Buffer))
	if old != nil {
		b.stopReminderGoroutine(old)
	}
	b.mu.Lock()
	// Unless another command replaced or disabled r meanwhile, r takes over
	// the old reminder's message, so its first repost edits or replaces it.
	// Read only after the stop: a repost in flight replaces the message.
	current := b.reminders[guildID][channelID] == r
	if current && old != nil {
		r.LastMessageID, r.LastPostTime = old.LastMessageID, old.LastPostTime
	}
	b.startReminderGoroutine(r)
	b.mu.Unlock()
	if !current && old != nil && old.LastMessageID != 0 {
		_ = b.Client.Rest.DeleteMessage(channelID, old.LastMessageID)
	}
	b.saveRemindersForGuild(guildID)

	b.Log.Info("Reminder enabled", "user_id", e.User().ID, "guild_id", guildID, "channel_id", channelID, "min_idle", minIdle, "max_idle", maxIdle, "msg_threshold", msgThreshold, "time_threshold", timeThreshold, "buffer", buffer)
}

// remindersLoaded makes sure the guild's stored reminders have loaded before
// a command reads or changes them, and returns how to answer the command. If
// they haven't, loading them takes an S3 GET whose retries can outlast the
// interaction's 3-second deadline, so the command is deferred and answered
// with a follow-up. If they can't be loaded it tells the user and returns
// false: a change saved now would overwrite them.
func (b *Bot) remindersLoaded(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID) (func(string), bool) {
	b.mu.Lock()
	_, loaded := b.reminders[guildID]
	b.mu.Unlock()
	if loaded {
		return func(content string) { botutil.RespondEphemeral(e, content) }, true
	}

	if err := e.DeferCreateMessage(true); err != nil {
		b.Log.Error("Failed to defer reminder command", "error", err)
		return nil, false
	}
	reply := func(content string) { botutil.FollowupEphemeral(b.Client, e.Token(), content) }
	if err := b.ensureRemindersLoaded(guildID); err != nil {
		b.Log.Error("Failed to load thread reminders", "guild_id", guildID, "error", err)
		reply("Couldn't load this server's thread reminders, so they can't be shown or changed right now. Please try again in a minute.")
		return nil, false
	}
	return reply, true
}

func (b *Bot) handleDisable(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	channelID := e.Channel().ID()
	reply, ok := b.remindersLoaded(e, guildID)
	if !ok {
		return
	}

	b.mu.Lock()
	r, ok := b.reminders[guildID][channelID]
	delete(b.reminders[guildID], channelID)
	b.mu.Unlock()
	if !ok {
		b.Log.Info("Disable: no reminder found", "user_id", e.User().ID, "guild_id", guildID, "channel_id", channelID)
		reply("No thread reminder in this channel.")
		return
	}

	// Respond first: stopping waits for any repost in flight, which can
	// outlast the interaction's 3-second deadline.
	reply("Thread reminders disabled.")
	b.stopReminderGoroutine(r)
	// Read only after the stop: a repost in flight replaces the message.
	if r.LastMessageID != 0 {
		_ = b.Client.Rest.DeleteMessage(channelID, r.LastMessageID)
	}
	b.saveRemindersForGuild(guildID)
	b.Log.Info("Reminder disabled", "user_id", e.User().ID, "guild_id", guildID, "channel_id", channelID)
}

func (b *Bot) handleThreads(e *events.ApplicationCommandInteractionCreate) {
	b.Log.Info("Listing threads", "user_id", e.User().ID, "guild_id", *e.GuildID(), "channel_id", e.Channel().ID())
	b.respondThreads(e, e.Channel().ID(), true)
}

func (b *Bot) handleFetchAll(e *events.ApplicationCommandInteractionCreate) {
	public, _ := e.Data.(discord.SlashCommandInteractionData).OptBool(optPublic)
	b.Log.Info("Fetching all threads", "user_id", e.User().ID, "guild_id", *e.GuildID(), "public", public)
	b.respondThreads(e, 0, !public)
}

// respondThreads answers a command with the thread list for channelID, or
// for the whole server when channelID is 0.
func (b *Bot) respondThreads(e *events.ApplicationCommandInteractionCreate, channelID snowflake.ID, ephemeral bool) {
	// Defer since fetching threads + member counts may exceed the 3s deadline.
	if err := e.DeferCreateMessage(ephemeral); err != nil {
		b.Log.Error("Failed to defer thread list response", "error", err)
		return
	}

	var msg discord.MessageCreate
	embed, err := b.buildThreadsEmbed(*e.GuildID(), channelID)
	switch {
	case err != nil:
		b.Log.Error("Failed to build thread list", "guild_id", *e.GuildID(), "channel_id", channelID, "error", err)
		msg.Content = "Couldn't fetch the active threads. Please try again in a minute."
	case embed != nil:
		msg.Embeds = []discord.Embed{*embed}
	case channelID == 0:
		msg.Content = "No active threads in this server."
	default:
		msg.Content = "No active threads in this channel."
	}
	if ephemeral {
		msg.Flags = discord.MessageFlagEphemeral
	}
	if _, err := b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), msg); err != nil {
		b.Log.Error("Failed to send thread list", "error", err)
	}
}

func (b *Bot) handleList(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	reply, ok := b.remindersLoaded(e, guildID)
	if !ok {
		return
	}

	b.mu.Lock()
	channels := b.reminders[guildID]
	if len(channels) == 0 {
		b.mu.Unlock()
		b.Log.Info("List reminders: none found", "user_id", e.User().ID, "guild_id", guildID)
		reply("No thread reminders in this server.")
		return
	}
	var lines []string
	for _, r := range channels {
		status := "enabled"
		if !r.Enabled {
			status = "disabled"
		}
		line := fmt.Sprintf("<#%d> — %s; idle %d–%d min, msg threshold %d, time threshold %d min, buffer %d",
			r.ChannelID, status, r.MinIdleMins, r.MaxIdleMins, r.MsgThreshold, r.TimeThresholdMins, r.Buffer)
		if msg := r.settingsError(); msg != "" {
			line += " — invalid: " + msg + " Re-run `/threadbot enable` there."
		}
		lines = append(lines, line)
	}
	b.mu.Unlock()

	b.Log.Info("List reminders", "user_id", e.User().ID, "guild_id", guildID, "count", len(lines))
	reply(strings.Join(lines, "\n"))
}

func (b *Bot) handleRefreshCounts(e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()

	// Replaced, not deleted, and saved: saves skip a guild with no cache, so
	// a restart would bring the old counts back. This also replaces stored
	// counts that failed to load.
	b.mu.Lock()
	b.memberCounts[guildID] = newMemberCountCache()
	b.mu.Unlock()

	b.Log.Info("Member count cache cleared", "user_id", e.User().ID, "guild_id", guildID)
	botutil.RespondEphemeral(e, "Thread member count cache cleared. Counts will refresh on the next reminder post or `/threads` command.")
	b.saveMemberCountsForGuild(guildID)
}
