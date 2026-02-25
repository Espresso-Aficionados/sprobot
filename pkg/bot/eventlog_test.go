package bot

import (
	"strings"
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

func TestChannelTypeNameAllCases(t *testing.T) {
	tests := []struct {
		ct   discord.ChannelType
		want string
	}{
		{discord.ChannelTypeGuildText, "Text"},
		{discord.ChannelTypeGuildVoice, "Voice"},
		{discord.ChannelTypeGuildCategory, "Category"},
		{discord.ChannelTypeGuildNews, "Announcement"},
		{discord.ChannelTypeGuildStageVoice, "Stage"},
		{discord.ChannelTypeGuildForum, "Forum"},
		{discord.ChannelTypeGuildMedia, "Media"},
		{discord.ChannelType(99), "Type 99"},
	}
	for _, tt := range tests {
		got := channelTypeName(tt.ct)
		if got != tt.want {
			t.Errorf("channelTypeName(%d) = %q, want %q", tt.ct, got, tt.want)
		}
	}
}

func TestTruncateExactBoundary(t *testing.T) {
	s := "abcde"
	got := truncate(s, 5)
	if got != "abcde" {
		t.Errorf("truncate at exact boundary = %q, want %q", got, "abcde")
	}
	got = truncate(s, 4)
	if got != "abc…" {
		t.Errorf("truncate at len-1 = %q, want %q", got, "abc…")
	}
}

func TestFormatPermissionDiffCreate(t *testing.T) {
	allow := discord.PermissionViewChannel | discord.PermissionSendMessages
	deny := discord.PermissionManageChannels
	result := formatPermissionDiff(discord.AuditLogEventChannelOverwriteCreate, 0, 0, allow, deny)

	if !strings.Contains(result, "✅ View Channel") {
		t.Errorf("result %q should contain allowed View Channel", result)
	}
	if !strings.Contains(result, "✅ Send Messages") {
		t.Errorf("result %q should contain allowed Send Messages", result)
	}
	if !strings.Contains(result, "❌ Manage Channels") {
		t.Errorf("result %q should contain denied Manage Channels", result)
	}
}

func TestFormatPermissionDiffDelete(t *testing.T) {
	oldAllow := discord.PermissionViewChannel
	oldDeny := discord.PermissionSendMessages
	result := formatPermissionDiff(discord.AuditLogEventChannelOverwriteDelete, oldAllow, oldDeny, 0, 0)

	if !strings.Contains(result, "↩️ View Channel") {
		t.Errorf("result %q should contain reset View Channel", result)
	}
	if !strings.Contains(result, "↩️ Send Messages") {
		t.Errorf("result %q should contain reset Send Messages", result)
	}
}

func TestFormatPermissionDiffUpdate(t *testing.T) {
	oldAllow := discord.PermissionViewChannel
	newAllow := discord.PermissionViewChannel | discord.PermissionSendMessages
	var oldDeny, newDeny discord.Permissions
	result := formatPermissionDiff(discord.AuditLogEventChannelOverwriteUpdate, oldAllow, oldDeny, newAllow, newDeny)

	if !strings.Contains(result, "✅ Send Messages") {
		t.Errorf("result %q should contain newly allowed Send Messages", result)
	}
	// View Channel was already allowed — should not appear
	if strings.Contains(result, "View Channel") {
		t.Errorf("result %q should not mention unchanged View Channel", result)
	}
}

func TestFormatPermissionDiffNoChanges(t *testing.T) {
	result := formatPermissionDiff(discord.AuditLogEventChannelOverwriteUpdate, 0, 0, 0, 0)
	if result != "*No recognizable permission changes*" {
		t.Errorf("expected no-changes message, got %q", result)
	}
}

func TestDerefStr(t *testing.T) {
	if got := derefStr(nil); got != "" {
		t.Errorf("derefStr(nil) = %q, want empty", got)
	}
	s := "hello"
	if got := derefStr(&s); got != "hello" {
		t.Errorf("derefStr(&hello) = %q, want hello", got)
	}
}

func TestDerefSnowflake(t *testing.T) {
	if got := derefSnowflake(nil); got != 0 {
		t.Errorf("derefSnowflake(nil) = %d, want 0", got)
	}
	id := snowflake.ID(42)
	if got := derefSnowflake(&id); got != 42 {
		t.Errorf("derefSnowflake(&42) = %d, want 42", got)
	}
}

func TestOrDash(t *testing.T) {
	if got := orDash(""); got != "-" {
		t.Errorf("orDash(\"\") = %q, want \"-\"", got)
	}
	if got := orDash("hello"); got != "hello" {
		t.Errorf("orDash(hello) = %q, want hello", got)
	}
}

func TestChannelMentionOrDash(t *testing.T) {
	if got := channelMentionOrDash(nil); got != "-" {
		t.Errorf("channelMentionOrDash(nil) = %q, want \"-\"", got)
	}
	zero := snowflake.ID(0)
	if got := channelMentionOrDash(&zero); got != "-" {
		t.Errorf("channelMentionOrDash(&0) = %q, want \"-\"", got)
	}
	id := snowflake.ID(123)
	if got := channelMentionOrDash(&id); got != "<#123>" {
		t.Errorf("channelMentionOrDash(&123) = %q, want <#123>", got)
	}
}

func TestFormatPartialRoleMentions(t *testing.T) {
	roles := []discord.PartialRole{
		{ID: 10, Name: "Admin"},
		{ID: 20, Name: "Mod"},
	}
	got := formatPartialRoleMentions(roles)
	if got != "<@&10>, <@&20>" {
		t.Errorf("formatPartialRoleMentions = %q", got)
	}
	if got := formatPartialRoleMentions(nil); got != "" {
		t.Errorf("formatPartialRoleMentions(nil) = %q, want empty", got)
	}
}

func TestAppendAuditFields(t *testing.T) {
	t.Run("with moderator and reason", func(t *testing.T) {
		embed := discord.Embed{}
		reason := "spam"
		entry := discord.AuditLogEntry{
			UserID: 999,
			Reason: &reason,
		}
		appendAuditFields(&embed, entry)
		if len(embed.Fields) != 2 {
			t.Fatalf("expected 2 fields, got %d", len(embed.Fields))
		}
		if !strings.Contains(embed.Fields[0].Value, "999") {
			t.Errorf("moderator field = %q, want mention of 999", embed.Fields[0].Value)
		}
		if embed.Fields[1].Value != "spam" {
			t.Errorf("reason field = %q, want spam", embed.Fields[1].Value)
		}
	})

	t.Run("no moderator no reason", func(t *testing.T) {
		embed := discord.Embed{}
		entry := discord.AuditLogEntry{}
		appendAuditFields(&embed, entry)
		if len(embed.Fields) != 0 {
			t.Fatalf("expected 0 fields, got %d", len(embed.Fields))
		}
	})

	t.Run("moderator only", func(t *testing.T) {
		embed := discord.Embed{}
		entry := discord.AuditLogEntry{UserID: 42}
		appendAuditFields(&embed, entry)
		if len(embed.Fields) != 1 {
			t.Fatalf("expected 1 field, got %d", len(embed.Fields))
		}
	})

	t.Run("empty reason ignored", func(t *testing.T) {
		embed := discord.Embed{}
		empty := ""
		entry := discord.AuditLogEntry{UserID: 42, Reason: &empty}
		appendAuditFields(&embed, entry)
		if len(embed.Fields) != 1 {
			t.Fatalf("expected 1 field (empty reason skipped), got %d", len(embed.Fields))
		}
	})
}

func TestTimePtrNotNil(t *testing.T) {
	now := time.Now()
	p := timePtr(now)
	if p == nil {
		t.Fatal("timePtr returned nil")
	}
	if !p.Equal(now) {
		t.Errorf("timePtr returned %v, want %v", *p, now)
	}
}
