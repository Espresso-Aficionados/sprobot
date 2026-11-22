package botutil

import (
	"context"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/sadbox/svc/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Telemetry shared by the bots (svc/telemetry has the standard), as
// Prometheus names them:
//
//	sprobot_interaction_duration_seconds{interaction_type, interaction_name}
//	    each interaction's handler; type command, autocomplete, component or
//	    modal; name a registered command's name, a known custom-ID prefix,
//	    or "other"
//	sprobot_gateway_events_total{event}   gateway events by disgo event type
//	sadbox_job_*{job_name="gateway_ready"}   one success per 30s tick while
//	    the gateway is ready: the heartbeat's replacement
//
// plus http_client_* for peer_service discord, s3, fetch and heartbeat. No
// Discord ID, name or content goes into a label or a span attribute.
const scope = "github.com/Espresso-Aficionados/sprobot/pkg/botutil"

// gatewayReadyJob succeeds on every heartbeat tick that finds the gateway
// ready, whether or not a heartbeat URL is set.
var gatewayReadyJob = telemetry.NewJob("gateway_ready")

var (
	interactionType = attribute.Key("interaction.type")
	interactionName = attribute.Key("interaction.name")
	eventKey        = attribute.Key("event")
)

// interactionBuckets, in seconds: an answer is due within 3s; deferred
// handlers (a profile image upload, a thread fetch) run far longer.
var interactionBuckets = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5, 10, 30, 60, 300}

var instruments = sync.OnceValues(func() (metric.Float64Histogram, metric.Int64Counter) {
	m := otel.Meter(scope)
	d, err := m.Float64Histogram("sprobot.interaction.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Duration of an interaction's handler, by interaction type and command."),
		metric.WithExplicitBucketBoundaries(interactionBuckets...),
	)
	if err != nil {
		otel.Handle(err)
	}
	c, err := m.Int64Counter("sprobot.gateway.events",
		metric.WithUnit("{event}"),
		metric.WithDescription("Gateway events received, by disgo event type."),
	)
	if err != nil {
		otel.Handle(err)
	}
	return d, c
})

// RESTClient is disgo's default REST client (its 20s timeout), traced and
// timed as calls to "discord".
func RESTClient() bot.ConfigOpt {
	return bot.WithRestClientConfigOpts(rest.WithHTTPClient(
		telemetry.Client("discord", &http.Client{Timeout: 20 * time.Second})))
}

// CountEvents counts every event disgo dispatches by its type
// ("MessageCreate", "GuildMemberUpdate"): a fixed set, disgo's.
func CountEvents() bot.ConfigOpt {
	return bot.WithEventListenerFunc(func(e bot.Event) {
		_, counter := instruments()
		if counter != nil {
			counter.Add(context.Background(), 1, metric.WithAttributes(eventKey.String(eventName(e))))
		}
	})
}

func eventName(e bot.Event) string {
	t := reflect.TypeOf(e)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}

// registeredCommands are the command names this process registered
// (RegisterGuildCommands, RegisterCommandNames): the only command names a
// label may carry.
var registeredCommands sync.Map // string -> struct{}

// RegisterCommandNames makes commands' names labels for their interactions.
// RegisterGuildCommands calls it; a bot registering commands itself must.
func RegisterCommandNames(commands []discord.ApplicationCommandCreate) {
	for _, c := range commands {
		registeredCommands.Store(c.CommandName(), struct{}{})
	}
}

// commandLabel is name if this process registered it, else "other". Until
// registration (seconds after startup) every command is "other".
func commandLabel(name string) string {
	if _, ok := registeredCommands.Load(name); ok {
		return name
	}
	return "other"
}

// CustomIDLabel is the longest of names that customID is, or starts with
// followed by "_" or ":"; else "other". Custom IDs carry IDs and user text after
// their prefix, so only a listed prefix is ever a label.
func CustomIDLabel(customID string, names []string) string {
	best := ""
	for _, n := range names {
		rest, ok := strings.CutPrefix(customID, n)
		if ok && (rest == "" || rest[0] == '_' || rest[0] == ':') && len(n) > len(best) {
			best = n
		}
	}
	if best == "" {
		return "other"
	}
	return best
}

// traceInteraction runs handle as one interaction, in a span named
// "interaction <kind> <name>", and records its duration.
func traceInteraction(kind, name string, handle func()) {
	attrs := []attribute.KeyValue{interactionType.String(kind), interactionName.String(name)}
	_, span := otel.Tracer(scope).Start(context.Background(), "interaction "+kind+" "+name,
		trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attrs...))
	start := time.Now()
	defer func() {
		span.End()
		if d, _ := instruments(); d != nil {
			d.Record(context.Background(), time.Since(start).Seconds(), metric.WithAttributes(attrs...))
		}
	}()
	handle()
}

// TraceCommands instruments an application command listener, labelled by
// command name.
func TraceCommands(h func(*events.ApplicationCommandInteractionCreate)) func(*events.ApplicationCommandInteractionCreate) {
	return func(e *events.ApplicationCommandInteractionCreate) {
		traceInteraction("command", commandLabel(e.Data.CommandName()), func() { h(e) })
	}
}

// TraceAutocomplete instruments an autocomplete listener, labelled by
// command name.
func TraceAutocomplete(h func(*events.AutocompleteInteractionCreate)) func(*events.AutocompleteInteractionCreate) {
	return func(e *events.AutocompleteInteractionCreate) {
		traceInteraction("autocomplete", commandLabel(e.Data.CommandName), func() { h(e) })
	}
}

// TraceComponents instruments a component listener, labelled by the custom
// ID's prefix among names.
func TraceComponents(names []string, h func(*events.ComponentInteractionCreate)) func(*events.ComponentInteractionCreate) {
	return func(e *events.ComponentInteractionCreate) {
		traceInteraction("component", CustomIDLabel(e.Data.CustomID(), names), func() { h(e) })
	}
}

// TraceModals instruments a modal submit listener, labelled by the custom
// ID's prefix among names.
func TraceModals(names []string, h func(*events.ModalSubmitInteractionCreate)) func(*events.ModalSubmitInteractionCreate) {
	return func(e *events.ModalSubmitInteractionCreate) {
		traceInteraction("modal", CustomIDLabel(e.Data.CustomID, names), func() { h(e) })
	}
}

// StartTelemetry starts export for the bot called service (telemetry.Start)
// and returns the function that flushes it. Call it in main after
// -healthcheck, -check-state and envconfig: only the running bot exports.
// Without OTEL_EXPORTER_OTLP_ENDPOINT it does nothing, and a bad OTEL_*
// setting costs only the telemetry.
func StartTelemetry(service string) (shutdown func()) {
	shutdown, err := telemetry.Start(context.Background(), service)
	if err != nil {
		slog.Warn("Telemetry off", "error", err)
		return func() {}
	}
	return shutdown
}
