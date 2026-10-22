package bot

import (
	"strings"
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
	cfgs := getSelfroleConfig()[1013566342345019512]
	label := selfroleLabel(cfgs, 1015493549430685706)
	if label != "BOTBROS" {
		t.Errorf("label = %q, want %q", label, "BOTBROS")
	}
}

func TestSelfrroleLabelNotFound(t *testing.T) {
	cfgs := getSelfroleConfig()[1013566342345019512]
	label := selfroleLabel(cfgs, 999)
	if label != "role" {
		t.Errorf("label = %q, want %q", label, "role")
	}
}

func TestSelfrroleLabelNilConfig(t *testing.T) {
	label := selfroleLabel(nil, 1015493549430685706)
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

func TestGetSelfroleConfig(t *testing.T) {
	cfg := getSelfroleConfig()
	if cfg == nil {
		t.Fatal("config is nil")
	}
	if len(cfg) != 2 {
		t.Fatalf("expected 2 guild entries, got %d", len(cfg))
	}
	cfgs, ok := cfg[1013566342345019512]
	if !ok {
		t.Fatal("missing dev guild")
	}
	if len(cfgs) != 1 {
		t.Errorf("expected 1 panel config for dev, got %d", len(cfgs))
	}
	cfgs, ok = cfg[726985544038612993]
	if !ok {
		t.Fatal("missing prod guild")
	}
	if len(cfgs) != 3 {
		t.Errorf("expected 3 panel configs for prod, got %d", len(cfgs))
	}
}

func newSelfroleBot(t *testing.T) (*Bot, *discordStub) {
	t.Helper()
	b, stub := newStubBot(t)
	b.selfroles = map[snowflake.ID][]selfroleConfig{10: {
		{ChannelID: 90, Message: "pronouns", Buttons: []selfroleButton{{Label: "They/them", RoleID: 80}}},
		{ChannelID: 90, Message: "helpers", Buttons: []selfroleButton{{Label: "Helper", RoleID: 81}}},
		{ChannelID: 90, Message: "opt out", GrantOnly: true, Buttons: []selfroleButton{{Label: "Opt Out", RoleID: 82}}},
	}}
	return b, stub
}

func TestSelfroleToggleDefersBeforeChangingRole(t *testing.T) {
	b, stub := newSelfroleBot(t)
	stub.on("PUT /guilds/10/members/30/roles/80", 204, "")

	e, replies := componentEvent(t, b.Client, "selfrole_80", "", "")
	b.handleSelfroleToggle(e, 80)

	typ, data := replies.only(t)
	if typ != discord.InteractionResponseTypeDeferredCreateMessage || !isEphemeral(data) {
		t.Errorf("first response = %v, want an ephemeral defer before the REST call", typ)
	}
	if got := stub.bodies("PUT /guilds/10/members/30/roles/80"); len(got) != 1 {
		t.Errorf("added the role %d times, want 1", len(got))
	}
	msgs := stub.followups(t, "tok")
	if len(msgs) != 1 || msgs[0].Content != "Added **They/them**" || !msgs[0].Flags.Has(discord.MessageFlagEphemeral) {
		t.Errorf("follow-ups = %+v, want an ephemeral confirmation", msgs)
	}
}

func TestSelfroleToggleRemovesHeldRole(t *testing.T) {
	b, stub := newSelfroleBot(t)
	stub.on("DELETE /guilds/10/members/30/roles/81", 204, "")

	e, _ := componentEvent(t, b.Client, "selfrole_81", `"81"`, "")
	b.handleSelfroleToggle(e, 81)

	if got := stub.bodies("DELETE /guilds/10/members/30/roles/81"); len(got) != 1 {
		t.Errorf("removed the role %d times, want 1", len(got))
	}
	if msgs := stub.followups(t, "tok"); len(msgs) != 1 || msgs[0].Content != "Removed **Helper**" {
		t.Errorf("follow-ups = %+v, want a removal confirmation", msgs)
	}
}

func TestSelfroleToggleStaleButton(t *testing.T) {
	b, stub := newSelfroleBot(t)

	e, replies := componentEvent(t, b.Client, "selfrole_999", "", "")
	b.handleSelfroleToggle(e, 999)

	_, data := replies.only(t)
	if !isEphemeral(data) || !strings.Contains(contentOf(data), "no longer available") {
		t.Errorf("response = %+v, want an ephemeral 'no longer available'", data)
	}
	if got := stub.routesCalled(); len(got) != 0 {
		t.Errorf("made REST calls for an unconfigured role: %v", got)
	}
}

func TestEnsureSelfrolePanelsReadsSharedChannelOnce(t *testing.T) {
	b, stub := newSelfroleBot(t)
	b.TestGuildIDsOverride = []snowflake.ID{10}
	stub.on("GET /channels/90/messages", 200, "[]")

	b.ensureSelfrolePanels()

	if got := stub.bodies("GET /channels/90/messages"); len(got) != 1 {
		t.Errorf("read the shared channel %d times, want 1", len(got))
	}
	if got := stub.bodies("POST /channels/90/messages"); len(got) != 3 {
		t.Errorf("posted %d panels, want 3", len(got))
	}
}
