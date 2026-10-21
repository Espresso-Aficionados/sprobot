package threadbot

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

// commandEvent builds a slash command interaction from user 9 in channel 500
// of guildID, carrying data. Its responses go to the returned channel.
func commandEvent(t *testing.T, guildID snowflake.ID, data string) (*events.ApplicationCommandInteractionCreate, <-chan discord.MessageCreate) {
	t.Helper()
	raw := fmt.Sprintf(`{"id":"1","application_id":"2","type":2,"token":"tok","guild_id":"%d",
		"channel":{"id":"500","type":0,"guild_id":"%d","name":"coffee"},
		"user":{"id":"9","username":"mod"},"data":%s}`, guildID, guildID, data)
	var i discord.ApplicationCommandInteraction
	if err := json.Unmarshal([]byte(raw), &i); err != nil {
		t.Fatalf("decoding interaction: %v", err)
	}
	responses := make(chan discord.MessageCreate, 4)
	return &events.ApplicationCommandInteractionCreate{
		ApplicationCommandInteraction: i,
		Respond: func(_ discord.InteractionResponseType, d discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			m, _ := d.(discord.MessageCreate)
			responses <- m
			return nil
		},
	}, responses
}

const enableData = `{"id":"3","name":"threadbot","type":1,"options":[{"name":"enable","type":1}]}`

// Commands registered in a guild before it left the allowlist stay
// registered, so the handler itself must turn them away without creating
// state.
func TestCommandFromGuildNotAllowedCreatesNoState(t *testing.T) {
	fake := testutil.NewFakeS3()
	s3srv := httptest.NewServer(fake)
	defer s3srv.Close()
	b := newTestBot(t, testutil.NewTestS3Client(t, s3srv))

	const foreignGuild snowflake.ID = 555
	e, responses := commandEvent(t, foreignGuild, enableData)
	b.onCommand(e)

	select {
	case m := <-responses:
		if m.Flags != discord.MessageFlagEphemeral {
			t.Errorf("response = %+v, want an ephemeral notice", m)
		}
	default:
		t.Error("no response to a command from a guild that is not allowed")
	}
	if _, ok := b.reminders[foreignGuild]; ok {
		t.Error("reminders created for a guild that is not allowed")
	}
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	for key := range fake.Objects {
		t.Errorf("S3 object written: %s", key)
	}
}

// /threadbot refreshcounts must clear the stored counts too, or a restart
// brings them back. It is also how corrupt stored counts get replaced.
func TestRefreshCountsClearsStoredCounts(t *testing.T) {
	for _, stored := range []string{
		`{"counts":{"101":130},"fetched":{"101":"2026-10-21T00:00:00Z"}}`,
		`not json`,
	} {
		fake := testutil.NewFakeS3()
		srv := httptest.NewServer(fake)
		fake.Objects[devCountsKey] = []byte(stored)
		b := newTestBot(t, testutil.NewTestS3Client(t, srv))
		b.loadMemberCounts()

		e, responses := commandEvent(t, devGuild, `{"id":"3","name":"threadbot","type":1,"options":[{"name":"refreshcounts","type":1}]}`)
		b.onCommand(e)
		if m := <-responses; !strings.Contains(m.Content, "cleared") {
			t.Errorf("stored %s: response %q", stored, m.Content)
		}

		restarted := newTestBot(t, testutil.NewTestS3Client(t, srv))
		restarted.loadMemberCounts()
		if c := restarted.memberCounts[devGuild]; c == nil || len(c.Counts) != 0 {
			t.Errorf("stored %s: counts after restart = %+v, want an empty cache", stored, c)
		}
		srv.Close()
	}
}

// /threadbot list shows each reminder's settings and flags stored ones enable
// would reject, such as those applyDefaults rewrote, so mods can fix them.
func TestListShowsSettingsAndFlagsInvalidOnes(t *testing.T) {
	fake := testutil.NewFakeS3()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	fake.Objects[devRemindersKey] = []byte(`{
		"500":{"channel_id":"500","guild_id":"1013566342345019512","enabled":true,"min_idle_mins":0,"max_idle_mins":15,"msg_threshold":0,"time_threshold_mins":60,"buffer":0},
		"600":{"channel_id":"600","guild_id":"1013566342345019512","enabled":true,"min_idle_mins":30,"max_idle_mins":15,"msg_threshold":30,"time_threshold_mins":60,"buffer":5}}`)
	b := newTestBot(t, testutil.NewTestS3Client(t, srv))
	b.loadReminders()

	e, responses := commandEvent(t, devGuild, `{"id":"3","name":"threadbot","type":1,"options":[{"name":"list","type":1}]}`)
	b.onCommand(e)
	lines := strings.Split((<-responses).Content, "\n")
	slices.Sort(lines)
	if len(lines) != 2 {
		t.Fatalf("list = %q, want two lines", lines)
	}
	if want := "<#500> — enabled; idle 0–15 min, msg threshold 0, time threshold 60 min, buffer 0"; lines[0] != want {
		t.Errorf("valid reminder listed as %q, want %q", lines[0], want)
	}
	if !strings.HasPrefix(lines[1], "<#600> — enabled; idle 30–15 min") || !strings.Contains(lines[1], "invalid: max_idle (15) must be greater than min_idle (30).") {
		t.Errorf("invalid reminder listed as %q, want its settings flagged", lines[1])
	}
}

func TestIsThread(t *testing.T) {
	tests := []struct {
		ct   discord.ChannelType
		want bool
	}{
		{discord.ChannelTypeGuildPublicThread, true},
		{discord.ChannelTypeGuildPrivateThread, true},
		{discord.ChannelTypeGuildNewsThread, true},
		{discord.ChannelTypeGuildText, false},
		{discord.ChannelTypeGuildVoice, false},
		{discord.ChannelTypeGuildForum, false},
		{discord.ChannelTypeGuildCategory, false},
	}
	for _, tt := range tests {
		got := isThread(tt.ct)
		if got != tt.want {
			t.Errorf("isThread(%d) = %v, want %v", tt.ct, got, tt.want)
		}
	}
}

func TestValidateReminderParams(t *testing.T) {
	tests := []struct {
		name                                                  string
		minIdle, maxIdle, msgThreshold, timeThreshold, buffer int
		wantOK                                                bool
	}{
		{"valid defaults", 30, 720, 500, 720, 5, true},
		{"valid no buffer", 0, 10, 5, 0, 0, true},
		{"valid time only", 0, 10, 0, 5, 0, true},
		{"max_idle equals min_idle", 30, 30, 10, 0, 0, false},
		{"max_idle less than min_idle", 30, 20, 10, 0, 0, false},
		{"msg_threshold equals buffer", 0, 10, 5, 0, 5, false},
		{"msg_threshold less than buffer", 0, 10, 3, 0, 5, false},
		{"both thresholds zero", 0, 10, 0, 0, 0, false},
		{"msg_threshold zero buffer nonzero ok", 0, 10, 0, 5, 3, true},
		{"buffer at Get Channel Messages limit", 0, 10, 500, 0, 100, true},
		{"buffer over Get Channel Messages limit", 0, 10, 500, 0, 101, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errMsg := validateReminderParams(tt.minIdle, tt.maxIdle, tt.msgThreshold, tt.timeThreshold, tt.buffer)
			if tt.wantOK && errMsg != "" {
				t.Errorf("expected OK, got error: %q", errMsg)
			}
			if !tt.wantOK && errMsg == "" {
				t.Error("expected error, got OK")
			}
		})
	}
}
