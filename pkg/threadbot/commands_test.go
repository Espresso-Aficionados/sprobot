package threadbot

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
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
