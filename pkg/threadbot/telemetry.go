package threadbot

import (
	"context"

	"github.com/disgoorg/snowflake/v2"
	"github.com/sadbox/svc/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
)

// threadbot's own metrics, besides botutil's, as Prometheus names them:
//
//	threadbot_reminders   enabled reminders in the guilds the bot is in;
//	    left out while one of them is unloaded
//	threadbot_reminders_sent_total{action}   reminders sent: posted anew,
//	    or edited in place (still in the buffer, or only bots since)
const scope = "github.com/Espresso-Aficionados/sprobot/pkg/threadbot"

// threadbot's 5-minute loops (sadbox_job_*{job_name}): the reminders' load
// retry and save, and the member counts' save. The 30s heartbeat is
// botutil's gateway_ready.
var (
	saveRemindersJob    = telemetry.NewJob("save_reminders")
	saveMemberCountsJob = telemetry.NewJob("save_member_counts")
)

var (
	actionKey    = attribute.Key("action")
	actionPosted = actionKey.String("posted")
	actionEdited = actionKey.String("edited")
)

var remindersSent = botutil.NewCounter("threadbot.reminders.sent", "{reminder}", "Thread reminders sent, by action: posted or edited.")

func init() {
	remindersSent.Seed(actionPosted)
	remindersSent.Seed(actionEdited)
}

// enabledReminders counts the enabled reminders of the ready guilds. ok is
// false before a guild is ready, and while a ready guild's reminders aren't
// loaded. Caller holds b.mu.
func enabledReminders(reminders map[snowflake.ID]map[snowflake.ID]*threadReminder, ready map[snowflake.ID]bool) (n int, ok bool) {
	if len(ready) == 0 {
		return 0, false
	}
	for guildID := range ready {
		channels, loaded := reminders[guildID]
		if !loaded {
			return 0, false
		}
		for _, r := range channels {
			if r.Enabled {
				n++
			}
		}
	}
	return n, true
}

// registerStats registers the callback that reads threadbot_reminders at
// each export. Run calls it; the registration ends with the process.
func (b *Bot) registerStats() (metric.Registration, error) {
	m := otel.Meter(scope)
	gauge, err := m.Int64ObservableGauge("threadbot.reminders",
		metric.WithUnit("{reminder}"),
		metric.WithDescription("Enabled thread reminders."))
	if err != nil {
		return nil, err
	}
	return m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		b.mu.Lock()
		n, ok := enabledReminders(b.reminders, b.readyGuilds)
		b.mu.Unlock()
		if ok {
			o.ObserveInt64(gauge, int64(n))
		}
		return nil
	}, gauge)
}
