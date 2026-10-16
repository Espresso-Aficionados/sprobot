package bot

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestGetTicketConfig(t *testing.T) {
	cfg := getTicketConfig()
	if cfg == nil {
		t.Fatal("config is nil")
	}
	if len(cfg) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(cfg))
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
	c, ok = cfg[726985544038612993]
	if !ok {
		t.Fatal("missing prod guild entry")
	}
	if c.ChannelID == 0 {
		t.Error("prod ChannelID should be set")
	}
}

func TestTicketPanelEmbed(t *testing.T) {
	cfg := getTicketConfig()[1013566342345019512]
	embed := ticketPanelEmbed(cfg)
	if embed.Description == "" {
		t.Error("embed description should not be empty")
	}
	if embed.Description != cfg.PanelMessage {
		t.Error("embed should use PanelMessage field")
	}
}

func TestPanelMessageFallback(t *testing.T) {
	cfg := ticketConfig{StaffRoleID: 123}
	msg := cfg.panelMessage()
	if msg == "" {
		t.Error("fallback panel message should not be empty")
	}
	if !strings.Contains(msg, "<@&123>") {
		t.Error("fallback should mention staff role")
	}
}

func TestPanelMessageUsesField(t *testing.T) {
	cfg := ticketConfig{PanelMessage: "Custom panel text", StaffRoleID: 456}
	msg := cfg.panelMessage()
	if msg != "Custom panel text" {
		t.Errorf("panelMessage() = %q, want %q", msg, "Custom panel text")
	}
}

func TestTicketPanelButton(t *testing.T) {
	cfg := getTicketConfig()[1013566342345019512]
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
	cfg := getTicketConfig()[1013566342345019512]
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
	cfg := getTicketConfig()[1013566342345019512]
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
	cfg := getTicketConfig()[1013566342345019512]
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

func TestTicketOpenerFromMessage(t *testing.T) {
	cfg := getTicketConfig()[1013566342345019512]
	tests := []struct {
		name string
		msg  discord.Message
		want snowflake.ID
	}{
		{
			name: "intro embed",
			msg: discord.Message{
				Content: "||<@&1015493549430685706>||",
				Embeds:  []discord.Embed{{Description: fmt.Sprintf(cfg.TicketIntro, "<@123456789012345678>")}},
			},
			want: 123456789012345678,
		},
		{
			name: "nickname mention",
			msg:  discord.Message{Embeds: []discord.Embed{{Description: "Hello, <@!42>!"}}},
			want: 42,
		},
		{
			name: "role mention only",
			msg:  discord.Message{Embeds: []discord.Embed{{Description: "ping <@&42>"}}},
			want: 0,
		},
		{
			name: "generic cancel text",
			msg:  discord.Message{Embeds: []discord.Embed{{Description: "Use the button below when you're ready to close this ticket."}}},
			want: 0,
		},
		{
			name: "no embeds",
			msg:  discord.Message{},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ticketOpenerFromMessage(tt.msg); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTicketCloseCustomIDRoundTrip(t *testing.T) {
	for _, opener := range []snowflake.ID{0, 123456789012345678} {
		for _, base := range []string{"ticket_close_yes", "ticket_close_no"} {
			id := ticketCloseCustomID(base, opener)
			got, ok := parseTicketCloseCustomID(id, base)
			if !ok || got != opener {
				t.Errorf("parse(%q, %q) = %d, %v; want %d, true", id, base, got, ok, opener)
			}
		}
	}
}

func TestParseTicketCloseCustomIDRejects(t *testing.T) {
	for _, id := range []string{
		"ticket_close",
		"ticket_close_no",
		"ticket_close_yes:",
		"ticket_close_yes:abc",
		"ticket_close_yesterday",
	} {
		if _, ok := parseTicketCloseCustomID(id, "ticket_close_yes"); ok {
			t.Errorf("parse(%q, ticket_close_yes) matched, want no match", id)
		}
	}
}

func TestTicketClosedDescription(t *testing.T) {
	at := time.Unix(1760000000, 0)
	got := ticketClosedDescription(11, 22, at)
	want := "**Ticket closed** <t:1760000000:f>\nOpened by: <@11>\nClosed by: <@22>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	got = ticketClosedDescription(0, 22, at)
	want = "**Ticket closed** <t:1760000000:f>\nClosed by: <@22>"
	if got != want {
		t.Errorf("unknown opener: got %q, want %q", got, want)
	}
}
