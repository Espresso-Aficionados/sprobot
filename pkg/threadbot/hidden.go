package threadbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/s3client"
	"github.com/Espresso-Aficionados/sprobot/pkg/statever"
)

// Hidden threads are never promoted: they are left out of channel reminders
// and /threads, including the "...and N more" count. They stay hidden until a
// mod unhides them or the thread is deleted.

var errHiddenNotLoaded = errors.New("hidden thread list not loaded")

type hiddenThreadsFile struct {
	ThreadIDs []snowflake.ID `json:"thread_ids"`
}

func encodeHiddenThreads(set map[snowflake.ID]struct{}) ([]byte, error) {
	ids := make([]snowflake.ID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	data, err := json.Marshal(hiddenThreadsFile{ThreadIDs: ids})
	if err != nil {
		return nil, err
	}
	return statever.Stamp(data)
}

func decodeHiddenThreads(guildID snowflake.ID, data []byte) (map[snowflake.ID]struct{}, error) {
	if _, err := statever.Of(fmt.Sprintf("%s/%d.json", s3client.HiddenThreadsPrefix, guildID), data); err != nil {
		return nil, err
	}
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

// ensureHiddenLoaded loads a guild's stored list unless it has loaded
// already. A failed load is retried on the next call; until one succeeds the
// list can't be changed, so a partial list never overwrites the stored one.
// Callers hold b.hiddenSaveMu.
func (b *Bot) ensureHiddenLoaded(ctx context.Context, guildID snowflake.ID) error {
	b.mu.Lock()
	loaded := b.hiddenLoaded[guildID]
	b.mu.Unlock()
	if loaded {
		return nil
	}

	set := map[snowflake.ID]struct{}{}
	data, err := b.S3.FetchHiddenThreads(ctx, fmt.Sprintf("%d", guildID))
	switch {
	case errors.Is(err, s3client.ErrNotFound):
	case err != nil:
		return fmt.Errorf("%w: %w", errHiddenNotLoaded, err)
	default:
		if set, err = decodeHiddenThreads(guildID, data); err != nil {
			return fmt.Errorf("%w: %w", errHiddenNotLoaded, err)
		}
	}

	b.mu.Lock()
	if b.hiddenLoaded == nil {
		b.hiddenLoaded = make(map[snowflake.ID]bool)
	}
	b.hidden[guildID] = set
	b.hiddenLoaded[guildID] = true
	b.mu.Unlock()
	b.Log.Info("Loaded hidden threads", "guild_id", guildID, "count", len(set))
	return nil
}

// hiddenSet returns a copy of the guild's hidden thread IDs, safe to use
// without holding b.mu. It fails if the stored list can't be loaded: listing
// threads as if none were hidden would promote the ones mods hid.
func (b *Bot) hiddenSet(ctx context.Context, guildID snowflake.ID) (map[snowflake.ID]struct{}, error) {
	b.hiddenSaveMu.Lock()
	err := b.ensureHiddenLoaded(ctx, guildID)
	b.hiddenSaveMu.Unlock()
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return maps.Clone(b.hidden[guildID]), nil
}

// setHidden hides or unhides a thread and persists the change. It reports
// whether anything changed. If the save fails the change is rolled back, so a
// retry saves again instead of finding nothing to do. hiddenSaveMu orders the
// saves, so an older snapshot can't land in S3 after a newer one.
func (b *Bot) setHidden(ctx context.Context, guildID, threadID snowflake.ID, hidden bool) (bool, error) {
	b.hiddenSaveMu.Lock()
	defer b.hiddenSaveMu.Unlock()

	// Saving before the stored list is loaded would overwrite it.
	if err := b.ensureHiddenLoaded(ctx, guildID); err != nil {
		return false, err
	}

	b.mu.Lock()
	set := b.hidden[guildID]
	_, was := set[threadID]
	if was == hidden {
		b.mu.Unlock()
		return false, nil
	}
	if set == nil {
		set = make(map[snowflake.ID]struct{})
		b.hidden[guildID] = set
	}
	apply := func(hide bool) {
		if hide {
			set[threadID] = struct{}{}
		} else {
			delete(set, threadID)
		}
	}
	apply(hidden)
	data, err := encodeHiddenThreads(set)
	b.mu.Unlock()

	if err == nil {
		err = b.S3.SaveHiddenThreads(ctx, fmt.Sprintf("%d", guildID), data)
	}
	if err != nil {
		b.mu.Lock()
		apply(was)
		b.mu.Unlock()
		return false, err
	}
	return true, nil
}

// loadHiddenThreads loads the hidden list of every allowed guild. Guilds that
// fail here load on first use. It returns the refusals of lists a newer
// build wrote.
func (b *Bot) loadHiddenThreads(ctx context.Context) error {
	var refused []error
	for _, guildID := range botutil.AllowedGuildIDs {
		b.hiddenSaveMu.Lock()
		err := b.ensureHiddenLoaded(ctx, guildID)
		b.hiddenSaveMu.Unlock()
		switch {
		case statever.IsNewer(err):
			refused = append(refused, err)
			b.Log.Error("Refusing hidden threads a newer build wrote; leaving them untouched", "guild_id", guildID, "error", err)
		case err != nil:
			b.Log.Error("Failed to load hidden threads; will retry on next use", "guild_id", guildID, "error", err)
		}
	}
	return errors.Join(refused...)
}

func (b *Bot) onThreadDelete(e *events.ThreadDelete) {
	ctx := context.Background()
	changed, err := b.setHidden(ctx, e.GuildID, e.ThreadID, false)
	switch {
	case errors.Is(err, errHiddenNotLoaded):
		// A deleted thread left in an unloaded list is harmless: it is never shown.
	case err != nil:
		b.Log.Error("Failed to save hidden threads after thread delete", "guild_id", e.GuildID, "thread_id", e.ThreadID, "error", err)
	case changed:
		b.Log.Info("Dropped deleted thread from hidden list", "guild_id", e.GuildID, "thread_id", e.ThreadID)
	}
}

func (b *Bot) handleHide(ctx context.Context, e *events.ApplicationCommandInteractionCreate, hide bool) {
	guildID := *e.GuildID()
	data := e.Data.(discord.SlashCommandInteractionData)
	thread, ok := data.OptChannel(optThread)
	if !ok || !isThread(thread.Type) {
		botutil.RespondEphemeral(ctx, e, "Pick a thread.")
		return
	}

	// Defer: loading and saving the list in S3 can outlast the interaction's
	// 3-second deadline.
	if err := e.DeferCreateMessage(true, rest.WithCtx(ctx)); err != nil {
		b.Log.Error("Failed to defer hide response", "error", err)
		return
	}
	reply := func(content string) { botutil.FollowupEphemeral(ctx, b.Client, e.Token(), content) }

	changed, err := b.setHidden(ctx, guildID, thread.ID, hide)
	if errors.Is(err, errHiddenNotLoaded) {
		b.Log.Error("Hidden thread list not loaded", "guild_id", guildID, "error", err)
		reply("Couldn't load the hidden thread list, so it can't be changed right now. Please try again in a minute.")
		return
	}
	if err != nil {
		b.Log.Error("Failed to save hidden threads", "guild_id", guildID, "thread_id", thread.ID, "error", err)
		reply("Something went wrong saving that, please try again.")
		return
	}
	b.Log.Info("Set thread hidden", "user_id", e.User().ID, "guild_id", guildID, "thread_id", thread.ID, "hidden", hide, "changed", changed)

	switch {
	case hide && changed:
		reply(fmt.Sprintf("<#%d> is now hidden from thread reminders and `/threads`.", thread.ID))
	case hide:
		reply(fmt.Sprintf("<#%d> was already hidden.", thread.ID))
	case changed:
		reply(fmt.Sprintf("<#%d> can be promoted again.", thread.ID))
	default:
		reply(fmt.Sprintf("<#%d> wasn't hidden.", thread.ID))
	}
}

func (b *Bot) handleHiddenList(ctx context.Context, e *events.ApplicationCommandInteractionCreate) {
	guildID := *e.GuildID()
	// Defer: loading the list from S3 can outlast the interaction's 3-second
	// deadline.
	if err := e.DeferCreateMessage(true, rest.WithCtx(ctx)); err != nil {
		b.Log.Error("Failed to defer hidden list response", "error", err)
		return
	}
	reply := func(content string) { botutil.FollowupEphemeral(ctx, b.Client, e.Token(), content) }

	set, err := b.hiddenSet(ctx, guildID)
	if err != nil {
		b.Log.Error("Hidden thread list not loaded", "guild_id", guildID, "error", err)
		reply("Couldn't load the hidden thread list. Please try again in a minute.")
		return
	}
	if len(set) == 0 {
		reply("No hidden threads in this server.")
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
	reply(sb.String())
}
