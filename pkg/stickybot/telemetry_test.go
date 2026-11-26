package stickybot

import (
	"fmt"
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/telemetrytest"
)

// The sticky settings modal's custom ID is labelled by its prefix, not its
// channel and message IDs.
func TestModalLabel(t *testing.T) {
	customID := fmt.Sprintf("%s%d_%d", modalPrefix, 1013566342345019512, 1300000000000000000)
	if got := botutil.CustomIDLabel(customID, modalNames); got != "sticky_config" {
		t.Errorf("modal %q labelled %q, want sticky_config", customID, got)
	}
}

// Only the active stickies of guilds the bot is in count, and none while
// one of those guilds isn't loaded.
func TestActiveStickies(t *testing.T) {
	stickies := map[snowflake.ID]map[snowflake.ID]*stickyMessage{
		1: {10: {Active: true}, 11: {Active: false}, 12: {Active: true}},
		2: {20: {Active: true}}, // loaded, but the bot isn't in it
	}
	if n, ok := activeStickies(stickies, map[snowflake.ID]bool{1: true}); !ok || n != 2 {
		t.Errorf("activeStickies = %d, %v; want 2, true", n, ok)
	}
	if _, ok := activeStickies(stickies, map[snowflake.ID]bool{1: true, 3: true}); ok {
		t.Error("counted while a ready guild's stickies aren't loaded")
	}
	if _, ok := activeStickies(stickies, nil); ok {
		t.Error("counted before any guild was ready")
	}
}

// The gauge reads the bot's state at export.
func TestStickiesGauge(t *testing.T) {
	rec := telemetrytest.Install(t)
	b := &Bot{
		stickies:    map[snowflake.ID]map[snowflake.ID]*stickyMessage{1: {10: {Active: true}}},
		readyGuilds: map[snowflake.ID]bool{1: true},
	}
	reg, err := b.registerStats()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Unregister() })
	points := rec.Points(t, "stickybot.stickies")
	if len(points) != 1 || points[0].Value != 1 || points[0].Attrs.Len() != 0 {
		t.Errorf("stickybot.stickies = %+v, want one unlabelled point at 1", points)
	}
}

// A repost that posts is counted as a success.
func TestRepostCounted(t *testing.T) {
	rec := telemetrytest.Install(t)
	successes := func() float64 {
		for _, p := range rec.Points(t, "stickybot.reposts") {
			if p.Get("outcome") == "success" {
				return p.Value
			}
		}
		t.Fatal("no success series")
		return 0
	}
	before := successes()
	b, _ := newFakeBot(t, &fakeDiscord{})
	s := &stickyMessage{ChannelID: 500, GuildID: testGuildID, Content: "hello", Active: true, MaxIdleMins: 10, MsgThreshold: 1}
	if !b.repostSticky(t.Context(), s) {
		t.Fatal("repost failed")
	}
	if got := successes(); got != before+1 {
		t.Errorf("successful reposts = %v, want %v", got, before+1)
	}
}
