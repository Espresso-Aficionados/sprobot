package bot

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type threadHelpInfo struct {
	HelperID     snowflake.ID
	LinkToPost   string
	MaxThreadAge time.Duration
	HistoryLimit int
}

func getThreadHelpConfig() map[snowflake.ID]threadHelpInfo {
	return map[snowflake.ID]threadHelpInfo{
		1019753326469980262: {
			HelperID:     1020401507121774722,
			LinkToPost:   "https://discord.com/channels/726985544038612993/727325278820368456/1020402429717663854",
			MaxThreadAge: 24 * time.Hour,
			HistoryLimit: 50,
		},
		1019680268229021807: {
			HelperID:     1015493549430685706,
			LinkToPost:   "https://discord.com/channels/1013566342345019512/1019680095893471322/1020431232129048667",
			MaxThreadAge: 5 * time.Minute,
			HistoryLimit: 5,
		},
	}
}

// forumReminderSync is how often the reminder reads the active-thread list
// over REST instead of relying on the gateway-fed channel cache.
const forumReminderSync = 24 * time.Hour

// forumThreadStatus is what the reminder knows of a thread's replies. Statuses
// only move up: a settled thread is never prompted.
type forumThreadStatus int

const (
	forumThreadUnknown forumThreadStatus = iota // e.g. created while the bot was offline
	forumThreadWaiting                          // only its owner has posted, as far as we've seen
	forumThreadDone                             // replied to, prompted, or too busy to tell
)

// forumReminderState is shared by the gateway handlers, which record new
// threads and replies, and the reminder loop.
type forumReminderState struct {
	mu       sync.Mutex
	threads  map[snowflake.ID]forumThreadStatus
	lastSync time.Time // last REST read of the active threads; used only by the loop
}

func (s *forumReminderState) status(id snowflake.ID) forumThreadStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threads[id]
}

// record raises a thread's status; it never moves a thread back down.
func (s *forumReminderState) record(id snowflake.ID, st forumThreadStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threads == nil {
		s.threads = make(map[snowflake.ID]forumThreadStatus)
	}
	if st > s.threads[id] {
		s.threads[id] = st
	}
}

// forget drops what is known of a thread, so its history is read again
// before any prompt.
func (s *forumReminderState) forget(id snowflake.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.threads, id)
}

// onForumThreadCreate records a thread created in a reminder forum while the
// bot is online: from here on, replies arrive as message events.
func (b *Bot) onForumThreadCreate(e *events.ThreadCreate) {
	if !e.NewlyCreated {
		return
	}
	if _, ok := b.threadHelpConfig[e.ParentID]; !ok {
		return
	}
	b.forumReminders.record(e.ThreadID, forumThreadWaiting)
}

// onForumThreadMessage settles a reminder-forum thread once someone other
// than its owner posts. Only the author is read, never the content.
func (b *Bot) onForumThreadMessage(e *events.GuildMessageCreate) {
	thread, ok := b.Client.Caches.GuildThread(e.ChannelID)
	if !ok {
		return
	}
	if _, ok := b.threadHelpConfig[*thread.ParentID()]; !ok {
		return
	}
	if e.Message.Author.ID != thread.OwnerID {
		b.forumReminders.record(thread.ID(), forumThreadDone)
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

	// The guilds' active threads just arrived with GUILD_CREATE, so the first
	// REST sync can wait a day.
	b.forumReminders.lastSync = time.Now()

	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	// Run immediately once, then on ticker
	safeRemind := func() {
		defer func() {
			if r := recover(); r != nil {
				b.Log.Error("Panic in forum reminder", "error", r)
			}
		}()
		b.sendForumReminders()
	}

	safeRemind()
	for {
		select {
		case <-b.stop:
			return
		case <-ticker.C:
			safeRemind()
		}
	}
}

func (b *Bot) sendForumReminders() {
	now := time.Now()
	fullSync := now.Sub(b.forumReminders.lastSync) >= forumReminderSync
	if fullSync {
		b.forumReminders.lastSync = now
	}
	for forumID, info := range b.threadHelpConfig {
		threads := b.forumThreads(forumID, fullSync)
		for _, thread := range forumReminderCandidates(threads, forumID, info.MaxThreadAge, now, b.forumReminders.status) {
			b.checkThread(thread, info)
		}
	}
}

// forumThreads returns the threads to consider for a forum: the channel
// cache, which gateway events keep current, or on a sync Discord's list of
// active threads, in case the cache missed some.
func (b *Bot) forumThreads(forumID snowflake.ID, fullSync bool) []discord.GuildThread {
	if forum, ok := b.Client.Caches.GuildForumChannel(forumID); ok && fullSync {
		active, err := b.Client.Rest.GetActiveGuildThreads(forum.GuildID())
		if err == nil {
			return active.Threads
		}
		b.Log.Error("Failed to get active threads", "guild_id", forum.GuildID(), "error", err)
	}
	return b.Client.Caches.GuildThreadsInChannel(forumID)
}

// forumReminderCandidates returns the forum's threads that may need a
// prompt: open, at least maxAge old, and not settled.
func forumReminderCandidates(threads []discord.GuildThread, forumID snowflake.ID, maxAge time.Duration, now time.Time, status func(snowflake.ID) forumThreadStatus) []discord.GuildThread {
	var out []discord.GuildThread
	for _, t := range threads {
		if p := t.ParentID(); p == nil || *p != forumID {
			continue
		}
		if t.ThreadMetadata.Archived || t.ThreadMetadata.Locked {
			continue
		}
		if now.Sub(t.CreatedAt()) < maxAge {
			continue
		}
		if status(t.ID()) == forumThreadDone {
			continue
		}
		out = append(out, t)
	}
	return out
}

// threadHistoryStatus settles a thread from its most recent messages
// (newest first, up to limit): replied to if anyone but the owner posted,
// too busy to tell if the history fills the limit, else still waiting.
func threadHistoryStatus(msgs []discord.Message, ownerID snowflake.ID, limit int) (forumThreadStatus, string) {
	for _, msg := range msgs {
		if msg.Author.ID != ownerID {
			return forumThreadDone, "Thread has a reply from a non-op author, skipping"
		}
	}
	if len(msgs) >= limit {
		return forumThreadDone, fmt.Sprintf("Thread has too many replies (>%d), skipping", limit)
	}
	return forumThreadWaiting, ""
}

// isPermanentRESTError reports whether Discord refused for good: the thread
// is gone (404) or the bot may not use it (403).
func isPermanentRESTError(err error) bool {
	var restErr *rest.Error
	if !errors.As(err, &restErr) || restErr.Response == nil {
		return false
	}
	code := restErr.Response.StatusCode
	return code == http.StatusForbidden || code == http.StatusNotFound
}

// checkThread prompts helpers in a candidate thread nobody but its owner has
// replied to.
func (b *Bot) checkThread(thread discord.GuildThread, info threadHelpInfo) {
	threadID := thread.ID()
	known := b.forumReminders.status(threadID) == forumThreadWaiting

	// message_count leaves out the starter post, so a count of 0 means there
	// is no history worth reading. That count may be stale; it is checked
	// again on the fresh copy below.
	if !known && thread.MessageCount > 0 {
		if !b.readThreadHistory(thread, info) {
			return
		}
		known = true
	}

	// Re-read the thread right before posting: the cache can be stale, and
	// posting in an archived thread would unarchive it.
	ch, err := b.Client.Rest.GetChannel(threadID)
	if err != nil {
		b.Log.Error("Failed to re-check thread before reminder", "error", err, "thread_id", threadID)
		if isPermanentRESTError(err) {
			b.forumReminders.record(threadID, forumThreadDone)
		}
		return
	}
	fresh, ok := ch.(discord.GuildThread)
	if !ok {
		return
	}
	if fresh.ThreadMetadata.Archived || fresh.ThreadMetadata.Locked {
		b.Log.Info("Thread is locked/archived, skipping", "thread_id", threadID, "thread_name", fresh.Name())
		// Keep the cache from offering it again, and read its history afresh
		// should it be reopened.
		b.Client.Caches.AddChannel(fresh)
		b.forumReminders.forget(threadID)
		return
	}
	if !known && fresh.MessageCount > 0 && !b.readThreadHistory(fresh, info) {
		return
	}

	b.Log.Info("Sending help prompt", "thread_id", threadID, "thread_name", fresh.Name())

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

	_, err = b.Client.Rest.CreateMessage(threadID, discord.MessageCreate{
		Content: helpMessage,
		Embeds:  []discord.Embed{embed},
	})
	if err != nil {
		b.Log.Error("Failed to send forum reminder", "error", err, "thread_id", threadID)
		// Try again next round unless Discord won't ever accept it.
		if !isPermanentRESTError(err) {
			return
		}
	}
	b.forumReminders.record(threadID, forumThreadDone)
}

// readThreadHistory reads a thread's recent messages and records what they
// show. It reports whether the thread is still waiting for a reply.
func (b *Bot) readThreadHistory(thread discord.GuildThread, info threadHelpInfo) bool {
	messages, err := b.Client.Rest.GetMessages(thread.ID(), 0, 0, 0, info.HistoryLimit)
	if err != nil {
		b.Log.Error("Failed to get thread messages", "error", err, "thread_id", thread.ID())
		return false
	}
	status, reason := threadHistoryStatus(messages, thread.OwnerID, info.HistoryLimit)
	if reason != "" {
		b.Log.Info(reason, "thread_id", thread.ID(), "thread_name", thread.Name())
	}
	b.forumReminders.record(thread.ID(), status)
	return status == forumThreadWaiting
}
