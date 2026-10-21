package threadbot

import (
	"bytes"
	"encoding/json"
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
	b := newTestBot(t, testutil.NewTestS3Client(t, srv))
	defer b.stopAllReminderGoroutines()
	b.loadReminders()
	b.saveAllReminders()

	e, responses := commandEvent(t, devGuild, enableData)
	b.onCommand(e)
	if m := <-responses; !strings.Contains(m.Content, "Couldn't load") {
		t.Errorf("enable while unloaded answered %q, want a load failure notice", m.Content)
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

	fs.failGet.Store(true)
	b := newTestBot(t, testutil.NewTestS3Client(t, srv))
	b.Client = &bot.Client{Rest: rest.New(rest.NewClient("test", rest.WithURL(apiSrv.URL)))}
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
