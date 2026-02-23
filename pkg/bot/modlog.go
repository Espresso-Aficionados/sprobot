package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

const embedSplitSize = 1024

type modLogInfo struct {
	ChannelID snowflake.ID
}

func getModLogConfig(env string) *modLogInfo {
	switch env {
	case "prod":
		return &modLogInfo{ChannelID: 1141477354129080361}
	case "dev":
		return &modLogInfo{ChannelID: 1142519200682876938}
	default:
		return nil
	}
}

func (b *Bot) handleModLogMenu(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.MessageCommandInteractionData)
	if !ok {
		respondEphemeral(e, "Oops! Something went wrong.")
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
		var permLinks []string
		for _, att := range msg.Attachments {
			permLink, err := b.S3.SaveModImage(context.Background(), guildStr, att.ProxyURL)
			if err != nil {
				b.Log.Error("Failed to save mod image", "error", err)
				permLinks = append(permLinks, att.ProxyURL)
			} else {
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
		Value: fmt.Sprintf("[Click here](%s)", messageLink(guildStr, fmt.Sprintf("%d", channelID), fmt.Sprintf("%d", messageID))),
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

	modLogConfig := getModLogConfig(b.Env)
	if modLogConfig == nil {
		b.Log.Info("No mod log config found")
		return
	}

	// Find or create thread in the mod log forum channel
	thread := b.findOrCreateModLogThread(modLogConfig.ChannelID, msg.Author)
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

	modLogChannel, _ := b.Client.Rest.GetChannel(modLogConfig.ChannelID)
	modLogChannelName := fmt.Sprintf("%d", modLogConfig.ChannelID)
	if modLogChannel != nil {
		modLogChannelName = modLogChannel.Name()
	}

	notificationEmbed := discord.Embed{
		Title: fmt.Sprintf("Saved message to from %s in #%s/%s", msg.Author.Username, modLogChannelName, thread.Name()),
		URL:   messageLink(guildStr, fmt.Sprintf("%d", thread.ID()), fmt.Sprintf("%d", sentMsg.ID)),
	}

	b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
		Embeds: []discord.Embed{notificationEmbed},
		Flags:  discord.MessageFlagEphemeral,
	})
}

func (b *Bot) findOrCreateModLogThread(forumChannelID snowflake.ID, author discord.User) discord.Channel {
	searchTerms := []string{
		fmt.Sprintf("- %d", author.ID),
		author.Username,
	}

	// Search active threads - need to get guild ID from channel
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
			for _, term := range searchTerms {
				if strings.Contains(thread.Name(), term) {
					return thread
				}
			}
		}
	}

	// Search archived threads
	archivedThreads, err := b.Client.Rest.GetPublicArchivedThreads(forumChannelID, time.Time{}, 0)
	if err == nil {
		for _, thread := range archivedThreads.Threads {
			for _, term := range searchTerms {
				if strings.Contains(thread.Name(), term) {
					return thread
				}
			}
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

func messageLink(guildID, channelID, messageID string) string {
	return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, channelID, messageID)
}
