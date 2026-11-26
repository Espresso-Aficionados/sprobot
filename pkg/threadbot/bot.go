package threadbot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/envconfig"
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

func New(cfg envconfig.Bot) (*Bot, error) {
	base, err := botutil.NewBaseBot(cfg)
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

	client, err := disgo.New(cfg.Token,
		botutil.RESTClient(),
		botutil.CountEvents(),
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
		bot.WithGatewayConfigOpts(b.GatewayResumeOpts()...),
		bot.WithEventListenerFunc(b.OnReady),
		bot.WithEventListenerFunc(b.OnResumed),
		bot.WithEventListenerFunc(b.OnGuildJoin),
		bot.WithEventListenerFunc(botutil.TraceEvent("onGuildReady", b.onGuildReady)),
		bot.WithEventListenerFunc(botutil.TraceCommands(b.onCommand)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onMessage", b.onMessage)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onSessionReady", b.onSessionReady)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onThreadMemberAdd", b.onThreadMemberAdd)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onThreadMemberRemove", b.onThreadMemberRemove)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onThreadDelete", b.onThreadDelete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onChannelDelete", b.onChannelDelete)),
	)
	if err != nil {
		return nil, err
	}

	b.SetClient(client)
	return b, nil
}

func (b *Bot) Run() error {
	ctx := context.Background()
	// Startup's loads, gateway open and registrations, in one span.
	start, startSpan := botutil.StartSpan("startup")
	defer startSpan.End() // on a failed start; a second End does nothing

	b.Log.InfoContext(start, fmt.Sprintf("Invite: https://discord.com/oauth2/authorize?client_id=%d&scope=bot%%20applications.commands&permissions=68608", b.Client.ApplicationID))

	// State loads before the gateway opens, so no event or command runs
	// against a guild whose stored state isn't in place yet. Nothing has
	// been saved, and nothing will be: a newer build's state is left as it
	// is.
	if err := errors.Join(b.loadReminders(start), b.loadMemberCounts(start), b.loadHiddenThreads(start)); err != nil {
		return err
	}

	b.seedResumedSession(start)
	b.ServeHealth()
	if err := b.OpenGateway(start); err != nil {
		b.shutdown(start)
		return err
	}
	defer b.Client.Close(ctx)

	b.WaitForGuilds(30 * time.Second)

	if err := b.registerAllCommands(start); err != nil {
		b.shutdown(start)
		return fmt.Errorf("registering commands: %w", err)
	}
	startSpan.End()
	go botutil.RunSaveLoop(&b.Ready, 30*time.Second, b.stop, b.Heartbeat)
	go botutil.RunJobLoop(&b.Ready, 5*time.Minute, b.stop, saveRemindersJob, b.retryAndSaveReminders)
	go botutil.RunJobLoop(&b.Ready, 5*time.Minute, b.stop, saveMemberCountsJob, b.saveMemberCounts)

	shutdownErr := b.WaitForShutdown("Threadbot")
	stop, stopSpan := botutil.StartSpan("shutdown")
	defer stopSpan.End()
	// No event arrives after this, so what is saved below is all the
	// session's events did. The reposts that shutdown waits out need only
	// REST.
	handoff := b.HandOffGateway(stop, shutdownErr)
	saveErr := b.shutdown(stop)
	// A resume continues from exactly what was saved: only then.
	b.SaveHandoff(stop, handoff, saveErr == nil)
	return shutdownErr
}

// seedResumedSession sets up a resumed session as its READY and GuildReady
// events would have: a resume sends neither. The session's start is its
// own, so member counts fetched since then stay live; each guild's reminder
// loops start, as on its GuildReady. Safe to repeat: if Discord refuses the
// resume, those events follow and do it again.
func (b *Bot) seedResumedSession(ctx context.Context) {
	s := b.Resumed()
	if s == nil {
		return
	}
	b.mu.Lock()
	b.sessionStart = s.StartedAt
	b.mu.Unlock()
	for _, id := range s.Guilds {
		b.guildReady(ctx, id)
	}
}

// shutdown stops the repost loops, waiting out reposts in flight, and saves
// state, reporting whether a save failed (each failure is logged). It runs
// before the client closes, which the reposts still need.
func (b *Bot) shutdown(ctx context.Context) error {
	close(b.stop)
	b.stopAllReminderGoroutines()
	return errors.Join(b.saveAllReminders(ctx), b.saveMemberCounts(ctx))
}

func (b *Bot) onMessage(_ context.Context, e *events.MessageCreate) {
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
