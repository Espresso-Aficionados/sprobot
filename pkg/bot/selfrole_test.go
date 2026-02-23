package bot

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestIsSelfroleInteractionValid(t *testing.T) {
	id, ok := isSelfroleInteraction("selfrole_807495977362653214")
	if !ok {
		t.Fatal("expected true for valid selfrole custom ID")
	}
	if id != snowflake.ID(807495977362653214) {
		t.Errorf("got ID %d, want 807495977362653214", id)
	}
}

func TestIsSelfroleInteractionInvalid(t *testing.T) {
	_, ok := isSelfroleInteraction("ticket_open")
	if ok {
		t.Error("expected false for non-selfrole custom ID")
	}
}

func TestIsSelfroleInteractionMalformed(t *testing.T) {
	_, ok := isSelfroleInteraction("selfrole_notanumber")
	if ok {
		t.Error("expected false for malformed selfrole custom ID")
	}
}

func TestIsSelfroleInteractionEmpty(t *testing.T) {
	_, ok := isSelfroleInteraction("")
	if ok {
		t.Error("expected false for empty custom ID")
	}
}

func TestSelfroleLabel(t *testing.T) {
	label := selfroleLabel(1013566342345019512, 1015493549430685706, "dev")
	if label != "BOTBROS" {
		t.Errorf("label = %q, want %q", label, "BOTBROS")
	}
}

func TestSelfrroleLabelNotFound(t *testing.T) {
	label := selfroleLabel(1013566342345019512, 999, "dev")
	if label != "role" {
		t.Errorf("label = %q, want %q", label, "role")
	}
}

func TestSelfrroleLabelNilConfig(t *testing.T) {
	label := selfroleLabel(1013566342345019512, 1015493549430685706, "unknown")
	if label != "role" {
		t.Errorf("label = %q, want %q", label, "role")
	}
}

func TestSelfrolePanelNeedsUpdateMatch(t *testing.T) {
	cfg := selfroleConfig{
		Message: "Click to toggle",
		Buttons: []selfroleButton{
			{Label: "Test", Emoji: "🤖", RoleID: 123},
		},
	}
	embed := selfrolePanelEmbed(cfg)
	components := selfrolePanelButtons(cfg)
	msg := discord.Message{
		Embeds:     []discord.Embed{embed},
		Components: components,
	}
	if selfrolePanelNeedsUpdate(msg, cfg) {
		t.Error("expected no update needed for matching panel")
	}
}

func TestSelfrolePanelNeedsUpdateContentChange(t *testing.T) {
	cfg := selfroleConfig{
		Message: "Click to toggle",
		Buttons: []selfroleButton{
			{Label: "Test", Emoji: "🤖", RoleID: 123},
		},
	}
	embed := selfrolePanelEmbed(cfg)
	components := selfrolePanelButtons(cfg)
	msg := discord.Message{
		Content:    "extra content",
		Embeds:     []discord.Embed{embed},
		Components: components,
	}
	if !selfrolePanelNeedsUpdate(msg, cfg) {
		t.Error("expected update needed when message has extra content")
	}
}

func TestSelfrolePanelNeedsUpdateButtonChange(t *testing.T) {
	cfg := selfroleConfig{
		Message: "Click to toggle",
		Buttons: []selfroleButton{
			{Label: "Test", Emoji: "🤖", RoleID: 123},
			{Label: "Test2", Emoji: "🎉", RoleID: 456},
		},
	}
	embed := selfrolePanelEmbed(cfg)
	// Existing message only has one button
	msg := discord.Message{
		Embeds: []discord.Embed{embed},
		Components: []discord.LayoutComponent{
			discord.NewActionRow(discord.ButtonComponent{
				Style:    discord.ButtonStyleSecondary,
				Label:    "Test",
				CustomID: "selfrole_123",
				Emoji:    &discord.ComponentEmoji{Name: "🤖"},
			}),
		},
	}
	if !selfrolePanelNeedsUpdate(msg, cfg) {
		t.Error("expected update needed when button count differs")
	}
}

func TestSelfrolePanelNeedsUpdateRowMismatch(t *testing.T) {
	cfg := selfroleConfig{
		Message: "Click to toggle",
		Buttons: []selfroleButton{
			{Label: "Test", Emoji: "🤖", RoleID: 123},
		},
	}
	embed := selfrolePanelEmbed(cfg)
	msg := discord.Message{
		Embeds:     []discord.Embed{embed},
		Components: []discord.LayoutComponent{}, // no rows
	}
	if !selfrolePanelNeedsUpdate(msg, cfg) {
		t.Error("expected update needed when no component rows")
	}
}

func TestGetSelfroleConfigDev(t *testing.T) {
	cfg := getSelfroleConfig("dev")
	if cfg == nil {
		t.Fatal("dev config is nil")
	}
	cfgs, ok := cfg[1013566342345019512]
	if !ok {
		t.Fatal("missing dev guild")
	}
	if len(cfgs) != 1 {
		t.Errorf("expected 1 panel config, got %d", len(cfgs))
	}
}

func TestGetSelfroleConfigProd(t *testing.T) {
	cfg := getSelfroleConfig("prod")
	if cfg == nil {
		t.Fatal("prod config is nil")
	}
	cfgs, ok := cfg[726985544038612993]
	if !ok {
		t.Fatal("missing prod guild")
	}
	if len(cfgs) != 2 {
		t.Errorf("expected 2 panel configs, got %d", len(cfgs))
	}
}

func TestGetSelfroleConfigUnknown(t *testing.T) {
	if getSelfroleConfig("staging") != nil {
		t.Error("expected nil for unknown env")
	}
}
