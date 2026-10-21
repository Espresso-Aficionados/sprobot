package threadbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
)

// Hidden threads are never promoted: they are left out of channel reminders
// and /threads, including the "...and N more" count. They stay hidden until a
// mod unhides them or the thread is deleted.

type hiddenThreadsFile struct {
	ThreadIDs []snowflake.ID `json:"thread_ids"`
}

func encodeHiddenThreads(set map[snowflake.ID]struct{}) ([]byte, error) {
	ids := make([]snowflake.ID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return json.Marshal(hiddenThreadsFile{ThreadIDs: ids})
}

func decodeHiddenThreads(data []byte) (map[snowflake.ID]struct{}, error) {
	var f hiddenThreadsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	set := make(map[snowflake.ID]struct{}, len(f.ThreadIDs))
	for _, id := range f.ThreadIDs {
		set[id] = struct{}{}
	}
	return set, nil
}

// hiddenSet returns a copy of the guild's hidden thread IDs, safe to use
// without holding b.mu.
func (b *Bot) hiddenSet(guildID snowflake.ID) map[snowflake.ID]struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[snowflake.ID]struct{}, len(b.hidden[guildID]))
	for id := range b.hidden[guildID] {
		out[id] = struct{}{}
	}
	return out
}

// setHidden hides or unhides a thread and persists the change. It reports
// whether anything changed.
func (b *Bot) setHidden(guildID, threadID snowflake.ID, hidden bool) (bool, error) {
	b.mu.Lock()
	set := b.hidden[guildID]
	_, was := set[threadID]
	if was == hidden {
		b.mu.Unlock()
		return false, nil
	}
	if hidden {
		if set == nil {
			set = make(map[snowflake.ID]struct{})
			b.hidden[guildID] = set
		}
		set[threadID] = struct{}{}
	} else {
		delete(set, threadID)
	}
	data, err := encodeHiddenThreads(set)
	b.mu.Unlock()
	if err != nil {
		return true, err
	}
	return true, b.S3.SaveHiddenThreads(context.Background(), fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) loadHiddenThreads() {
	ctx := context.Background()
	for _, guildID := range b.GuildIDs() {
		data, err := b.S3.FetchHiddenThreads(ctx, fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			continue
		}
		if err != nil {
			b.Log.Error("Failed to load hidden threads", "guild_id", guildID, "error", err)
			continue
		}
		set, err := decodeHiddenThreads(data)
		if err != nil {
			b.Log.Error("Failed to decode hidden threads", "guild_id", guildID, "error", err)
			continue
		}
		b.mu.Lock()
		b.hidden[guildID] = set
		b.mu.Unlock()
		b.Log.Info("Loaded hidden threads", "guild_id", guildID, "count", len(set))
	}
}

func (b *Bot) onThreadDelete(e *events.ThreadDelete) {
	if changed, err := b.setHidden(e.GuildID, e.ThreadID, false); changed {
		if err != nil {
			b.Log.Error("Failed to save hidden threads after thread delete", "guild_id", e.GuildID, "thread_id", e.ThreadID, "error", err)
			return
		}
		b.Log.Info("Dropped deleted thread from hidden list", "guild_id", e.GuildID, "thread_id", e.ThreadID)
	}
}

func (b *Bot) handleHide(e *events.ApplicationCommandInteractionCreate, hide bool) {
	guildID := *e.GuildID()
	data := e.Data.(discord.SlashCommandInteractionData)
	thread, ok := data.OptChannel(optThread)
	if !ok || !isThread(thread.Type) {
		botutil.RespondEphemeral(e, "Pick a thread.")
		return
	}

	changed, err := b.setHidden(guildID, thread.ID, hide)
	if err != nil {
		b.Log.Error("Failed to save hidden threads", "guild_id", guildID, "thread_id", thread.ID, "error", err)
		botutil.RespondEphemeral(e, "Something went wrong saving that, please try again.")
		return
	}
	b.Log.Info("Set thread hidden", "user_id", e.User().ID, "guild_id", guildID, "thread_id", thread.ID, "hidden", hide, "changed", changed)

	switch {
	case hide && changed:
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> is now hidden from thread reminders and `/threads`.", thread.ID))
	case hide:
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> was already hidden.", thread.ID))
	case changed:
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> can be promoted again.", thread.ID))
	default:
		botutil.RespondEphemeral(e, fmt.Sprintf("<#%d> wasn't hidden.", thread.ID))
	}
}

func (b *Bot) handleHiddenList(e *events.ApplicationCommandInteractionCreate) {
	set := b.hiddenSet(*e.GuildID())
	if len(set) == 0 {
		botutil.RespondEphemeral(e, "No hidden threads in this server.")
		return
	}
	ids := make([]snowflake.ID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var sb strings.Builder
	sb.WriteString("Hidden from thread reminders and `/threads`:\n")
	for i, id := range ids {
		line := fmt.Sprintf("- <#%d>\n", id)
		if sb.Len()+len(line) > 1900 {
			fmt.Fprintf(&sb, "...and %d more\n", len(ids)-i)
			break
		}
		sb.WriteString(line)
	}
	botutil.RespondEphemeral(e, sb.String())
}
