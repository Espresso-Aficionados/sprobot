package threadbot

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

const (
	devGuild  snowflake.ID = 1013566342345019512
	prodGuild snowflake.ID = 726985544038612993

	devRemindersKey = "/test-bucket/threadreminders/1013566342345019512.json"
	devCountsKey    = "/test-bucket/threadmembercounts/1013566342345019512.json"
)

func (f *flakyS3) object(key string) []byte {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.Objects[key]
}

// A failed load must never be followed by a save of the partial state: the
// next /threadbot enable would otherwise replace every stored reminder in
// the guild with the one it adds.
func TestRemindersNeverSavedOverFailedLoad(t *testing.T) {
	fs := &flakyS3{FakeS3: testutil.NewFakeS3()}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	stored, _ := json.Marshal(map[string]*threadReminder{
		"111": {ChannelID: 111, GuildID: devGuild, MinIdleMins: 30, MaxIdleMins: 720, MsgThreshold: 500},
	})
	fs.Objects[devRemindersKey] = stored

	fs.failGet.Store(true)
	api := newFakeThreadAPI()
	b := newThreadListBot(t, api)
	b.S3 = testutil.NewTestS3Client(t, srv)
	defer b.stopAllReminderGoroutines()
	b.loadReminders()
	b.saveAllReminders()

	e, responses := commandEvent(t, devGuild, enableData)
	b.onCommand(e)
	<-responses // deferred
	if sent := api.sentFollowups(); len(sent) != 1 || !strings.Contains(sent[0].Content, "Couldn't load") {
		t.Errorf("enable while unloaded answered %+v, want a load failure notice", sent)
	}
	if got := fs.object(devRemindersKey); !bytes.Equal(got, stored) {
		t.Fatalf("stored reminders overwritten while unloaded: %s", got)
	}

	// Once S3 is back, the save loop's load retry (or the next command)
	// loads the stored reminders, and a change merges with them.
	fs.failGet.Store(false)
	b.loadReminders()
	b.mu.Lock()
	_, loaded := b.reminders[devGuild][111]
	b.mu.Unlock()
	if !loaded {
		t.Fatal("load not retried after S3 recovered")
	}
	e, responses = commandEvent(t, devGuild, enableData)
	b.onCommand(e)
	if m := <-responses; !strings.Contains(m.Content, "enabled") {
		t.Errorf("enable after recovery answered %q", m.Content)
	}
	var saved map[string]*threadReminder
	if err := json.Unmarshal(fs.object(devRemindersKey), &saved); err != nil {
		t.Fatal(err)
	}
	if saved["111"] == nil || saved["500"] == nil {
		t.Errorf("stored reminders = %v, want channels 111 and 500", saved)
	}
}

func TestMemberCountsNeverSavedOverFailedLoad(t *testing.T) {
	api := newFakeThreadAPI()
	api.addThread("101", memberCountCap, 120)
	apiSrv := httptest.NewServer(api)
	defer apiSrv.Close()
	fs := &flakyS3{FakeS3: testutil.NewFakeS3()}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	stored, _ := json.Marshal(&memberCountCache{
		Counts:  map[snowflake.ID]int{101: 130},
		Fetched: map[snowflake.ID]time.Time{101: time.Now()},
	})
	fs.Objects[devCountsKey] = stored

	b := newTestBot(t, testutil.NewTestS3Client(t, srv))
	b.Client = &bot.Client{Rest: rest.New(rest.NewClient("test", rest.WithURL(apiSrv.URL)))}
	b.loadHiddenThreads() // only the counts fail to load
	fs.failGet.Store(true)
	b.loadMemberCounts()

	// Unloaded: member_count is shown, nothing is fetched or saved.
	e, err := b.buildThreadsEmbed(devGuild, 500)
	if e == nil || err != nil || !strings.Contains(e.Description, "50 members") {
		t.Fatalf("embed while unloaded = %+v, %v; want member_count shown", e, err)
	}
	b.saveMemberCounts()
	if api.fetchCount("101") != 0 {
		t.Error("count fetched while the stored counts were unloaded")
	}
	if got := fs.object(devCountsKey); !bytes.Equal(got, stored) {
		t.Fatalf("stored counts overwritten while unloaded: %s", got)
	}

	// Once S3 is back, the stored count loads on next use, fresh, so it is
	// shown without a fetch.
	fs.failGet.Store(false)
	if e, err := b.buildThreadsEmbed(devGuild, 500); e == nil || err != nil || !strings.Contains(e.Description, "130 members") {
		t.Errorf("embed after recovery = %+v, %v; want the stored count", e, err)
	}
	if api.fetchCount("101") != 0 {
		t.Error("fresh stored count re-fetched")
	}
}

// State loads before the gateway opens, when the guild cache is empty, so the
// loaders cover every allowed guild rather than the cached ones.
func TestLoadersCoverAllowedGuilds(t *testing.T) {
	fake := testutil.NewFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	reminders, _ := json.Marshal(map[string]*threadReminder{"111": {ChannelID: 111, GuildID: prodGuild}})
	counts, _ := json.Marshal(&memberCountCache{Counts: map[snowflake.ID]int{7: 70}})
	fake.Objects["/test-bucket/threadreminders/726985544038612993.json"] = reminders
	fake.Objects["/test-bucket/threadmembercounts/726985544038612993.json"] = counts
	fake.Objects["/test-bucket/threadhidden/726985544038612993.json"] = []byte(`{"thread_ids":["9"]}`)

	b := newTestBot(t, testutil.NewTestS3Client(t, srv))
	b.TestGuildIDsOverride = []snowflake.ID{} // nothing cached yet
	b.loadReminders()
	b.loadMemberCounts()
	b.loadHiddenThreads()

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reminders[prodGuild][111] == nil {
		t.Error("prod reminders not loaded")
	}
	if b.memberCounts[prodGuild] == nil || b.memberCounts[prodGuild].Counts[7] != 70 {
		t.Error("prod member counts not loaded")
	}
	if _, ok := b.hidden[prodGuild][9]; !ok || !b.hiddenLoaded[prodGuild] {
		t.Error("prod hidden threads not loaded")
	}
	// Nothing stored is loaded too, so the dev guild can be saved.
	if _, ok := b.reminders[devGuild]; !ok {
		t.Error("dev guild with no stored reminders not marked loaded")
	}
}

// Every allowed guild loads at startup, but the periodic and shutdown saves
// write only guilds the bot is in: a dev instance sharing the bucket must not
// write its startup copy of prod's reminders back over prod's changes.
func TestPeriodicSavesSkipGuildsBotIsNotIn(t *testing.T) {
	fake := testutil.NewFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	const prodRemindersKey = "/test-bucket/threadreminders/726985544038612993.json"
	const prodCountsKey = "/test-bucket/threadmembercounts/726985544038612993.json"
	fake.Objects[prodRemindersKey] = []byte(`{}`)
	fake.Objects[prodCountsKey] = []byte(`{"counts":{},"fetched":{}}`)

	b := newTestBot(t, testutil.NewTestS3Client(t, srv)) // in the dev guild only
	b.loadReminders()
	b.loadMemberCounts()
	// Meanwhile the prod instance enables a reminder and saves it.
	prodState := []byte(`{"111":{"channel_id":"111","guild_id":"726985544038612993"}}`)
	fake.Mu.Lock()
	fake.Objects[prodRemindersKey] = prodState
	fake.Mu.Unlock()

	b.saveAllReminders()
	b.saveMemberCounts()

	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	if got := fake.Objects[prodRemindersKey]; !bytes.Equal(got, prodState) {
		t.Errorf("prod reminders overwritten by a bot not in prod: %s", got)
	}
	if _, ok := fake.Objects[devRemindersKey]; !ok {
		t.Error("dev reminders not saved")
	}
	if _, ok := fake.Objects[devCountsKey]; !ok {
		t.Error("dev member counts not saved")
	}
}

// Loaders write the maps that event handlers read; -race catches a load that
// doesn't hold b.mu.
func TestLoadRacesWithEvents(t *testing.T) {
	fake := testutil.NewFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	b := newTestBot(t, testutil.NewTestS3Client(t, srv))

	guildID := devGuild
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 50 {
			b.onMessage(&events.MessageCreate{GenericMessage: &events.GenericMessage{
				GuildID: &guildID, ChannelID: 500, Message: discord.Message{Author: discord.User{ID: 9}},
			}})
			b.adjustMemberCount(guildID, 101, 1)
			b.onSessionReady(&events.Ready{EventReady: gateway.EventReady{}})
		}
	}()
	b.loadReminders()
	b.loadMemberCounts()
	<-done
}

func guildReady(guildID snowflake.ID) *events.GuildReady {
	return &events.GuildReady{GenericGuild: &events.GenericGuild{GuildID: guildID}}
}

// running reports which of the guild's reminders have a loop.
func running(b *Bot, guildID snowflake.ID) map[snowflake.ID]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[snowflake.ID]bool{}
	for id, r := range b.reminders[guildID] {
		out[id] = r.handle != nil
	}
	return out
}

// seedReminders stores enabled reminders for channels 500, 600, 700 and 900
// of the dev guild and one for the prod guild.
func seedReminders(fake *testutil.FakeS3) {
	r := func(guildID, channelID snowflake.ID) *threadReminder {
		return &threadReminder{ChannelID: channelID, GuildID: guildID, Enabled: true, MinIdleMins: 30, MaxIdleMins: 720, MsgThreshold: 500, TimeThresholdMins: 720, Buffer: 5}
	}
	dev, _ := json.Marshal(map[string]*threadReminder{"500": r(devGuild, 500), "600": r(devGuild, 600), "700": r(devGuild, 700), "900": r(devGuild, 900)})
	prod, _ := json.Marshal(map[string]*threadReminder{"800": r(prodGuild, 800)})
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	fake.Objects[devRemindersKey] = dev
	fake.Objects["/test-bucket/threadreminders/726985544038612993.json"] = prod
}

// Reminders load for every allowed guild before the gateway opens, but loops
// start only once the gateway says the bot is in the guild. Discord never
// replays a channel delete, so then a reminder whose channel was deleted
// meanwhile is dropped and the drop saved; one whose channel the bot can't
// see, or that Get Channel fails on, is kept.
func TestGuildReadyStartsLoopsAndDropsDeletedChannels(t *testing.T) {
	api := newFakeThreadAPI()
	api.channelErrs["600"] = int(rest.JSONErrorCodeUnknownChannel)
	api.channelErrs["700"] = 50001 // Missing Access
	api.channelErrs["900"] = 0     // 500 Internal Server Error
	b := newThreadListBot(t, api)
	fake := testutil.NewFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	b.S3 = testutil.NewTestS3Client(t, srv)
	seedReminders(fake)
	defer b.stopAllReminderGoroutines()

	b.loadReminders()
	if got := running(b, devGuild); len(got) != 4 || got[500] || got[600] || got[700] || got[900] {
		t.Fatalf("after load: running = %v, want four loaded and none running", got)
	}

	b.onGuildReady(guildReady(devGuild))
	if got := running(b, devGuild); len(got) != 3 || !got[500] || !got[700] || !got[900] {
		t.Errorf("after GuildReady: running = %v, want 500, 700 and 900 running and 600 dropped", got)
	}
	// No GuildReady for prod: this bot isn't in it.
	if got := running(b, prodGuild); len(got) != 1 || got[800] {
		t.Errorf("prod: running = %v, want loaded and not running", got)
	}

	restarted := newTestBot(t, b.S3)
	restarted.loadReminders()
	got := running(restarted, devGuild)
	if _, ok := got[600]; ok || len(got) != 3 {
		t.Errorf("stored reminders after restart = %v, want 500, 700 and 900", got)
	}
}

// A guild whose reminders load only after its GuildReady, because the
// startup load failed, starts them, and drops deleted channels, on that load.
func TestLateLoadStartsReadyGuild(t *testing.T) {
	api := newFakeThreadAPI()
	api.channelErrs["600"] = int(rest.JSONErrorCodeUnknownChannel)
	b := newThreadListBot(t, api)
	fs := &flakyS3{FakeS3: testutil.NewFakeS3()}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	b.S3 = testutil.NewTestS3Client(t, srv)
	seedReminders(fs.FakeS3)
	defer b.stopAllReminderGoroutines()

	fs.failGet.Store(true)
	b.loadReminders()
	b.onGuildReady(guildReady(devGuild))
	fs.failGet.Store(false)
	b.loadReminders()

	if got := running(b, devGuild); len(got) != 3 || !got[500] || !got[700] || !got[900] {
		t.Errorf("running = %v, want 500, 700 and 900 running and 600 dropped", got)
	}
}

// While a guild's reminders are unloaded, a command defers before retrying
// the load, so a slow S3 can't make it miss Discord's 3-second deadline. A
// load that then succeeds carries out the command.
func TestReminderCommandDefersBeforeLoading(t *testing.T) {
	s3 := newSlowS3(http.MethodGet)
	srv := httptest.NewServer(s3)
	defer srv.Close()
	defer close(s3.release)
	api := newFakeThreadAPI()
	b := newThreadListBot(t, api)
	b.S3 = testutil.NewTestS3Client(t, srv)
	defer b.stopAllReminderGoroutines()

	e, responses := commandEvent(t, devGuild, enableData)
	done := make(chan struct{})
	go func() {
		b.onCommand(e)
		close(done)
	}()
	select {
	case <-s3.started:
	case <-time.After(5 * time.Second):
		t.Fatal("enable never loaded the reminders")
	}
	select {
	case m := <-responses:
		if m.Flags != discord.MessageFlagEphemeral || m.Content != "" {
			t.Errorf("first response = %+v, want an ephemeral deferral", m)
		}
	default:
		t.Fatal("enable did not answer the interaction before loading")
	}

	s3.release <- struct{}{}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("enable did not finish after the load")
	}
	if sent := api.sentFollowups(); len(sent) != 1 || !strings.Contains(sent[0].Content, "enabled") {
		t.Errorf("follow-ups = %+v, want the outcome", sent)
	}
	if s3.object(devRemindersKey) == nil {
		t.Error("enabled reminder not saved")
	}
}

// Saves are ordered: a save that snapshots the reminders after a change
// can't be overwritten by one that snapshotted them before it.
func TestReminderSavesLandInOrder(t *testing.T) {
	s3 := newSlowS3(http.MethodPut)
	srv := httptest.NewServer(s3)
	defer srv.Close()
	b := newTestBot(t, testutil.NewTestS3Client(t, srv))
	b.reminders[devGuild] = map[snowflake.ID]*threadReminder{
		500: {ChannelID: 500, GuildID: devGuild},
		600: {ChannelID: 600, GuildID: devGuild},
	}

	first := make(chan struct{})
	go func() {
		b.saveRemindersForGuild(devGuild)
		close(first)
	}()
	<-s3.started // the snapshot with 500 and 600 is in flight
	b.mu.Lock()
	delete(b.reminders[devGuild], 500)
	b.mu.Unlock()
	second := make(chan struct{})
	go func() {
		b.saveRemindersForGuild(devGuild)
		close(second)
	}()
	select {
	case <-s3.started:
		t.Error("second save sent while the first was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(s3.release)
	<-first
	<-second

	var saved map[string]*threadReminder
	if err := json.Unmarshal(s3.object(devRemindersKey), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved["600"] == nil {
		t.Errorf("stored reminders = %v, want only 600", saved)
	}
}
