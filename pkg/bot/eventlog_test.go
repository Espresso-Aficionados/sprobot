package bot

import (
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestGetEventLogConfigDev(t *testing.T) {
	cfg := getEventLogConfig("dev")
	if cfg == nil {
		t.Fatal("dev config is nil")
	}
	c, ok := cfg[1013566342345019512]
	if !ok {
		t.Fatal("missing dev guild entry")
	}
	if c.ChannelID == 0 {
		t.Error("dev ChannelID should be set")
	}
}

func TestGetEventLogConfigProd(t *testing.T) {
	cfg := getEventLogConfig("prod")
	if cfg == nil {
		t.Fatal("prod config is nil")
	}
	c, ok := cfg[726985544038612993]
	if !ok {
		t.Fatal("missing prod guild entry")
	}
	if c.ChannelID == 0 {
		t.Error("prod ChannelID should be set")
	}
}

func TestGetEventLogConfigUnknown(t *testing.T) {
	if getEventLogConfig("staging") != nil {
		t.Error("expected nil for unknown env")
	}
	if getEventLogConfig("") != nil {
		t.Error("expected nil for empty env")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello world", 6, "hello…"},
		{"ab", 1, "…"},
	}
	for _, tt := range tests {
		got := truncate(tt.input, tt.max)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{400 * 24 * time.Hour, "1 years, 35 days"},
		{30 * 24 * time.Hour, "30 days"},
		{5 * time.Hour, "5 hours"},
		{15 * time.Minute, "15 minutes"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestChannelTypeName(t *testing.T) {
	tests := []struct {
		t    discord.ChannelType
		want string
	}{
		{discord.ChannelTypeGuildText, "Text"},
		{discord.ChannelTypeGuildVoice, "Voice"},
		{discord.ChannelTypeGuildForum, "Forum"},
	}
	for _, tt := range tests {
		got := channelTypeName(tt.t)
		if got != tt.want {
			t.Errorf("channelTypeName(%d) = %q, want %q", tt.t, got, tt.want)
		}
	}
}

func TestChannelMention(t *testing.T) {
	got := channelMention(snowflake.ID(123))
	if got != "<#123>" {
		t.Errorf("channelMention = %q, want <#123>", got)
	}
}

func TestUserMention(t *testing.T) {
	got := userMention(snowflake.ID(456))
	if got != "<@456>" {
		t.Errorf("userMention = %q, want <@456>", got)
	}
}

func TestBoolPtr(t *testing.T) {
	p := boolPtr(true)
	if p == nil || !*p {
		t.Error("boolPtr(true) should return pointer to true")
	}
	p = boolPtr(false)
	if p == nil || *p {
		t.Error("boolPtr(false) should return pointer to false")
	}
}

func TestEmbedColors(t *testing.T) {
	if colorRed != 0xED4245 {
		t.Errorf("colorRed = %X, want ED4245", colorRed)
	}
	if colorYellow != 0xFEE75C {
		t.Errorf("colorYellow = %X, want FEE75C", colorYellow)
	}
	if colorGreen != 0x57F287 {
		t.Errorf("colorGreen = %X, want 57F287", colorGreen)
	}
	if colorOrange != 0xE67E22 {
		t.Errorf("colorOrange = %X, want E67E22", colorOrange)
	}
	if colorBlue != 0x3498DB {
		t.Errorf("colorBlue = %X, want 3498DB", colorBlue)
	}
	if colorDarkRed != 0x992D22 {
		t.Errorf("colorDarkRed = %X, want 992D22", colorDarkRed)
	}
	if colorTeal != 0x1ABC9C {
		t.Errorf("colorTeal = %X, want 1ABC9C", colorTeal)
	}
}
