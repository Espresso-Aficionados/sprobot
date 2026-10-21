package bot

import (
	"fmt"
	"slices"
	"strings"
	"sync"
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
	opened := time.Unix(1760000000, 0)
	closed := opened.Add(26*time.Hour + 30*time.Minute)
	got := ticketClosedDescription(11, 22, opened, closed)
	want := "**Ticket closed**\n" +
		"Opened by <@11> on <t:1760000000:f>\n" +
		"Closed by <@22> on <t:1760095400:f>\n" +
		"Open for 1d 2h"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	got = ticketClosedDescription(0, 22, opened, closed)
	want = "**Ticket closed**\n" +
		"Opened on <t:1760000000:f>\n" +
		"Closed by <@22> on <t:1760095400:f>\n" +
		"Open for 1d 2h"
	if got != want {
		t.Errorf("unknown opener: got %q, want %q", got, want)
	}
}

func TestFormatTicketDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-time.Minute, "less than a minute"},
		{0, "less than a minute"},
		{59 * time.Second, "less than a minute"},
		{time.Minute, "1m"},
		{45*time.Minute + 30*time.Second, "45m"},
		{time.Hour, "1h"},
		{2*time.Hour + 15*time.Minute, "2h 15m"},
		{24 * time.Hour, "1d"},
		{3*24*time.Hour + 5*time.Minute, "3d"},
		{50 * time.Hour, "2d 2h"},
		{40 * 24 * time.Hour, "40d"},
	}
	for _, tt := range tests {
		if got := formatTicketDuration(tt.d); got != tt.want {
			t.Errorf("formatTicketDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestTicketOpenGuard(t *testing.T) {
	st := &ticketState{}
	now := time.Now()
	const alice, bob = snowflake.ID(1), snowflake.ID(2)

	if ok, _ := st.beginOpen(alice, now); !ok {
		t.Fatal("first open refused")
	}
	if ok, recent := st.beginOpen(alice, now); ok || recent != 0 {
		t.Errorf("click during an open = %v, %d; want refused with no ticket yet", ok, recent)
	}
	if ok, _ := st.beginOpen(bob, now); !ok {
		t.Error("another user's open was refused")
	}

	st.endOpen(alice, 99, now)
	if ok, recent := st.beginOpen(alice, now.Add(ticketOpenCooldown-time.Second)); ok || recent != 99 {
		t.Errorf("click within the cooldown = %v, %d; want refused pointing at ticket 99", ok, recent)
	}
	if ok, _ := st.beginOpen(alice, now.Add(ticketOpenCooldown)); !ok {
		t.Error("open after the cooldown refused")
	}

	// A failed open can be retried straight away.
	st.endOpen(bob, 0, now)
	if ok, _ := st.beginOpen(bob, now); !ok {
		t.Error("retry after a failed open refused")
	}
}

// newTicketBot returns a bot with tickets configured in guild 10: panel
// channel 50, staff role 60. Opening a ticket creates thread 70.
func newTicketBot(t *testing.T) (*Bot, *discordStub) {
	t.Helper()
	b, stub := newStubBot(t)
	b.ticketConfigs = map[snowflake.ID]ticketConfig{10: {
		ChannelID:        50,
		StaffRoleID:      60,
		TicketIntro:      "Hello, %s!",
		CloseButtonLabel: "Close Ticket",
	}}
	b.tickets = newGuildStateStore(b.S3, b.Log, "tickets",
		func() *ticketState { return &ticketState{Counter: 1} },
		func(st *ticketState) *sync.Mutex { return &st.mu })
	b.tickets.set(10, &ticketState{Counter: 1})
	stub.on("POST /channels/50/threads", 201, `{"id":"70","type":12,"guild_id":"10","parent_id":"50","name":"ticket-1","thread_metadata":{}}`)
	return b, stub
}

func TestTicketOpenDeletesThreadTheUserCannotJoin(t *testing.T) {
	b, stub := newTicketBot(t)
	stub.on("PUT /channels/70/thread-members/30", 403, discordError(50001, "Missing Access"))

	e, replies := componentEvent(t, b.Client, "ticket_open", `"60"`, "")
	b.handleTicketOpen(e)

	if _, data := replies.only(t); !isEphemeral(data) {
		t.Error("open was not deferred ephemerally")
	}
	if got := stub.bodies("DELETE /channels/70"); len(got) != 1 {
		t.Errorf("deleted the unusable thread %d times, want 1", len(got))
	}
	if got := stub.bodies("POST /channels/70/messages"); len(got) != 0 {
		t.Error("posted the intro (and pinged staff) in a thread the user can't see")
	}
	msgs := stub.followups(t, "tok")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "went wrong") || !msgs[0].Flags.Has(discord.MessageFlagEphemeral) {
		t.Errorf("follow-ups = %+v, want one ephemeral failure notice", msgs)
	}

	// The failed open doesn't block a retry.
	stub.on("PUT /channels/70/thread-members/30", 204, "")
	e, _ = componentEvent(t, b.Client, "ticket_open", `"60"`, "")
	b.handleTicketOpen(e)
	if got := stub.bodies("POST /channels/50/threads"); len(got) != 2 {
		t.Errorf("created %d threads, want a retry to create a second", len(got))
	}
}

func TestTicketOpenSecondClickOpensNoSecondTicket(t *testing.T) {
	b, stub := newTicketBot(t)
	stub.on("PUT /channels/70/thread-members/30", 204, "")

	for range 2 {
		e, _ := componentEvent(t, b.Client, "ticket_open", `"60"`, "")
		b.handleTicketOpen(e)
	}

	if got := stub.bodies("POST /channels/50/threads"); len(got) != 1 {
		t.Errorf("created %d ticket threads, want 1", len(got))
	}
	if got := stub.bodies("POST /channels/70/messages"); len(got) != 1 {
		t.Errorf("pinged staff %d times, want 1", len(got))
	}
	msgs := stub.followups(t, "tok")
	if len(msgs) != 2 || !strings.Contains(msgs[1].Content, "<#70>") || !msgs[1].Flags.Has(discord.MessageFlagEphemeral) {
		t.Errorf("follow-ups = %+v, want the second click pointed at the ticket", msgs)
	}
}

func TestTicketOpenAnswersWhenNotConfigured(t *testing.T) {
	b, stub := newTicketBot(t)
	b.ticketConfigs = nil

	e, _ := componentEvent(t, b.Client, "ticket_open", "", "")
	b.handleTicketOpen(e)

	msgs := stub.followups(t, "tok")
	if len(msgs) != 1 || !msgs[0].Flags.Has(discord.MessageFlagEphemeral) {
		t.Errorf("follow-ups = %+v, want one ephemeral answer instead of leaving the user thinking", msgs)
	}
}

func TestTicketCloseLocksBeforeSayingClosed(t *testing.T) {
	b, stub := newTicketBot(t)

	e, replies := componentEvent(t, b.Client, "ticket_close_yes:30", `"60"`, "")
	b.handleTicketClose(e, 30)

	if typ, _ := replies.only(t); typ != discord.InteractionResponseTypeDeferredUpdateMessage {
		t.Errorf("acknowledged with %v, want a deferred update", typ)
	}
	want := []string{"PATCH /channels/20", "PATCH /webhooks/2/tok/messages/@original", "PATCH /channels/20"}
	if got := stub.routesCalled(); !slices.Equal(got, want) {
		t.Errorf("calls = %v, want lock, edit, archive: %v", got, want)
	}
	if bodies := stub.bodies("PATCH /channels/20"); len(bodies) != 2 || !strings.Contains(bodies[0], `"locked":true`) || !strings.Contains(bodies[1], `"archived":true`) {
		t.Errorf("thread updates = %v, want lock then archive", bodies)
	}
}

func TestTicketCloseReportsLockFailure(t *testing.T) {
	b, stub := newTicketBot(t)
	stub.on("PATCH /channels/20", 403, discordError(50013, "Missing Permissions"))

	e, _ := componentEvent(t, b.Client, "ticket_close_yes:30", `"60"`, "")
	b.handleTicketClose(e, 30)

	if got := stub.bodies("PATCH /webhooks/2/tok/messages/@original"); len(got) != 0 {
		t.Errorf("marked the ticket closed although locking failed: %v", got)
	}
	msgs := stub.followups(t, "tok")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "Couldn't close") || !msgs[0].Flags.Has(discord.MessageFlagEphemeral) {
		t.Errorf("follow-ups = %+v, want an ephemeral failure notice", msgs)
	}
}
