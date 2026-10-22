package botutil

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

const (
	prodGuild    snowflake.ID = 726985544038612993
	devGuild     snowflake.ID = 1013566342345019512
	foreignGuild snowflake.ID = 555
)

// guildCaches returns gateway caches holding the given guilds.
func guildCaches(ids ...snowflake.ID) cache.Caches {
	caches := cache.New(cache.WithCaches(cache.FlagGuilds))
	for _, id := range ids {
		caches.AddGuild(discord.Guild{ID: id})
	}
	return caches
}

func TestGuildIDsSkipsGuildsNotAllowed(t *testing.T) {
	b := &BaseBot{Client: &bot.Client{Caches: guildCaches(prodGuild, foreignGuild, devGuild)}}

	got := b.GuildIDs()
	slices.Sort(got)
	want := []snowflake.ID{prodGuild, devGuild}
	if !slices.Equal(got, want) {
		t.Errorf("GuildIDs() = %v, want %v", got, want)
	}

	// The test override is returned as given.
	b.TestGuildIDsOverride = []snowflake.ID{foreignGuild}
	if got := b.GuildIDs(); !slices.Equal(got, []snowflake.ID{foreignGuild}) {
		t.Errorf("GuildIDs() with override = %v", got)
	}
}

// Commands are registered for GuildIDs(), so a guild the bot was added to but
// isn't allowed gets none.
func TestCommandsRegisterOnlyInAllowedGuilds(t *testing.T) {
	var mu sync.Mutex
	var registered []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		registered = append(registered, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	b := &BaseBot{Log: testutil.DiscardLogger(), Client: &bot.Client{
		ApplicationID: 42,
		Caches:        guildCaches(foreignGuild, devGuild),
		Rest:          rest.New(rest.NewClient("test", rest.WithURL(srv.URL))),
	}}
	if err := RegisterGuildCommands(b.Client, b.GuildIDs(), nil, b.Log); err != nil {
		t.Fatalf("RegisterGuildCommands: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{fmt.Sprintf("PUT /applications/42/guilds/%d/commands", devGuild)}
	if !slices.Equal(registered, want) {
		t.Errorf("requests = %v, want %v", registered, want)
	}
}

func TestIgnoredGuildWarnedOnce(t *testing.T) {
	var buf bytes.Buffer
	b := &BaseBot{Log: slog.New(slog.NewTextHandler(&buf, nil))}
	ready := &events.Ready{EventReady: gateway.EventReady{Guilds: []discord.UnavailableGuild{
		{ID: prodGuild}, {ID: devGuild}, {ID: foreignGuild},
	}}}

	checkWarned := func(want map[snowflake.ID]int) {
		t.Helper()
		logs := buf.String()
		for id, n := range want {
			if got := strings.Count(logs, fmt.Sprintf("guild_id=%d", id)); got != n {
				t.Errorf("guild %d warned %d times, want %d\n%s", id, got, n, logs)
			}
		}
	}

	// Guilds the bot is already in at startup arrive in Ready, not GuildJoin.
	b.OnReady(ready)
	b.OnReady(ready) // a new session sends Ready again
	checkWarned(map[snowflake.ID]int{foreignGuild: 1, prodGuild: 0, devGuild: 0})

	b.OnGuildJoin(&events.GuildJoin{GenericGuild: &events.GenericGuild{GuildID: foreignGuild}})
	b.OnGuildJoin(&events.GuildJoin{GenericGuild: &events.GenericGuild{GuildID: 777}})
	checkWarned(map[snowflake.ID]int{foreignGuild: 1, 777: 1, prodGuild: 0, devGuild: 0})
}

// fakeInteraction records the responses to an interaction.
type fakeInteraction struct {
	guildID *snowflake.ID
	sent    []discord.MessageCreate
}

func (f *fakeInteraction) GuildID() *snowflake.ID { return f.guildID }

func (f *fakeInteraction) CreateMessage(m discord.MessageCreate, _ ...rest.RequestOpt) error {
	f.sent = append(f.sent, m)
	return nil
}

func TestRejectGuildNotAllowed(t *testing.T) {
	b := &BaseBot{Log: testutil.DiscardLogger()}
	id := func(v snowflake.ID) *snowflake.ID { return &v }
	tests := []struct {
		name    string
		guildID *snowflake.ID
		reject  bool
	}{
		{"prod", id(prodGuild), false},
		{"dev", id(devGuild), false},
		{"not allowed", id(foreignGuild), true},
		{"DM", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &fakeInteraction{guildID: tt.guildID}
			if got := b.RejectGuildNotAllowed(e); got != tt.reject {
				t.Fatalf("RejectGuildNotAllowed = %v, want %v", got, tt.reject)
			}
			if !tt.reject {
				if len(e.sent) != 0 {
					t.Errorf("answered an allowed interaction: %+v", e.sent)
				}
				return
			}
			if len(e.sent) != 1 || e.sent[0].Flags != discord.MessageFlagEphemeral {
				t.Errorf("responses = %+v, want one ephemeral notice", e.sent)
			}
		})
	}
}
