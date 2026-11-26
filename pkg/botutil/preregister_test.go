package botutil

import (
	"testing"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/bot/handlers"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
)

// A guild that was out at the handoff comes back, on the resumed session,
// as disgo's GuildReady (which starts its loops), not GuildJoin: preRegister
// marks it unready as READY would. A guild never pre-registered is a join.
func TestPreRegisterMakesGuildCreateReady(t *testing.T) {
	var ready, joined []snowflake.ID
	client, err := disgo.New("MTIz.x.y",
		bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagGuilds)),
		bot.WithEventListenerFunc(func(e *events.GuildReady) { ready = append(ready, e.GuildID) }),
		bot.WithEventListenerFunc(func(e *events.GuildJoin) { joined = append(joined, e.GuildID) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	pending, other := AllowedGuildIDs[0], AllowedGuildIDs[1]
	preRegister(client.Caches, []snowflake.ID{pending})

	create := handlers.GetGatewayHandlers()[gateway.EventTypeGuildCreate]
	for _, id := range []snowflake.ID{pending, other} {
		create.HandleGatewayEvent(client, 1, 0, gateway.EventGuildCreate{
			GatewayGuild: discord.GatewayGuild{CacheGuild: discord.CacheGuild{Guild: discord.Guild{ID: id}}},
		})
	}
	if len(ready) != 1 || ready[0] != pending {
		t.Errorf("GuildReady for %v, want the pre-registered guild %v alone", ready, pending)
	}
	if len(joined) != 1 || joined[0] != other {
		t.Errorf("GuildJoin for %v, want %v (never pre-registered)", joined, other)
	}
}
