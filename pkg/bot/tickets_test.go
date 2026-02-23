package bot

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
)

func TestGetTicketConfigDev(t *testing.T) {
	cfg := getTicketConfig("dev")
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
	if c.PanelButtonLabel == "" {
		t.Error("dev PanelButtonLabel should be set")
	}
}

func TestGetTicketConfigProd(t *testing.T) {
	cfg := getTicketConfig("prod")
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

func TestGetTicketConfigUnknown(t *testing.T) {
	if getTicketConfig("staging") != nil {
		t.Error("expected nil for unknown env")
	}
	if getTicketConfig("") != nil {
		t.Error("expected nil for empty env")
	}
}

func TestTicketPanelEmbed(t *testing.T) {
	cfg := getTicketConfig("dev")[1013566342345019512]
	embed := ticketPanelEmbed(cfg)
	if embed.Description == "" {
		t.Error("embed description should not be empty")
	}
}

func TestTicketPanelButton(t *testing.T) {
	cfg := getTicketConfig("dev")[1013566342345019512]
	btn := ticketPanelButton(cfg)
	if btn.Label != "Open Ticket" {
		t.Errorf("button label = %q, want %q", btn.Label, "Open Ticket")
	}
	if btn.CustomID != "ticket_open" {
		t.Errorf("button custom ID = %q, want %q", btn.CustomID, "ticket_open")
	}
	if btn.Style != discord.ButtonStylePrimary {
		t.Errorf("button style = %d, want Primary", btn.Style)
	}
}

func TestPanelNeedsUpdateMatch(t *testing.T) {
	cfg := getTicketConfig("dev")[1013566342345019512]
	embed := ticketPanelEmbed(cfg)
	btn := ticketPanelButton(cfg)
	msg := discord.Message{
		Embeds: []discord.Embed{embed},
		Components: []discord.LayoutComponent{
			discord.NewActionRow(btn),
		},
	}
	b := &Bot{} // panelNeedsUpdate doesn't use Bot fields
	if b.panelNeedsUpdate(msg, cfg) {
		t.Error("expected no update needed for matching panel")
	}
}

func TestPanelNeedsUpdateMismatch(t *testing.T) {
	cfg := getTicketConfig("dev")[1013566342345019512]
	msg := discord.Message{
		Content: "stale content",
		Embeds:  []discord.Embed{ticketPanelEmbed(cfg)},
		Components: []discord.LayoutComponent{
			discord.NewActionRow(ticketPanelButton(cfg)),
		},
	}
	b := &Bot{}
	if !b.panelNeedsUpdate(msg, cfg) {
		t.Error("expected update needed when message has extra content")
	}
}

func TestPanelNeedsUpdateMissingEmbed(t *testing.T) {
	cfg := getTicketConfig("dev")[1013566342345019512]
	msg := discord.Message{
		Embeds: []discord.Embed{},
		Components: []discord.LayoutComponent{
			discord.NewActionRow(ticketPanelButton(cfg)),
		},
	}
	b := &Bot{}
	if !b.panelNeedsUpdate(msg, cfg) {
		t.Error("expected update needed when embed is missing")
	}
}
