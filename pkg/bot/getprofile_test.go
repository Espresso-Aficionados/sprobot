package bot

import (
	"slices"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

// getprofileData is /getprofile with the name option set to member 31, whose
// nickname is Bobby.
const getprofileData = `{"id":"100","name":"getprofile","type":1,` +
	`"options":[{"name":"name","type":6,"value":"31"}],` +
	`"resolved":{"users":{"31":{"id":"31","username":"bob"}},"members":{"31":{"nick":"Bobby","roles":[]}}}}`

func TestGetProfileNotFoundIsPrivate(t *testing.T) {
	b, stub := newStubBot(t)
	tmpl := sprobot.ProfileTemplate

	e, replies := commandEvent(t, b.Client, getprofileData)
	b.handleGet(e, tmpl)

	if typ, data := replies.only(t); typ != discord.InteractionResponseTypeDeferredCreateMessage || isEphemeral(data) {
		t.Errorf("deferred with %v (ephemeral %v), want a public defer", typ, isEphemeral(data))
	}
	// The public placeholder goes first, or the follow-up would inherit it.
	want := []string{"DELETE /webhooks/2/tok/messages/@original", "POST /webhooks/2/tok"}
	if got := stub.routesCalled(); !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v (and no member lookup)", got, want)
	}
	msgs := stub.followups(t, "tok")
	if len(msgs) != 1 || !msgs[0].Flags.Has(discord.MessageFlagEphemeral) {
		t.Fatalf("follow-ups = %+v, want one ephemeral message", msgs)
	}
	if wantMsg := "Whoops! Unable to find a Coffee Setup profile for Bobby."; msgs[0].Content != wantMsg {
		t.Errorf("not-found message = %q, want %q", msgs[0].Content, wantMsg)
	}
}

func TestGetProfileShowsFoundProfilePublicly(t *testing.T) {
	b, stub, fake := newStubBotWithS3(t)
	tmpl := sprobot.ProfileTemplate
	fake.Objects["/test-bucket/"+sprobot.ProfileS3Path("10", tmpl.Name, "31")] = []byte(`{"Machine":"Lever"}`)

	e, _ := commandEvent(t, b.Client, getprofileData)
	b.handleGet(e, tmpl)

	if got := stub.bodies("DELETE /webhooks/2/tok/messages/@original"); len(got) != 0 {
		t.Error("deleted the deferred response for a found profile")
	}
	msgs := stub.followups(t, "tok")
	if len(msgs) != 1 || msgs[0].Flags.Has(discord.MessageFlagEphemeral) || len(msgs[0].Embeds) != 1 {
		t.Fatalf("follow-ups = %+v, want one public profile embed", msgs)
	}
	if title := msgs[0].Embeds[0].Title; !strings.HasSuffix(title, "for Bobby") {
		t.Errorf("embed title = %q, want the resolved nickname", title)
	}
}

func TestGetProfileMenuNames(t *testing.T) {
	tmpl := sprobot.ProfileTemplate
	tests := []struct {
		name       string
		data       string
		wantLookup bool
	}{
		{
			name: "user target is resolved",
			data: `{"id":"100","name":"Get Coffee Setup Profile","type":2,"target_id":"31",` +
				`"resolved":{"users":{"31":{"id":"31","username":"bob"}},"members":{"31":{"nick":"Bobby","roles":[]}}}}`,
		},
		{
			name: "message target needs a member lookup",
			data: `{"id":"100","name":"Get Coffee Setup Profile","type":3,"target_id":"40",` +
				`"resolved":{"messages":{"40":{"id":"40","channel_id":"20","author":{"id":"31","username":"bob"}}}}}`,
			wantLookup: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, stub := newStubBot(t)
			stub.on("GET /guilds/10/members/31", 200, `{"user":{"id":"31","username":"bob"},"nick":"Bobby","roles":[]}`)

			e, _ := commandEvent(t, b.Client, tt.data)
			b.handleGetMenu(e, tmpl)

			if got := len(stub.bodies("GET /guilds/10/members/31")) == 1; got != tt.wantLookup {
				t.Errorf("member lookup made = %v, want %v", got, tt.wantLookup)
			}
			msgs := stub.followups(t, "tok")
			if len(msgs) != 1 || !strings.HasSuffix(msgs[0].Content, "profile for Bobby.") || !msgs[0].Flags.Has(discord.MessageFlagEphemeral) {
				t.Errorf("follow-ups = %+v, want an ephemeral not-found naming Bobby", msgs)
			}
		})
	}
}
