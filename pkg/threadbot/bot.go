package threadbot

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

type Bot struct {
	*botutil.BaseBot
	mu   sync.Mutex
	stop chan struct{}
	// A guild is in reminders and memberCounts only once its stored data has
	// loaded (see ensureRemindersLoaded), so nothing saves over data that
	// failed to load.
	reminders    map[snowflake.ID]map[snowflake.ID]*threadReminder // guild -> channel -> reminder
	memberCounts map[snowflake.ID]*memberCountCache                // guild -> cache
	sessionStart time.Time                                         // when the current gateway session began
	hidden       map[snowflake.ID]map[snowflake.ID]struct{}        // guild -> threads never promoted

	hiddenSaveMu sync.Mutex            // orders hidden-list loads and saves
	hiddenLoaded map[snowflake.ID]bool // guilds whose hidden list has loaded from S3

	// saveMu orders reminder and member-count saves: held from snapshot to
	// PUT, so an older snapshot never lands in S3 after a newer one.
	saveMu      sync.Mutex
	readyGuilds map[snowflake.ID]bool // guilds whose GuildReady has arrived: the bot is in them
}

func New(token string) (*Bot, error) {
	base, err := botutil.NewBaseBot("THREADBOT")
	if err != nil {
		return nil, err
	}

	b := &Bot{
		BaseBot:      base,
		stop:         make(chan struct{}),
		reminders:    make(map[snowflake.ID]map[snowflake.ID]*threadReminder),
		memberCounts: make(map[snowflake.ID]*memberCountCache),
		hidden:       make(map[snowflake.ID]map[snowflake.ID]struct{}),
	}

	client, err := disgo.New(token,
		// Handlers run off the gateway goroutine: stopping a repost loop
		// waits out a repost in flight, which must not stall other events or
		// heartbeat ACKs.
		bot.WithEventManagerConfigOpts(bot.WithAsyncEventsEnabled()),
		// Only guilds are cached: the member and thread-member events that
		// GuildMembers delivers are counted, never stored.
		bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagGuilds)),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMessages,
				// Privileged (approved): thread member add/remove events for
				// other users, used to keep member counts current between
				// REST refreshes. See docs/privileged-intents/threadbot.md.
				gateway.IntentGuildMembers,
			),
			gateway.WithPresenceOpts(gateway.WithCustomActivity(botutil.PrivacyPolicyStatus)),
			gateway.WithCloseHandler(b.OnGatewayClose),
		),
		bot.WithEventListenerFunc(b.OnReady),
		bot.WithEventListenerFunc(b.OnGuildJoin),
		bot.WithEventListenerFunc(b.onGuildReady),
		bot.WithEventListenerFunc(b.onCommand),
		bot.WithEventListenerFunc(b.onMessage),
		bot.WithEventListenerFunc(b.onSessionReady),
		bot.WithEventListenerFunc(b.onThreadMemberAdd),
		bot.WithEventListenerFunc(b.onThreadMemberRemove),
		bot.WithEventListenerFunc(b.onThreadDelete),
		bot.WithEventListenerFunc(b.onChannelDelete),
	)
	if err != nil {
		return nil, err
	}

	b.Client = client
	return b, nil
}

func (b *Bot) Run() error {
	ctx := context.Background()

	b.Log.Info(fmt.Sprintf("Invite: https://discord.com/oauth2/authorize?client_id=%d&scope=bot%%20applications.commands&permissions=68608", b.Client.ApplicationID))

	// State loads before the gateway opens, so no event or command runs
	// against a guild whose stored state isn't in place yet.
	b.loadReminders()
	b.loadMemberCounts()
	b.loadHiddenThreads()

	if err := b.OpenGateway(ctx); err != nil {
		b.shutdown()
		return err
	}
	defer b.Client.Close(ctx)

	b.WaitForGuilds(30 * time.Second)

	if err := b.registerAllCommands(); err != nil {
		b.shutdown()
		return fmt.Errorf("registering commands: %w", err)
	}
	go botutil.RunSaveLoop(&b.Ready, 30*time.Second, b.stop, b.PingHealthcheck)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, func() {
		b.loadReminders() // retries guilds whose load failed
		b.saveAllReminders()
	})
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveMemberCounts)

	shutdownErr := b.WaitForShutdown("Threadbot")
	b.shutdown()
	return shutdownErr
}

// shutdown stops the repost loops, waiting out reposts in flight, and saves
// state. It runs before the client closes, which the reposts still need.
func (b *Bot) shutdown() {
	close(b.stop)
	b.stopAllReminderGoroutines()
	b.saveAllReminders()
	b.saveMemberCounts()
}

func (b *Bot) onMessage(e *events.MessageCreate) {
	if e.Message.Author.Bot {
		return
	}
	if e.GuildID == nil {
		return
	}

	b.mu.Lock()
	r := b.reminders[*e.GuildID][e.ChannelID]
	if r == nil || !r.Enabled {
		b.mu.Unlock()
		return
	}
	h := r.handle
	b.mu.Unlock()
	h.Signal()
}
