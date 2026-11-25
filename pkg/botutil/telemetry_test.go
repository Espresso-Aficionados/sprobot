package botutil

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/sadbox/svc/telemetry"
	"go.opentelemetry.io/otel/trace"

	"github.com/Espresso-Aficionados/sprobot/pkg/telemetrytest"
)

// A loop's job succeeds when fn returns nil, fails when it returns an error
// or panics, and the loop survives the panic.
func TestRunJobLoop(t *testing.T) {
	job := telemetry.NewJob("test_run_job_loop")
	var ready atomic.Bool
	ready.Store(true)
	stop := make(chan struct{})
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		RunJobLoop(&ready, time.Millisecond, stop, job, func(context.Context) error {
			switch calls.Add(1) {
			case 1:
				return errors.New("S3 down")
			case 2:
				panic("boom")
			case 3:
				return nil
			}
			<-stop
			return nil
		})
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() < 4 {
		t.Fatalf("loop stopped after %d calls", calls.Load())
	}
	close(stop)
	<-done
	if job.LastSuccess().IsZero() {
		t.Error("a nil return recorded no success")
	}
}

func TestRunJobLoopFailureIsNoSuccess(t *testing.T) {
	job := telemetry.NewJob("test_run_job_loop_failure")
	var ready atomic.Bool
	ready.Store(true)
	stop := make(chan struct{})
	ran := make(chan struct{}, 1)
	go RunJobLoop(&ready, time.Millisecond, stop, job, func(context.Context) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return errors.New("save failed")
	})
	<-ran
	<-ran
	close(stop)
	if !job.LastSuccess().IsZero() {
		t.Error("a failing run recorded a success")
	}
}

func TestCustomIDLabel(t *testing.T) {
	names := []string{"ticket_close", "ticket_close_yes", "del_profile", "selfrole", "sconfig_set", "lc"}
	tests := map[string]string{
		"ticket_close":                 "ticket_close",
		"ticket_close_yes_12345678901": "ticket_close_yes",
		"del_profile_cs":               "del_profile",
		"selfrole_726985544038612993":  "selfrole",
		"sconfig_set_my shortcut name": "sconfig_set",
		"ticket_closed":                "other", // not "ticket_close" followed by "_"
		"726985544038612993":           "other",
		"ticket_close_yes:12345678901": "ticket_close_yes",
		"lc:rm:3":                      "lc",
		"":                             "other",
	}
	for id, want := range tests {
		if got := CustomIDLabel(id, names); got != want {
			t.Errorf("CustomIDLabel(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestCommandLabel(t *testing.T) {
	RegisterCommandNames([]discord.ApplicationCommandCreate{
		discord.SlashCommandCreate{Name: "test-topposters"},
		discord.MessageCommandCreate{Name: "Test Save message"},
	})
	for name, want := range map[string]string{
		"test-topposters":   "test-topposters",
		"Test Save message": "Test Save message",
		"never-registered":  "other",
	} {
		if got := commandLabel(name); got != want {
			t.Errorf("commandLabel(%q) = %q, want %q", name, got, want)
		}
	}
}

// An interaction is a span and a duration labelled by its type and a fixed
// name, never by the IDs in its custom ID.
func TestTraceModals(t *testing.T) {
	rec := telemetrytest.Install(t)
	ran := false
	h := TraceModals([]string{"test_modlog"}, func(context.Context, *events.ModalSubmitInteractionCreate) { ran = true })
	h(&events.ModalSubmitInteractionCreate{ModalSubmitInteraction: discord.ModalSubmitInteraction{
		Data: discord.ModalSubmitInteractionData{CustomID: "test_modlog_1013566342345019512_1300000000000000000"},
	}})
	if !ran {
		t.Fatal("handler not called")
	}

	found := false
	for _, p := range rec.Points(t, "sprobot.interaction.duration") {
		if p.Get("interaction.type") == "modal" && p.Get("interaction.name") == "test_modlog" {
			found = true
		}
	}
	if !found {
		t.Error("no duration recorded for modal test_modlog")
	}
	for _, s := range rec.Spans() {
		if s.Name != "interaction modal test_modlog" {
			continue
		}
		for _, kv := range s.Attributes {
			if strings.Contains(kv.Value.String(), "1013566342345019512") {
				t.Errorf("span attribute %s carries an ID", kv.Key)
			}
		}
		return
	}
	t.Error("no span for modal test_modlog")
}

// Each wrapper hands its handler the interaction span's context, so the
// handler's downstream calls are that span's children.
func TestTraceHandlerContextCarriesSpan(t *testing.T) {
	rec := telemetrytest.Install(t)
	tests := []struct {
		name string
		run  func(record func(context.Context))
	}{
		{"interaction command other", func(record func(context.Context)) {
			TraceCommands(func(ctx context.Context, _ *events.ApplicationCommandInteractionCreate) { record(ctx) })(
				&events.ApplicationCommandInteractionCreate{ApplicationCommandInteraction: discord.ApplicationCommandInteraction{
					Data: discord.SlashCommandInteractionData{},
				}})
		}},
		{"interaction autocomplete other", func(record func(context.Context)) {
			TraceAutocomplete(func(ctx context.Context, _ *events.AutocompleteInteractionCreate) { record(ctx) })(
				&events.AutocompleteInteractionCreate{})
		}},
		{"interaction component other", func(record func(context.Context)) {
			TraceComponents(nil, func(ctx context.Context, _ *events.ComponentInteractionCreate) { record(ctx) })(
				&events.ComponentInteractionCreate{ComponentInteraction: discord.ComponentInteraction{
					Data: discord.ButtonInteractionData{},
				}})
		}},
		{"interaction modal test_ctx", func(record func(context.Context)) {
			TraceModals([]string{"test_ctx"}, func(ctx context.Context, _ *events.ModalSubmitInteractionCreate) { record(ctx) })(
				&events.ModalSubmitInteractionCreate{ModalSubmitInteraction: discord.ModalSubmitInteraction{
					Data: discord.ModalSubmitInteractionData{CustomID: "test_ctx_1"},
				}})
		}},
		{"event MessageCreate", func(record func(context.Context)) {
			TraceEvent("onMessage", func(ctx context.Context, _ *events.MessageCreate) { record(ctx) })(&events.MessageCreate{})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got trace.SpanContext
			tt.run(func(ctx context.Context) { got = trace.SpanFromContext(ctx).SpanContext() })
			if !got.IsValid() {
				t.Fatal("handler's context carries no span")
			}
			for _, s := range rec.Spans() {
				if s.SpanContext.Equal(got) {
					if s.Name != tt.name {
						t.Errorf("handler's span is %q, want %q", s.Name, tt.name)
					}
					return
				}
			}
			t.Errorf("handler's span %s is not a recorded span", got.SpanID())
		})
	}
}

func TestEventName(t *testing.T) {
	if got := eventName(&events.MessageCreate{}); got != "MessageCreate" {
		t.Errorf("eventName = %q", got)
	}
}

// The bots' REST calls are recorded as calls to "discord".
func TestRESTClientIsDiscord(t *testing.T) {
	rec := telemetrytest.Install(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	client, err := disgo.New("MTIz.x.y", RESTClient(), bot.WithRestClientConfigOpts(rest.WithURL(srv.URL)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Rest.GetChannel(1); err == nil {
		t.Fatal("GetChannel of a 404 succeeded")
	}
	for _, p := range rec.Points(t, "http.client.request.duration") {
		if p.Get("peer.service") == "discord" {
			return
		}
	}
	t.Error("no client request recorded for peer discord")
}

// A dispatched event is counted by its type.
func TestCountEvents(t *testing.T) {
	rec := telemetrytest.Install(t)
	client, err := disgo.New("MTIz.x.y", CountEvents())
	if err != nil {
		t.Fatal(err)
	}
	client.EventManager.DispatchEvent(&events.GuildMemberLeave{})
	for _, p := range rec.Points(t, "sprobot.gateway.events") {
		if p.Get("event") == "GuildMemberLeave" && p.Value >= 1 {
			return
		}
	}
	t.Error("no count for event GuildMemberLeave")
}
