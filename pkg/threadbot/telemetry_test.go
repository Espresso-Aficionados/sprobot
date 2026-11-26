package threadbot

import (
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/Espresso-Aficionados/sprobot/pkg/telemetrytest"
)

// Only the enabled reminders of guilds the bot is in count, and none while
// one of those guilds isn't loaded.
func TestEnabledReminders(t *testing.T) {
	reminders := map[snowflake.ID]map[snowflake.ID]*threadReminder{
		1: {10: {Enabled: true}, 11: {Enabled: false}},
		2: {20: {Enabled: true}}, // loaded, but the bot isn't in it
	}
	if n, ok := enabledReminders(reminders, map[snowflake.ID]bool{1: true}); !ok || n != 1 {
		t.Errorf("enabledReminders = %d, %v; want 1, true", n, ok)
	}
	if _, ok := enabledReminders(reminders, map[snowflake.ID]bool{1: true, 3: true}); ok {
		t.Error("counted while a ready guild's reminders aren't loaded")
	}
	if _, ok := enabledReminders(reminders, nil); ok {
		t.Error("counted before any guild was ready")
	}
}

// The gauge reads the bot's state at export.
func TestRemindersGauge(t *testing.T) {
	rec := telemetrytest.Install(t)
	b := &Bot{
		reminders:   map[snowflake.ID]map[snowflake.ID]*threadReminder{1: {10: {Enabled: true}, 11: {Enabled: true}}},
		readyGuilds: map[snowflake.ID]bool{1: true},
	}
	reg, err := b.registerStats()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Unregister() })
	points := rec.Points(t, "threadbot.reminders")
	if len(points) != 1 || points[0].Value != 2 || points[0].Attrs.Len() != 0 {
		t.Errorf("threadbot.reminders = %+v, want one unlabelled point at 2", points)
	}
	sent := map[string]float64{}
	for _, p := range rec.Points(t, "threadbot.reminders.sent") {
		sent[p.Get("action")] = p.Value
	}
	if _, ok := sent["posted"]; !ok || len(sent) != 2 {
		t.Errorf("threadbot.reminders.sent = %v, want posted and edited seeded", sent)
	}
}
