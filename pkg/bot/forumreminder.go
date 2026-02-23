package bot

import (
	"fmt"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

type threadHelpInfo struct {
	HelperID     snowflake.ID
	LinkToPost   string
	MaxThreadAge time.Duration
	HistoryLimit int
}

func getThreadHelpConfig(env string) map[snowflake.ID]threadHelpInfo {
	switch env {
	case "prod":
		return map[snowflake.ID]threadHelpInfo{
			1019753326469980262: {
				HelperID:     1020401507121774722,
				LinkToPost:   "https://discord.com/channels/726985544038612993/727325278820368456/1020402429717663854",
				MaxThreadAge: 24 * time.Hour,
				HistoryLimit: 50,
			},
		}
	case "dev":
		return map[snowflake.ID]threadHelpInfo{
			1019680268229021807: {
				HelperID:     1015493549430685706,
				LinkToPost:   "https://discord.com/channels/1013566342345019512/1019680095893471322/1020431232129048667",
				MaxThreadAge: 5 * time.Minute,
				HistoryLimit: 5,
			},
		}
	default:
		return nil
	}
}

func (b *Bot) forumReminderLoop() {
	// Wait for the bot to be ready
	readyTicker := time.NewTicker(1 * time.Second)
	defer readyTicker.Stop()
	for !b.Ready.Load() {
		select {
		case <-b.stop:
			return
		case <-readyTicker.C:
		}
	}

	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	// Run immediately once, then on ticker
	b.sendForumReminders()
	for {
		select {
		case <-b.stop:
			return
		case <-ticker.C:
			b.sendForumReminders()
		}
	}
}

func (b *Bot) sendForumReminders() {
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in forum reminder", "error", r)
		}
	}()

	config := getThreadHelpConfig(b.Env)
	if config == nil {
		return
	}

	for channelID, info := range config {
		channel, err := b.Client.Rest.GetChannel(channelID)
		if err != nil {
			b.Log.Info("Unknown channel to check for old forum posts", "channel_id", channelID)
			continue
		}

		if channel.Type() != discord.ChannelTypeGuildForum {
			b.Log.Info("Channel is not a ForumChannel", "channel_id", channelID, "type", channel.Type())
			continue
		}

		forumCh, ok := channel.(discord.GuildForumChannel)
		if !ok {
			continue
		}

		b.Log.Info("Scanning for threads",
			"guild_id", forumCh.GuildID(),
			"channel_id", channelID,
			"channel_name", channel.Name(),
		)

		// Get active threads for this guild
		activeThreads, err := b.Client.Rest.GetActiveGuildThreads(forumCh.GuildID())
		if err != nil {
			b.Log.Error("Failed to get active threads", "error", err)
			continue
		}

		for _, thread := range activeThreads.Threads {
			parentID := thread.ParentID()
			if parentID == nil || *parentID != channelID {
				continue
			}
			b.checkThread(thread, info)
		}
	}
}

func (b *Bot) checkThread(thread discord.GuildThread, info threadHelpInfo) {
	threadID := thread.ID()
	if reason, ok := b.skipList[threadID]; ok {
		b.Log.Info("Thread is in the skip_list",
			"reason", reason,
			"thread_id", thread.ID(),
			"thread_name", thread.Name(),
		)
		return
	}

	if thread.ThreadMetadata.Archived || thread.ThreadMetadata.Locked {
		b.Log.Info("Thread is locked/archived, skipping",
			"thread_id", thread.ID(),
			"thread_name", thread.Name(),
		)
		return
	}

	// Parse thread creation time from snowflake ID
	createdAt := thread.CreatedAt()
	threadAge := time.Since(createdAt)

	if threadAge < info.MaxThreadAge {
		b.Log.Info(fmt.Sprintf("Thread is only %s old, waiting until %s", threadAge, info.MaxThreadAge),
			"thread_id", thread.ID(),
			"thread_name", thread.Name(),
		)
		return
	}

	// Check message history
	messages, err := b.Client.Rest.GetMessages(thread.ID(), 0, 0, 0, info.HistoryLimit)
	if err != nil {
		b.Log.Error("Failed to get thread messages", "error", err, "thread_id", thread.ID())
		return
	}

	foundNonOP := false
	for _, msg := range messages {
		if msg.Author.ID != thread.OwnerID {
			foundNonOP = true
			break
		}
	}

	if foundNonOP {
		reason := "Thread has a reply from a non-op author, skipping"
		b.Log.Info(reason, "thread_id", thread.ID(), "thread_name", thread.Name())
		b.skipList[threadID] = reason
		return
	}

	if len(messages) >= info.HistoryLimit {
		reason := fmt.Sprintf("Thread has too many replies (>%d), skipping", info.HistoryLimit)
		b.Log.Info(reason, "thread_id", thread.ID(), "thread_name", thread.Name())
		b.skipList[threadID] = reason
		return
	}

	b.Log.Info("Sending help prompt", "thread_id", thread.ID(), "thread_name", thread.Name())

	helpMessage := fmt.Sprintf(
		"It looks like nobody has responded even though this thread has been open for a while. "+
			"Maybe one of our <@&%d> could help?",
		info.HelperID,
	)

	embed := discord.Embed{
		Description: fmt.Sprintf(
			"Want to be part of the <@&%d>? Sign up by reacting to this [post in #info](%s)",
			info.HelperID,
			info.LinkToPost,
		),
	}

	_, err = b.Client.Rest.CreateMessage(thread.ID(), discord.MessageCreate{
		Content: helpMessage,
		Embeds:  []discord.Embed{embed},
	})
	if err != nil {
		b.Log.Error("Failed to send forum reminder", "error", err, "thread_id", thread.ID())
	}

	b.skipList[threadID] = fmt.Sprintf("Already sent a response to %s", thread.Name())
}
