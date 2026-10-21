package bot

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

func TestGetThreadHelpConfig(t *testing.T) {
	config := getThreadHelpConfig()
	if config == nil {
		t.Fatal("config is nil")
	}
	if len(config) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(config))
	}

	info, ok := config[1019680268229021807]
	if !ok {
		t.Fatal("dev channel ID not found")
	}
	if info.HelperID != 1015493549430685706 {
		t.Errorf("dev HelperID = %d, want %d", info.HelperID, 1015493549430685706)
	}
	if info.MaxThreadAge != 5*time.Minute {
		t.Errorf("dev MaxThreadAge = %v, want 5m", info.MaxThreadAge)
	}
	if info.HistoryLimit != 5 {
		t.Errorf("dev HistoryLimit = %d, want 5", info.HistoryLimit)
	}

	info, ok = config[1019753326469980262]
	if !ok {
		t.Fatal("prod channel ID not found")
	}
	if info.MaxThreadAge != 24*time.Hour {
		t.Errorf("prod MaxThreadAge = %v, want 24h", info.MaxThreadAge)
	}
	if info.HistoryLimit != 50 {
		t.Errorf("prod HistoryLimit = %d, want 50", info.HistoryLimit)
	}
}

// forumThreadJSON is a thread in forum 500 of guild 10, owned by user 30.
func forumThreadJSON(id snowflake.ID, messageCount int, archived bool) string {
	return fmt.Sprintf(`{"id":"%d","type":11,"guild_id":"10","parent_id":"500","owner_id":"30","name":"help %d",`+
		`"message_count":%d,"thread_metadata":{"archived":%t}}`, id, id, messageCount, archived)
}

func TestForumReminderCandidates(t *testing.T) {
	now := time.Now()
	old := snowflake.New(now.Add(-2 * time.Hour))
	thread := func(id snowflake.ID, parent string, archived, locked bool) discord.GuildThread {
		return mustThread(t, fmt.Sprintf(`{"id":"%d","type":11,"parent_id":"%s","owner_id":"30","thread_metadata":{"archived":%t,"locked":%t}}`, id, parent, archived, locked))
	}
	threads := []discord.GuildThread{
		thread(old, "500", false, false),                                  // eligible
		thread(old+1, "501", false, false),                                // another channel
		thread(old+2, "500", true, false),                                 // archived
		thread(old+3, "500", false, true),                                 // locked
		thread(snowflake.New(now.Add(-time.Minute)), "500", false, false), // too new
		thread(old+4, "500", false, false),                                // settled
	}
	status := func(id snowflake.ID) forumThreadStatus {
		if id == old+4 {
			return forumThreadDone
		}
		return forumThreadUnknown
	}

	got := forumReminderCandidates(threads, 500, time.Hour, now, status)
	if len(got) != 1 || got[0].ID() != old {
		var ids []snowflake.ID
		for _, th := range got {
			ids = append(ids, th.ID())
		}
		t.Errorf("candidates = %v, want only %d", ids, old)
	}
}

func TestThreadHistoryStatus(t *testing.T) {
	msg := func(author snowflake.ID) discord.Message { return discord.Message{Author: discord.User{ID: author}} }
	tests := []struct {
		name string
		msgs []discord.Message
		want forumThreadStatus
	}{
		{"owner only", []discord.Message{msg(30), msg(30)}, forumThreadWaiting},
		{"reply", []discord.Message{msg(30), msg(31)}, forumThreadDone},
		{"history fills the limit", []discord.Message{msg(30), msg(30), msg(30)}, forumThreadDone},
		{"empty", nil, forumThreadWaiting},
	}
	for _, tt := range tests {
		if got, _ := threadHistoryStatus(tt.msgs, 30, 3); got != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestForumReminderStateNeverMovesBack(t *testing.T) {
	var s forumReminderState
	// With async events, a reply can be handled before its thread's create.
	s.record(1, forumThreadDone)
	s.record(1, forumThreadWaiting)
	if got := s.status(1); got != forumThreadDone {
		t.Errorf("status = %d, want a settled thread to stay settled", got)
	}
}

// newForumBot returns a bot reminding in forum 500 of guild 10 after an hour,
// with the daily REST sync not yet due.
func newForumBot(t *testing.T) (*Bot, *discordStub) {
	t.Helper()
	b, stub := newStubBot(t)
	b.threadHelpConfig = map[snowflake.ID]threadHelpInfo{
		500: {HelperID: 600, LinkToPost: "https://example.com/post", MaxThreadAge: time.Hour, HistoryLimit: 5},
	}
	b.forumReminders.lastSync = time.Now()
	b.Client.Caches.AddChannel(mustChannel(t, `{"id":"500","type":15,"guild_id":"10","name":"help"}`))
	return b, stub
}

// addForumThread caches a thread created age ago, answers GET for it, and,
// if live, records its creation as seen on the gateway.
func addForumThread(t *testing.T, b *Bot, stub *discordStub, age time.Duration, messageCount int, live bool) snowflake.ID {
	t.Helper()
	id := snowflake.New(time.Now().Add(-age))
	raw := forumThreadJSON(id, messageCount, false)
	thread := mustThread(t, raw)
	b.Client.Caches.AddChannel(thread)
	stub.on(fmt.Sprintf("GET /channels/%d", id), 200, raw)
	if live {
		b.onForumThreadCreate(&events.ThreadCreate{
			GenericThread: &events.GenericThread{GenericEvent: events.NewGenericEvent(b.Client, 0, 0), ThreadID: id, GuildID: 10, ParentID: 500, Thread: thread},
			NewlyCreated:  true,
		})
	}
	return id
}

func threadMessage(b *Bot, threadID, author snowflake.ID) *events.GuildMessageCreate {
	return &events.GuildMessageCreate{GenericGuildMessage: &events.GenericGuildMessage{
		GenericEvent: events.NewGenericEvent(b.Client, 0, 0),
		ChannelID:    threadID,
		GuildID:      10,
		Message:      discord.Message{ChannelID: threadID, Author: discord.User{ID: author}},
	}}
}

func TestForumReminderPromptsLiveThreadWithoutReadingHistory(t *testing.T) {
	b, stub := newForumBot(t)
	id := addForumThread(t, b, stub, 2*time.Hour, 2, true)
	b.onForumThreadMessage(threadMessage(b, id, 30)) // the owner adding detail

	b.sendForumReminders()

	want := []string{fmt.Sprintf("GET /channels/%d", id), fmt.Sprintf("POST /channels/%d/messages", id)}
	if got := stub.routesCalled(); !slices.Equal(got, want) {
		t.Errorf("calls = %v, want only the pre-post re-check and the prompt: %v", got, want)
	}

	stub.reset()
	b.sendForumReminders()
	if got := stub.routesCalled(); len(got) != 0 {
		t.Errorf("prompted thread checked again: %v", got)
	}
}

func TestForumReminderSkipsThreadRepliedToWhileOnline(t *testing.T) {
	b, stub := newForumBot(t)
	id := addForumThread(t, b, stub, 2*time.Hour, 1, true)
	b.onForumThreadMessage(threadMessage(b, id, 31))

	b.sendForumReminders()

	if got := stub.routesCalled(); len(got) != 0 {
		t.Errorf("calls = %v, want none for a thread with a reply", got)
	}
}

func TestForumReminderReadsHistoryOnlyWhenUnknown(t *testing.T) {
	b, stub := newForumBot(t)
	replied := addForumThread(t, b, stub, 2*time.Hour, 3, false)
	stub.on(fmt.Sprintf("GET /channels/%d/messages", replied), 200, `[{"id":"1","channel_id":"1","author":{"id":"31","username":"bob"}},{"id":"2","channel_id":"1","author":{"id":"30","username":"op"}}]`)
	untouched := addForumThread(t, b, stub, 3*time.Hour, 0, false)

	b.sendForumReminders()

	if got := stub.bodies(fmt.Sprintf("GET /channels/%d/messages", replied)); len(got) != 1 {
		t.Errorf("read the history of a thread with messages %d times, want 1", len(got))
	}
	if got := stub.bodies(fmt.Sprintf("POST /channels/%d/messages", replied)); len(got) != 0 {
		t.Error("prompted a thread someone replied to while the bot was offline")
	}
	if got := stub.bodies(fmt.Sprintf("GET /channels/%d/messages", untouched)); len(got) != 0 {
		t.Error("read the history of a thread with no messages past the starter")
	}
	if got := stub.bodies(fmt.Sprintf("POST /channels/%d/messages", untouched)); len(got) != 1 {
		t.Errorf("prompted the unanswered thread %d times, want 1", len(got))
	}

	stub.reset()
	b.sendForumReminders()
	if got := stub.routesCalled(); len(got) != 0 {
		t.Errorf("settled threads checked again: %v", got)
	}
}

func TestForumReminderStaleCountIsRechecked(t *testing.T) {
	b, stub := newForumBot(t)
	id := addForumThread(t, b, stub, 2*time.Hour, 0, false)
	// The cached copy says no messages, but the thread has some by now.
	stub.on(fmt.Sprintf("GET /channels/%d", id), 200, forumThreadJSON(id, 4, false))
	stub.on(fmt.Sprintf("GET /channels/%d/messages", id), 200, `[{"id":"1","channel_id":"1","author":{"id":"31","username":"bob"}}]`)

	b.sendForumReminders()

	if got := stub.bodies(fmt.Sprintf("POST /channels/%d/messages", id)); len(got) != 0 {
		t.Error("prompted a thread whose fresh copy shows a reply")
	}
}

func TestForumReminderLeavesArchivedThreadAlone(t *testing.T) {
	b, stub := newForumBot(t)
	id := addForumThread(t, b, stub, 2*time.Hour, 0, true)
	// Archived since it was cached, e.g. while the bot was offline.
	stub.on(fmt.Sprintf("GET /channels/%d", id), 200, forumThreadJSON(id, 0, true))

	b.sendForumReminders()
	if got := stub.bodies(fmt.Sprintf("POST /channels/%d/messages", id)); len(got) != 0 {
		t.Error("posted in (and so unarchived) an archived thread")
	}

	stub.reset()
	b.sendForumReminders()
	if got := stub.routesCalled(); len(got) != 0 {
		t.Errorf("archived thread re-checked every round: %v", got)
	}
}

func TestForumReminderRetriesFailedPost(t *testing.T) {
	b, stub := newForumBot(t)
	id := addForumThread(t, b, stub, 2*time.Hour, 0, true)
	post := fmt.Sprintf("POST /channels/%d/messages", id)

	stub.on(post, 500, discordError(0, "Internal Server Error"))
	b.sendForumReminders()
	stub.on(post, 200, "{}")
	b.sendForumReminders()
	b.sendForumReminders()

	if got := stub.bodies(post); len(got) != 2 {
		t.Errorf("posted %d times, want a retry after the failure and nothing after the success", len(got))
	}
}

func TestForumReminderGivesUpOnForbiddenThread(t *testing.T) {
	b, stub := newForumBot(t)
	id := addForumThread(t, b, stub, 2*time.Hour, 0, true)
	post := fmt.Sprintf("POST /channels/%d/messages", id)
	stub.on(post, 403, discordError(50001, "Missing Access"))

	b.sendForumReminders()
	b.sendForumReminders()

	if got := stub.bodies(post); len(got) != 1 {
		t.Errorf("posted %d times, want no retry after a 403", len(got))
	}
}

func TestForumReminderDailySync(t *testing.T) {
	b, stub := newForumBot(t)
	// A thread the cache missed, e.g. its create event was lost.
	id := snowflake.New(time.Now().Add(-2 * time.Hour))
	stub.on(fmt.Sprintf("GET /channels/%d", id), 200, forumThreadJSON(id, 0, false))
	stub.on("GET /guilds/10/threads/active", 200, `{"threads":[`+forumThreadJSON(id, 0, false)+`],"members":[]}`)

	b.sendForumReminders()
	if got := stub.routesCalled(); len(got) != 0 {
		t.Fatalf("calls before the sync is due: %v", got)
	}

	b.forumReminders.lastSync = time.Now().Add(-forumReminderSync)
	b.sendForumReminders()
	b.sendForumReminders()

	if got := stub.bodies("GET /guilds/10/threads/active"); len(got) != 1 {
		t.Errorf("read the active threads %d times, want once a day", len(got))
	}
	if got := stub.bodies(fmt.Sprintf("POST /channels/%d/messages", id)); len(got) != 1 {
		t.Errorf("prompted the thread found by the sync %d times, want 1", len(got))
	}
}
