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
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
)

type renameLogState struct {
	mu          sync.Mutex
	Destination snowflake.ID   `json:"destination"`
	Monitored   []snowflake.ID `json:"monitored"`
}

func (b *Bot) loadRenameLogs() {
	ctx := context.Background()
	for _, guildID := range b.GuildIDs() {
		st := &renameLogState{}

		data, err := b.S3.FetchGuildJSON(ctx, "renamelogs", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing rename log data, starting fresh", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load rename log data", "guild_id", guildID, "error", err)
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode rename log data", "guild_id", guildID, "error", err)
			}
		}

		b.renameLogs[guildID] = st
		b.Log.Info("Loaded rename log state", "guild_id", guildID, "destination", st.Destination, "monitored", len(st.Monitored))
	}
}

func (b *Bot) persistRenameLog(guildID snowflake.ID, st *renameLogState) error {
	st.mu.Lock()
	data, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return b.S3.SaveGuildJSON(ctx, "renamelogs", fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) saveRenameLogs() {
	for guildID, st := range b.renameLogs {
		if err := b.persistRenameLog(guildID, st); err != nil {
			b.Log.Error("Failed to save rename log data", "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Saved rename log state", "guild_id", guildID)
		}
	}
}

func (b *Bot) postRenameLog(guildID snowflake.ID, embed discord.Embed) {
	st := b.renameLogs[guildID]
	if st == nil {
		return
	}
	st.mu.Lock()
	dest := st.Destination
	st.mu.Unlock()
	if dest == 0 {
		return
	}
	embed.Timestamp = timePtr(time.Now())
	if _, err := b.Client.Rest.CreateMessage(dest, discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	}); err != nil {
		b.Log.Error("Failed to post rename log", "guild_id", guildID, "error", err)
	}
}

// checkChannelRename posts to the rename log when a monitored channel's name changes.
func (b *Bot) checkChannelRename(e *events.GuildChannelUpdate) {
	if e.OldChannel == nil {
		return
	}
	if e.OldChannel.Name() == e.Channel.Name() {
		return
	}

	st := b.renameLogs[e.GuildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	monitored := b.isMonitored(st, e.ChannelID)
	st.mu.Unlock()
	if !monitored {
		return
	}

	embed := discord.Embed{
		Title: "Channel Renamed",
		Color: colorYellow,
		Fields: []discord.EmbedField{
			{Name: "Channel", Value: channelMention(e.ChannelID), Inline: boolPtr(true)},
			{Name: "Old Name", Value: e.OldChannel.Name(), Inline: boolPtr(true)},
			{Name: "New Name", Value: e.Channel.Name(), Inline: boolPtr(true)},
		},
	}
	b.postRenameLog(e.GuildID, embed)
}

// checkThreadRename posts to the rename log when a monitored thread's name changes.
// A thread matches if its ID or its parent channel ID is in the monitored list.
func (b *Bot) checkThreadRename(e *events.ThreadUpdate) {
	if e.OldThread.ID() == 0 {
		return
	}
	if e.OldThread.Name() == e.Thread.Name() {
		return
	}

	st := b.renameLogs[e.GuildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	monitored := b.isMonitored(st, e.ThreadID) || b.isMonitored(st, e.ParentID)
	st.mu.Unlock()
	if !monitored {
		return
	}

	embed := discord.Embed{
		Title: "Thread Renamed",
		Color: colorYellow,
		Fields: []discord.EmbedField{
			{Name: "Thread", Value: channelMention(e.ThreadID), Inline: boolPtr(true)},
			{Name: "Old Name", Value: e.OldThread.Name(), Inline: boolPtr(true)},
			{Name: "New Name", Value: e.Thread.Name(), Inline: boolPtr(true)},
		},
	}
	b.postRenameLog(e.GuildID, embed)
}

// isMonitored checks if a channel ID is in the monitored list. Caller must hold st.mu.
func (b *Bot) isMonitored(st *renameLogState, id snowflake.ID) bool {
	for _, m := range st.Monitored {
		if m == id {
			return true
		}
	}
	return false
}

// --- Slash command handlers ---

func (b *Bot) handleRenameLog(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "set":
		b.handleRenameLogSet(e)
	case "add":
		b.handleRenameLogAdd(e)
	case "remove":
		b.handleRenameLogRemove(e)
	case "list":
		b.handleRenameLogList(e)
	case "clear":
		b.handleRenameLogClear(e)
	}
}

func (b *Bot) handleRenameLogSet(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Channel is required.")
		return
	}

	b.Log.Info("Rename log set destination", "user_id", e.User().ID, "guild_id", *guildID, "channel_id", ch.ID)

	st := b.renameLogs[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	st.Destination = ch.ID
	st.mu.Unlock()

	if err := b.persistRenameLog(*guildID, st); err != nil {
		b.Log.Error("Failed to save rename log data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save rename log config.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Rename log destination set to %s.", channelMention(ch.ID)))
}

func (b *Bot) handleRenameLogAdd(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Channel is required.")
		return
	}

	b.Log.Info("Rename log add channel", "user_id", e.User().ID, "guild_id", *guildID, "channel_id", ch.ID)

	st := b.renameLogs[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	for _, m := range st.Monitored {
		if m == ch.ID {
			st.mu.Unlock()
			botutil.RespondEphemeral(e, fmt.Sprintf("%s is already monitored.", channelMention(ch.ID)))
			return
		}
	}
	st.Monitored = append(st.Monitored, ch.ID)
	st.mu.Unlock()

	if err := b.persistRenameLog(*guildID, st); err != nil {
		b.Log.Error("Failed to save rename log data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save rename log config.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Now monitoring %s for renames.", channelMention(ch.ID)))
}

func (b *Bot) handleRenameLogRemove(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	data := e.Data.(discord.SlashCommandInteractionData)
	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Channel is required.")
		return
	}

	b.Log.Info("Rename log remove channel", "user_id", e.User().ID, "guild_id", *guildID, "channel_id", ch.ID)

	st := b.renameLogs[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	found := false
	for i, m := range st.Monitored {
		if m == ch.ID {
			st.Monitored = append(st.Monitored[:i], st.Monitored[i+1:]...)
			found = true
			break
		}
	}
	st.mu.Unlock()

	if !found {
		botutil.RespondEphemeral(e, fmt.Sprintf("%s is not monitored.", channelMention(ch.ID)))
		return
	}

	if err := b.persistRenameLog(*guildID, st); err != nil {
		b.Log.Error("Failed to save rename log data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save rename log config.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Stopped monitoring %s for renames.", channelMention(ch.ID)))
}

func (b *Bot) handleRenameLogList(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.Info("Rename log list", "user_id", e.User().ID, "guild_id", *guildID)

	st := b.renameLogs[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "No rename log configured.")
		return
	}

	st.mu.Lock()
	dest := st.Destination
	monitored := make([]snowflake.ID, len(st.Monitored))
	copy(monitored, st.Monitored)
	st.mu.Unlock()

	var sb strings.Builder
	if dest == 0 {
		sb.WriteString("**Destination:** not set\n")
	} else {
		sb.WriteString(fmt.Sprintf("**Destination:** %s\n", channelMention(dest)))
	}

	if len(monitored) == 0 {
		sb.WriteString("**Monitored channels:** none")
	} else {
		sb.WriteString("**Monitored channels:**\n")
		for _, m := range monitored {
			sb.WriteString(fmt.Sprintf("- %s\n", channelMention(m)))
		}
	}

	botutil.RespondEphemeral(e, sb.String())
}

func (b *Bot) handleRenameLogClear(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.Info("Rename log clear", "user_id", e.User().ID, "guild_id", *guildID)

	st := b.renameLogs[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	st.Destination = 0
	st.Monitored = nil
	st.mu.Unlock()

	if err := b.persistRenameLog(*guildID, st); err != nil {
		b.Log.Error("Failed to save rename log data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to clear rename log config.")
		return
	}

	botutil.RespondEphemeral(e, "Rename log config cleared.")
}
