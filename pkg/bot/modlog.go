package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
)

const embedSplitSize = 1024

type modLogState struct {
	mu        sync.Mutex
	ChannelID snowflake.ID `json:"channel_id"`
}

func defaultModLogConfig() map[snowflake.ID]snowflake.ID {
	return map[snowflake.ID]snowflake.ID{
		726985544038612993:  1141477354129080361,
		1013566342345019512: 1142519200682876938,
	}
}

func (b *Bot) loadModLog() {
	ctx := context.Background()
	defaults := defaultModLogConfig()
	for _, guildID := range b.GuildIDs() {
		st := &modLogState{}

		data, err := b.S3.FetchGuildJSON(ctx, "modlog", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			if chID, ok := defaults[guildID]; ok {
				st.ChannelID = chID
			}
			b.Log.Info("No existing modlog config, using defaults", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load modlog config", "guild_id", guildID, "error", err)
			if chID, ok := defaults[guildID]; ok {
				st.ChannelID = chID
			}
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode modlog config", "guild_id", guildID, "error", err)
			}
		}

		b.modLog[guildID] = st
		b.Log.Info("Loaded modlog config", "guild_id", guildID, "channel_id", st.ChannelID)
	}
}

func (b *Bot) persistModLog(guildID snowflake.ID, st *modLogState) error {
	st.mu.Lock()
	data, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return b.S3.SaveGuildJSON(context.Background(), "modlog", fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) saveModLog() {
	for guildID, st := range b.modLog {
		if err := b.persistModLog(guildID, st); err != nil {
			b.Log.Error("Failed to save modlog config", "guild_id", guildID, "error", err)
		}
	}
}

func (b *Bot) handleModLogMenu(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.MessageCommandInteractionData)
	if !ok {
		botutil.RespondEphemeral(e, "Oops! Something went wrong.")
		return
	}
	msg := data.TargetMessage()

	b.Log.Info("Processing save message to mod log",
		"user_id", userIDStr(e),
		"guild_id", guildIDStr(e),
	)

	// Send a modal asking for a mod note
	err := e.Modal(discord.ModalCreate{
		CustomID: fmt.Sprintf("modlog_%d_%d", msg.ChannelID, msg.ID),
		Title:    "Save Message to Mod Logs",
		Components: []discord.LayoutComponent{
			discord.NewLabel(
				"Mod Note about message",
				discord.TextInputComponent{
					CustomID:    "mod_note",
					Placeholder: "Context for why we're saving this",
					Style:       discord.TextInputStyleParagraph,
					MaxLength:   1024,
					Required:    false,
				},
			),
		},
	})
	if err != nil {
		b.Log.Error("Failed to respond with mod log modal", "error", err)
	}
}

func (b *Bot) handleModLogModalSubmit(e *events.ModalSubmitInteractionCreate, channelID, messageID snowflake.ID) {
	// Defer the response since this might take a while
	if err := e.DeferCreateMessage(true); err != nil {
		b.Log.Error("Failed to defer mod log modal response", "error", err)
		return
	}

	msg, err := b.Client.Rest.GetMessage(channelID, messageID)
	if err != nil {
		b.Log.Error("Failed to fetch original message", "error", err)
		b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
			Content: "Oops! Something went wrong.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	channel, err := b.Client.Rest.GetChannel(channelID)
	if err != nil {
		b.Log.Error("Failed to get channel", "error", err)
	}
	channelName := fmt.Sprintf("%d", channelID)
	if channel != nil {
		channelName = channel.Name()
	}

	embed := discord.Embed{
		Title: fmt.Sprintf("Message from @%s to #%s", msg.Author.Username, channelName),
	}

	avatarURL := msg.Author.EffectiveAvatarURL()
	embed.Author = &discord.EmbedAuthor{
		Name:    msg.Author.Username,
		IconURL: avatarURL,
	}

	requestorAvatarURL := e.User().EffectiveAvatarURL()
	embed.Footer = &discord.EmbedFooter{
		Text:    fmt.Sprintf("archived on behalf of @%s", e.User().Username),
		IconURL: requestorAvatarURL,
	}

	if msg.Content != "" {
		for idx := 0; idx < len(msg.Content); idx += embedSplitSize {
			end := idx + embedSplitSize
			if end > len(msg.Content) {
				end = len(msg.Content)
			}
			embed.Fields = append(embed.Fields, discord.EmbedField{
				Value: msg.Content[idx:end],
			})
		}
	}

	guildStr := guildIDStr(e)

	if len(msg.Attachments) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var permLinks []string
		for _, att := range msg.Attachments {
			permLink, err := b.S3.SaveModImage(ctx, guildStr, att.ProxyURL)
			if err != nil {
				b.Log.Error("Failed to save mod image", "error", err)
				permLinks = append(permLinks, att.ProxyURL)
			} else {
				permLink = b.S3.PresignExisting(ctx, permLink)
				permLinks = append(permLinks, permLink)
			}
		}
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name:  "Attachments",
			Value: strings.Join(permLinks, " "),
		})
	}

	embed.Fields = append(embed.Fields, discord.EmbedField{
		Name:  "Link to Message",
		Value: fmt.Sprintf("[Click here](%s)", messageLink(*e.GuildID(), channelID, messageID)),
	})

	timestampField := fmt.Sprintf("Created: %s UTC\n", msg.CreatedAt.Format(time.DateTime))
	if msg.EditedTimestamp != nil {
		timestampField += fmt.Sprintf("Edited: %s UTC\n", msg.EditedTimestamp.Format(time.DateTime))
	}
	timestampField += fmt.Sprintf("Archived: %s UTC", time.Now().UTC().Format(time.DateTime))
	embed.Fields = append(embed.Fields, discord.EmbedField{
		Name:  "Timestamps",
		Value: timestampField,
	})

	// Get mod note from modal
	modNote := e.Data.Text("mod_note")
	if modNote != "" {
		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name:  "Mod Note",
			Value: modNote,
		})
	}

	st := b.modLog[*e.GuildID()]
	if st == nil {
		b.Log.Info("No mod log config found")
		return
	}
	st.mu.Lock()
	modLogChannelID := st.ChannelID
	st.mu.Unlock()
	if modLogChannelID == 0 {
		b.Log.Info("Mod log channel not set")
		return
	}

	// Find or create thread in the mod log forum channel
	thread := b.findOrCreateModLogThread(modLogChannelID, msg.Author)
	if thread == nil {
		b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
			Content: "Oops! Something went wrong finding the mod log channel.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	sentMsg, err := b.Client.Rest.CreateMessage(thread.ID(), discord.MessageCreate{
		Embeds: []discord.Embed{embed},
	})
	if err != nil {
		b.Log.Error("Failed to send mod log message", "error", err)
		b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
			Content: "Oops! Something went wrong.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	modLogChannel, _ := b.Client.Rest.GetChannel(modLogChannelID)
	modLogChannelName := fmt.Sprintf("%d", modLogChannelID)
	if modLogChannel != nil {
		modLogChannelName = modLogChannel.Name()
	}

	notificationEmbed := discord.Embed{
		Title: fmt.Sprintf("Saved message to from %s in #%s/%s", msg.Author.Username, modLogChannelName, thread.Name()),
		URL:   messageLink(*e.GuildID(), thread.ID(), sentMsg.ID),
	}

	b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
		Embeds: []discord.Embed{notificationEmbed},
		Flags:  discord.MessageFlagEphemeral,
	})
}

func (b *Bot) findOrCreateModLogThread(forumChannelID snowflake.ID, author discord.User) discord.Channel {
	idTerm := fmt.Sprintf("- %d", author.ID)
	nameTerm := author.Username

	// Search active and archived threads for user ID match eagerly,
	// collecting candidates for a username fallback pass if ID isn't found.
	var candidates []discord.GuildThread

	ch, err := b.Client.Rest.GetChannel(forumChannelID)
	if err != nil {
		b.Log.Error("Failed to get mod log channel", "error", err)
		return nil
	}

	forumCh, ok := ch.(discord.GuildForumChannel)
	if !ok {
		b.Log.Error("Mod log channel is not a forum channel")
		return nil
	}

	// Active threads — check ID immediately, save for username fallback
	activeThreads, err := b.Client.Rest.GetActiveGuildThreads(forumCh.GuildID())
	if err != nil {
		b.Log.Error("Failed to get active threads", "error", err)
	}
	if activeThreads != nil {
		for _, thread := range activeThreads.Threads {
			parentID := thread.ParentID()
			if parentID == nil || *parentID != forumChannelID {
				continue
			}
			if strings.Contains(thread.Name(), idTerm) {
				return thread
			}
			candidates = append(candidates, thread)
		}
	}

	// Archived threads (paginated) — check ID each page, save for username fallback
	before := time.Time{}
	for {
		archivedThreads, err := b.Client.Rest.GetPublicArchivedThreads(forumChannelID, before, 0)
		if err != nil {
			b.Log.Error("Failed to get archived threads", "error", err)
			break
		}
		for _, thread := range archivedThreads.Threads {
			if strings.Contains(thread.Name(), idTerm) {
				return thread
			}
		}
		candidates = append(candidates, archivedThreads.Threads...)
		if !archivedThreads.HasMore || len(archivedThreads.Threads) == 0 {
			break
		}
		before = archivedThreads.Threads[len(archivedThreads.Threads)-1].ThreadMetadata.ArchiveTimestamp
	}

	// Fallback: match by username across all collected candidates
	for _, thread := range candidates {
		if strings.Contains(thread.Name(), nameTerm) {
			return thread
		}
	}

	// Create a new thread
	threadName := fmt.Sprintf("%s - %d", author.Username, author.ID)
	post, err := b.Client.Rest.CreatePostInThreadChannel(forumChannelID, discord.ThreadChannelPostCreate{
		Name:                threadName,
		AutoArchiveDuration: discord.AutoArchiveDuration1w,
		Message: discord.MessageCreate{
			Content: fmt.Sprintf("Topic for thread about @%s.", author.Username),
		},
	})
	if err != nil {
		b.Log.Error("Failed to create mod log thread", "error", err)
		return nil
	}
	return post.GuildThread
}

func messageLink(guildID, channelID, messageID snowflake.ID) string {
	return fmt.Sprintf("https://discord.com/channels/%d/%d/%d", guildID, channelID, messageID)
}

// --- /config modlog handlers ---

func (b *Bot) handleModLogConfig(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.modLog[*guildID]
	if st == nil {
		st = &modLogState{}
		b.modLog[*guildID] = st
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "set":
		b.handleModLogConfigSet(e, *guildID, st)
	case "show":
		b.handleModLogConfigShow(e, st)
	case "clear":
		b.handleModLogConfigClear(e, *guildID, st)
	case "audit":
		b.handleModLogAudit(e, *guildID, st)
	}
}

func (b *Bot) handleModLogConfigSet(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *modLogState) {
	data := e.Data.(discord.SlashCommandInteractionData)

	ch, ok := data.OptChannel("channel")
	if !ok {
		botutil.RespondEphemeral(e, "Please provide a channel.")
		return
	}

	b.Log.Info("Modlog config set", "user_id", e.User().ID, "guild_id", guildID, "channel_id", ch.ID)

	st.mu.Lock()
	st.ChannelID = ch.ID
	st.mu.Unlock()

	if err := b.persistModLog(guildID, st); err != nil {
		b.Log.Error("Failed to persist modlog config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Mod log forum channel set to <#%d>.", ch.ID))
}

func (b *Bot) handleModLogConfigShow(e *events.ApplicationCommandInteractionCreate, st *modLogState) {
	b.Log.Info("Modlog config show", "user_id", e.User().ID, "guild_id", *e.GuildID())

	st.mu.Lock()
	channelID := st.ChannelID
	st.mu.Unlock()

	if channelID == 0 {
		botutil.RespondEphemeral(e, "**Mod log channel:** Not set")
		return
	}
	botutil.RespondEphemeral(e, fmt.Sprintf("**Mod log channel:** <#%d>", channelID))
}

func (b *Bot) handleModLogConfigClear(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *modLogState) {
	b.Log.Info("Modlog config clear", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	st.ChannelID = 0
	st.mu.Unlock()

	if err := b.persistModLog(guildID, st); err != nil {
		b.Log.Error("Failed to persist modlog config", "guild_id", guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save configuration.")
		return
	}

	botutil.RespondEphemeral(e, "Mod log disabled.")
}

var threadUserIDRegex = regexp.MustCompile(`- (\d+)$`)

func parseThreadUserID(name string) string {
	m := threadUserIDRegex.FindStringSubmatch(name)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func (b *Bot) handleModLogAudit(e *events.ApplicationCommandInteractionCreate, guildID snowflake.ID, st *modLogState) {
	b.Log.Info("Modlog audit", "user_id", e.User().ID, "guild_id", guildID)

	st.mu.Lock()
	channelID := st.ChannelID
	st.mu.Unlock()

	if channelID == 0 {
		botutil.RespondEphemeral(e, "Mod log channel is not configured.")
		return
	}

	if err := e.DeferCreateMessage(true); err != nil {
		b.Log.Error("Failed to defer modlog audit response", "error", err)
		return
	}

	ch, err := b.Client.Rest.GetChannel(channelID)
	if err != nil {
		b.Log.Error("Failed to get mod log channel for audit", "error", err)
		b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
			Content: "Failed to access the mod log channel.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	forumCh, ok := ch.(discord.GuildForumChannel)
	if !ok {
		b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
			Content: "Mod log channel is not a forum channel.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	type threadInfo struct {
		Name string
		ID   snowflake.ID
	}

	// userID -> threads
	byUser := map[string][]threadInfo{}
	var unrecognized []threadInfo

	collect := func(thread discord.GuildThread) {
		uid := parseThreadUserID(thread.Name())
		info := threadInfo{Name: thread.Name(), ID: thread.ID()}
		if uid == "" {
			unrecognized = append(unrecognized, info)
		} else {
			byUser[uid] = append(byUser[uid], info)
		}
	}

	// Active threads
	activeThreads, err := b.Client.Rest.GetActiveGuildThreads(forumCh.GuildID())
	if err != nil {
		b.Log.Error("Failed to get active threads for audit", "error", err)
	}
	if activeThreads != nil {
		for _, thread := range activeThreads.Threads {
			parentID := thread.ParentID()
			if parentID == nil || *parentID != channelID {
				continue
			}
			collect(thread)
		}
	}

	// Archived threads (paginated)
	before := time.Time{}
	for {
		archivedThreads, err := b.Client.Rest.GetPublicArchivedThreads(channelID, before, 0)
		if err != nil {
			b.Log.Error("Failed to get archived threads for audit", "error", err)
			break
		}
		for _, thread := range archivedThreads.Threads {
			collect(thread)
		}
		if !archivedThreads.HasMore || len(archivedThreads.Threads) == 0 {
			break
		}
		before = archivedThreads.Threads[len(archivedThreads.Threads)-1].ThreadMetadata.ArchiveTimestamp
	}

	// Build report
	totalThreads := len(unrecognized)
	for _, threads := range byUser {
		totalThreads += len(threads)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "**Mod log audit** — %d threads scanned\n", totalThreads)

	hasDuplicates := false
	for uid, threads := range byUser {
		if len(threads) <= 1 {
			continue
		}
		hasDuplicates = true
		fmt.Fprintf(&sb, "\n**User ID %s** — %d threads:\n", uid, len(threads))
		for _, t := range threads {
			fmt.Fprintf(&sb, "- `%s` (<#%d>)\n", t.Name, t.ID)
		}
	}

	if len(unrecognized) > 0 {
		sb.WriteString("\n**Unrecognized thread names** (no parseable user ID):\n")
		for _, t := range unrecognized {
			fmt.Fprintf(&sb, "- `%s` (<#%d>)\n", t.Name, t.ID)
		}
	}

	if !hasDuplicates && len(unrecognized) == 0 {
		sb.WriteString("\nNo issues found.")
	}

	b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
		Content: sb.String(),
		Flags:   discord.MessageFlagEphemeral,
	})
}
