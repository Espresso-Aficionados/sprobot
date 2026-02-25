package threadbot

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
)

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
