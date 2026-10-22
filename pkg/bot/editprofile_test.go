package bot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/sprobot"
	"github.com/sadbox/sprobot/pkg/testutil"
)

const editUserID = "9"

type editResponse struct {
	kind discord.InteractionResponseType
	data discord.InteractionResponseData
}

func respondTo(responses chan<- editResponse) events.InteractionResponderFunc {
	return func(kind discord.InteractionResponseType, data discord.InteractionResponseData, _ ...rest.RequestOpt) error {
		responses <- editResponse{kind, data}
		return nil
	}
}

func editInteractionJSON(kind discord.InteractionType, data string) []byte {
	return []byte(fmt.Sprintf(`{"id":"1","application_id":"2","type":%d,"token":"tok","guild_id":"1",
		"channel":{"id":"500","type":0,"guild_id":"1","name":"general"},
		"user":{"id":"%s","username":"user"},"data":%s}`, kind, editUserID, data))
}

func newEditBot(t *testing.T) (*Bot, *hookedS3) {
	t.Helper()
	h, srv := newHookedS3(t)
	b := &Bot{BaseBot: &botutil.BaseBot{S3: testutil.NewTestS3Client(t, srv), Log: testutil.DiscardLogger()}}
	return b, h
}

func editCommand(t *testing.T, responses chan<- editResponse) *events.ApplicationCommandInteractionCreate {
	t.Helper()
	var i discord.ApplicationCommandInteraction
	if err := json.Unmarshal(editInteractionJSON(discord.InteractionTypeApplicationCommand, `{"id":"3","name":"editprofile","type":1}`), &i); err != nil {
		t.Fatalf("decoding interaction: %v", err)
	}
	return &events.ApplicationCommandInteractionCreate{ApplicationCommandInteraction: i, Respond: respondTo(responses)}
}

func TestEditShowsNoFormWhenProfileUnreadable(t *testing.T) {
	b, h := newEditBot(t)
	tmpl := sprobot.ProfileTemplate
	h.put(profileObject(tmpl, editUserID), `{"Machine":"saved"}`)
	h.setHook(failGets(profileObject(tmpl, editUserID)))

	responses := make(chan editResponse, 2)
	b.handleEdit(editCommand(t, responses), tmpl)

	r := <-responses
	msg, ok := r.data.(discord.MessageCreate)
	if r.kind != discord.InteractionResponseTypeCreateMessage || !ok || msg.Content != profileLoadFailed || msg.Flags != discord.MessageFlagEphemeral {
		t.Errorf("response = %v %+v, want an ephemeral %q", r.kind, r.data, profileLoadFailed)
	}
}

func TestEditShowsFormForNewProfile(t *testing.T) {
	b, _ := newEditBot(t)
	responses := make(chan editResponse, 2)
	b.handleEdit(editCommand(t, responses), sprobot.ProfileTemplate)

	if r := <-responses; r.kind != discord.InteractionResponseTypeModal {
		t.Errorf("response = %v %+v, want the edit form", r.kind, r.data)
	}
}

// followupAPI records the content of interaction follow-up messages.
type followupAPI struct {
	mu       sync.Mutex
	messages []string
}

func (f *followupAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/webhooks/2/tok") {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var msg struct{ Content string }
	_ = json.Unmarshal(body, &msg)
	f.mu.Lock()
	f.messages = append(f.messages, msg.Content)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"10","channel_id":"500","content":"","author":{"id":"2","username":"bot"}}`))
}

// If the saved profile can't be read, the submit must not save: it would
// drop the stored image.
func TestEditSubmitKeepsProfileWhenUnreadable(t *testing.T) {
	b, h := newEditBot(t)
	api := &followupAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	b.Client = &bot.Client{ApplicationID: 2, Rest: rest.New(rest.NewClient("test", rest.WithURL(srv.URL)))}

	tmpl := sprobot.ProfileTemplate
	path := profileObject(tmpl, editUserID)
	const saved = `{"Machine":"saved","Gear Picture":"https://example.com/setup.png"}`
	h.put(path, saved)
	h.setHook(failGets(path))

	var i discord.ModalSubmitInteraction
	if err := json.Unmarshal(editInteractionJSON(discord.InteractionTypeModalSubmit, `{"custom_id":"edit_profile","components":[]}`), &i); err != nil {
		t.Fatalf("decoding interaction: %v", err)
	}
	responses := make(chan editResponse, 2)
	b.handleEditModalSubmit(&events.ModalSubmitInteractionCreate{ModalSubmitInteraction: i, Respond: respondTo(responses)}, tmpl)

	if got, _ := h.object(path); got != saved {
		t.Errorf("stored profile = %q, want it untouched", got)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.messages) != 1 || api.messages[0] != profileLoadFailed {
		t.Errorf("follow-ups = %q, want only %q", api.messages, profileLoadFailed)
	}
}
