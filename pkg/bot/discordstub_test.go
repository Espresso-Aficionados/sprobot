package bot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/testutil"
)

// stubAppID is the application ID of the client newDiscordStub returns.
const stubAppID = 2

// discordStub is a fake Discord REST API for handler tests. It records every
// request and answers from routes set with on; anything else gets 200 {}.
type discordStub struct {
	mu       sync.Mutex
	requests []stubRequest
	routes   map[string]stubResponse
}

type stubRequest struct {
	route string // "METHOD /path", without the query string
	body  string
}

type stubResponse struct {
	status int
	body   string
}

// newDiscordStub returns a stub API and a client whose REST calls go to it.
// The client has a channel cache, for code that reads cached threads.
func newDiscordStub(t *testing.T) (*discordStub, *bot.Client) {
	t.Helper()
	s := &discordStub{routes: make(map[string]stubResponse)}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, &bot.Client{
		ApplicationID: stubAppID,
		Rest:          rest.New(rest.NewClient("test", rest.WithURL(srv.URL))),
		Caches:        cache.New(cache.WithCaches(cache.FlagChannels)),
	}
}

// newStubBot returns a Bot wired to a stub API and a fake S3.
func newStubBot(t *testing.T) (*Bot, *discordStub) {
	t.Helper()
	stub, client := newDiscordStub(t)
	s3srv := httptest.NewServer(testutil.NewFakeS3())
	t.Cleanup(s3srv.Close)
	b := &Bot{BaseBot: &botutil.BaseBot{
		Client: client,
		S3:     testutil.NewTestS3Client(t, s3srv),
		Log:    testutil.DiscardLogger(),
	}}
	return b, stub
}

// on answers route ("METHOD /path") with status and a JSON body.
func (s *discordStub) on(route string, status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[route] = stubResponse{status: status, body: body}
}

func (s *discordStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	route := r.Method + " " + r.URL.Path
	s.mu.Lock()
	s.requests = append(s.requests, stubRequest{route: route, body: string(body)})
	resp, ok := s.routes[route]
	s.mu.Unlock()
	if !ok {
		resp = stubResponse{status: http.StatusOK, body: "{}"}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.status)
	io.WriteString(w, resp.body)
}

// reset forgets the requests recorded so far.
func (s *discordStub) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

// routesCalled returns the route of every request so far, in order.
func (s *discordStub) routesCalled() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.requests {
		out = append(out, r.route)
	}
	return out
}

// bodies returns the bodies of the requests made to route, in order.
func (s *discordStub) bodies(route string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.requests {
		if r.route == route {
			out = append(out, r.body)
		}
	}
	return out
}

// followups returns the decoded follow-up messages sent for token.
func (s *discordStub) followups(t *testing.T, token string) []discord.MessageCreate {
	t.Helper()
	var out []discord.MessageCreate
	for _, body := range s.bodies("POST /webhooks/2/" + token) {
		var m discord.MessageCreate
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatalf("decoding follow-up %q: %v", body, err)
		}
		out = append(out, m)
	}
	return out
}

// discordError is a Discord JSON error body.
func discordError(code int, message string) string {
	b, _ := json.Marshal(map[string]any{"code": code, "message": message})
	return string(b)
}

// interactionReplies records the initial responses an event handler sends.
type interactionReplies struct {
	mu      sync.Mutex
	types   []discord.InteractionResponseType
	payload []discord.InteractionResponseData
}

func (r *interactionReplies) respond(typ discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.types = append(r.types, typ)
	r.payload = append(r.payload, data)
	return nil
}

// only returns the single response sent, failing the test otherwise.
func (r *interactionReplies) only(t *testing.T) (discord.InteractionResponseType, discord.InteractionResponseData) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.types) != 1 {
		t.Fatalf("sent %d interaction responses (%v), want 1", len(r.types), r.types)
	}
	return r.types[0], r.payload[0]
}

// interactionJSON is the shared part of a guild interaction payload: guild
// 10, channel 20, invoked by member 30 with the given roles, token "tok".
func interactionJSON(typ discord.InteractionType, roles string) string {
	return `"id":"1","application_id":"2","token":"tok","version":1,"type":` + jsonNumber(int(typ)) + `,` +
		`"guild_id":"10","channel":{"id":"20","type":0,"guild_id":"10","name":"general"},` +
		`"member":{"user":{"id":"30","username":"alice"},"roles":[` + roles + `]}`
}

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// componentEvent builds a button click on message (a JSON object) with
// customID, in guild 10 by member 30.
func componentEvent(t *testing.T, client *bot.Client, customID, roles, message string) (*events.ComponentInteractionCreate, *interactionReplies) {
	t.Helper()
	if message == "" {
		message = `{"id":"40","channel_id":"20","author":{"id":"2","username":"sprobot"}}`
	}
	raw := `{` + interactionJSON(discord.InteractionTypeComponent, roles) +
		`,"data":{"component_type":2,"custom_id":` + jsonString(customID) + `},"message":` + message + `}`
	var ci discord.ComponentInteraction
	if err := json.Unmarshal([]byte(raw), &ci); err != nil {
		t.Fatalf("building component interaction: %v", err)
	}
	replies := &interactionReplies{}
	return &events.ComponentInteractionCreate{
		GenericEvent:         events.NewGenericEvent(client, 0, 0),
		ComponentInteraction: ci,
		Respond:              replies.respond,
	}, replies
}

// modalEvent builds a modal submit with customID in guild 10 by member 30.
func modalEvent(t *testing.T, client *bot.Client, customID string) (*events.ModalSubmitInteractionCreate, *interactionReplies) {
	t.Helper()
	raw := `{` + interactionJSON(discord.InteractionTypeModalSubmit, "") +
		`,"data":{"custom_id":` + jsonString(customID) + `,"components":[]}}`
	var mi discord.ModalSubmitInteraction
	if err := json.Unmarshal([]byte(raw), &mi); err != nil {
		t.Fatalf("building modal interaction: %v", err)
	}
	replies := &interactionReplies{}
	return &events.ModalSubmitInteractionCreate{
		GenericEvent:           events.NewGenericEvent(client, 0, 0),
		ModalSubmitInteraction: mi,
		Respond:                replies.respond,
	}, replies
}

// commandEvent builds an application command interaction whose "data" is
// the given JSON object, in guild 10 by member 30.
func commandEvent(t *testing.T, client *bot.Client, data string) (*events.ApplicationCommandInteractionCreate, *interactionReplies) {
	t.Helper()
	raw := `{` + interactionJSON(discord.InteractionTypeApplicationCommand, "") + `,"data":` + data + `}`
	var ai discord.ApplicationCommandInteraction
	if err := json.Unmarshal([]byte(raw), &ai); err != nil {
		t.Fatalf("building command interaction: %v", err)
	}
	replies := &interactionReplies{}
	return &events.ApplicationCommandInteractionCreate{
		GenericEvent:                  events.NewGenericEvent(client, 0, 0),
		ApplicationCommandInteraction: ai,
		Respond:                       replies.respond,
	}, replies
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// isEphemeral reports whether an initial response (a message or a deferral)
// is ephemeral.
func isEphemeral(data discord.InteractionResponseData) bool {
	m, ok := data.(discord.MessageCreate)
	return ok && m.Flags.Has(discord.MessageFlagEphemeral)
}

// contentOf returns the text of an initial message response.
func contentOf(data discord.InteractionResponseData) string {
	if m, ok := data.(discord.MessageCreate); ok {
		return m.Content
	}
	return ""
}
