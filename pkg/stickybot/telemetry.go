package stickybot

import (
	"context"

	"github.com/disgoorg/snowflake/v2"
	"github.com/sadbox/svc/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
)

// stickybot's own metrics, besides botutil's, as Prometheus names them:
//
//	stickybot_stickies   active stickies in the guilds the bot is in; left
//	    out while one of them is unloaded
//	stickybot_reposts_total{outcome}   reposts posted (success) or that
//	    failed; a repost skipped (still in the buffer, or only bots since)
//	    is neither
const scope = "github.com/Espresso-Aficionados/sprobot/pkg/stickybot"

// retryFailedJob is the 5-minute retry of failed loads, saves and file
// deletions (sadbox_job_*{job_name="retry_failed"}); it fails while any is
// outstanding. The 30s heartbeat is botutil's gateway_ready.
var retryFailedJob = telemetry.NewJob("retry_failed")

// modalNames label modal submits (botutil.TraceModals).
var modalNames = []string{"sticky_config"}

var (
	outcomeKey     = attribute.Key("outcome")
	outcomeSuccess = outcomeKey.String("success")
	outcomeFailure = outcomeKey.String("failure")
)

var reposts = botutil.NewCounter("stickybot.reposts", "{repost}", "Sticky reposts, by outcome.")

func init() {
	reposts.Seed(outcomeSuccess)
	reposts.Seed(outcomeFailure)
}

// activeStickies counts the active stickies of the ready guilds. ok is false
// before a guild is ready, and while a ready guild's stickies aren't loaded.
// Caller holds b.mu.
func activeStickies(stickies map[snowflake.ID]map[snowflake.ID]*stickyMessage, ready map[snowflake.ID]bool) (n int, ok bool) {
	if len(ready) == 0 {
		return 0, false
	}
	for guildID := range ready {
		channels, loaded := stickies[guildID]
		if !loaded {
			return 0, false
		}
		for _, s := range channels {
			if s.Active {
				n++
			}
		}
	}
	return n, true
}

// registerStats registers the callback that reads stickybot_stickies at
// each export. Run calls it; the registration ends with the process.
func (b *Bot) registerStats() (metric.Registration, error) {
	m := otel.Meter(scope)
	gauge, err := m.Int64ObservableGauge("stickybot.stickies",
		metric.WithUnit("{sticky}"),
		metric.WithDescription("Active stickies."))
	if err != nil {
		return nil, err
	}
	return m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		b.mu.Lock()
		n, ok := activeStickies(b.stickies, b.readyGuilds)
		b.mu.Unlock()
		if ok {
			o.ObserveInt64(gauge, int64(n))
		}
		return nil
	}, gauge)
}
