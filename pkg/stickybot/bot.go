package stickybot

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

type Bot struct {
	*botutil.BaseBot
	mu   sync.Mutex
	stop chan struct{}
	// guild -> channel -> sticky. A guild is absent until its stickies have
	// loaded (see guildLoaded).
	stickies map[snowflake.ID]map[snowflake.ID]*stickyMessage
	pending  botutil.MessageStash // context-menu targets awaiting modal submit
	// Keys of removed stickies' files whose deletion failed; guarded by mu.
	undeleted []string
	// Guilds a GuildReady has said the bot is in; guarded by mu.
	readyGuilds map[snowflake.ID]bool

	saveMu  sync.Mutex            // serializes saveStickiesForGuild
	unsaved map[snowflake.ID]bool // guilds whose last save failed; guarded by saveMu
}

func New(token string) (*Bot, error) {
	base, err := botutil.NewBaseBot("STICKYBOT")
	if err != nil {
		return nil, err
	}

	b := &Bot{
		BaseBot:     base,
		stop:        make(chan struct{}),
		stickies:    make(map[snowflake.ID]map[snowflake.ID]*stickyMessage),
		readyGuilds: make(map[snowflake.ID]bool),
		unsaved:     make(map[snowflake.ID]bool),
	}

	client, err := disgo.New(token,
		// Handlers run off the gateway goroutine: stopping a repost loop
		// waits out a repost in flight, which must not stall other events or
		// heartbeat ACKs.
		bot.WithEventManagerConfigOpts(bot.WithAsyncEventsEnabled()),
		bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagGuilds)),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMessages,
			),
			gateway.WithPresenceOpts(gateway.WithCustomActivity(botutil.PrivacyPolicyStatus)),
			gateway.WithCloseHandler(b.OnGatewayClose),
		),
		bot.WithEventListenerFunc(b.OnReady),
		bot.WithEventListenerFunc(b.OnGuildJoin),
		bot.WithEventListenerFunc(b.onGuildReady),
		bot.WithEventListenerFunc(b.onCommand),
		bot.WithEventListenerFunc(b.onModal),
		bot.WithEventListenerFunc(b.onMessage),
		bot.WithEventListenerFunc(b.onChannelDelete),
		bot.WithEventListenerFunc(b.onThreadDelete),
	)
	if err != nil {
		return nil, err
	}

	b.Client = client
	return b, nil
}

func (b *Bot) Run() error {
	ctx := context.Background()

	// Before the gateway opens, so no command or event sees a guild whose
	// stickies aren't in memory yet.
	b.loadStickies()

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
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.retryFailed)

	b.Log.Info(fmt.Sprintf("Invite: https://discord.com/oauth2/authorize?client_id=%d&scope=bot%%20applications.commands&permissions=68608", b.Client.ApplicationID))
	shutdownErr := b.WaitForShutdown("Stickybot")
	b.shutdown()
	return shutdownErr
}

// shutdown stops the repost loops, waiting out reposts in flight, and retries
// any saves and file deletions that failed.
func (b *Bot) shutdown() {
	close(b.stop)
	b.stopAllStickyGoroutines()
	b.saveUnsavedStickies()
	b.retryFileDeletes()
}

func (b *Bot) onMessage(e *events.MessageCreate) {
	if e.Message.Author.Bot {
		return
	}
	if e.GuildID == nil {
		return
	}

	b.mu.Lock()
	s := b.getSticky(*e.GuildID, e.ChannelID)
	if s == nil || !s.Active {
		b.mu.Unlock()
		return
	}
	h := s.handle
	b.mu.Unlock()
	h.Signal()
}

// onGuildReady checks the guild's stickies for deleted channels. GuildReady
// fires for every guild the bot is in on each new gateway session; a guild
// whose stickies haven't loaded yet is checked by the load that succeeds (see
// loadGuildStickies).
func (b *Bot) onGuildReady(e *events.GuildReady) {
	if !botutil.GuildAllowed(e.GuildID) {
		return
	}
	b.mu.Lock()
	b.readyGuilds[e.GuildID] = true
	_, loaded := b.stickies[e.GuildID]
	b.mu.Unlock()
	if loaded {
		b.dropStickiesOfDeletedChannels(e.GuildID)
	}
}

// dropStickiesOfDeletedChannels drops guildID's stickies whose channel no
// longer exists. Discord never replays a channel delete, so this catches
// channels deleted while nothing was listening: the bot was down, its session
// was replaced rather than resumed, or the guild's stickies hadn't loaded. A
// sticky in a thread goes too when the thread's parent was deleted, since
// that deletes the thread.
func (b *Bot) dropStickiesOfDeletedChannels(guildID snowflake.ID) {
	b.mu.Lock()
	channelIDs := slices.Collect(maps.Keys(b.stickies[guildID]))
	b.mu.Unlock()
	for _, channelID := range channelIDs {
		// Only Unknown Channel means deleted: a channel the bot can't see is
		// Missing Access, and any other failure may pass.
		if _, err := b.Client.Rest.GetChannel(channelID); rest.IsJSONErrorCode(err, rest.JSONErrorCodeUnknownChannel) {
			b.dropDeletedChannelSticky(guildID, channelID)
		}
	}
}

func (b *Bot) onChannelDelete(e *events.GuildChannelDelete) {
	b.dropDeletedChannelSticky(e.GuildID, e.ChannelID)
}

func (b *Bot) onThreadDelete(e *events.ThreadDelete) {
	b.dropDeletedChannelSticky(e.GuildID, e.ThreadID)
}

// dropDeletedChannelSticky removes the sticky of a deleted channel or thread.
// Left alone, it would keep its repost loop and stay stored as active, to be
// started again on every boot.
func (b *Bot) dropDeletedChannelSticky(guildID, channelID snowflake.ID) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if !b.guildLoaded(ctx, guildID) {
		return
	}
	s := b.takeSticky(guildID, channelID)
	if s == nil {
		return
	}
	b.Log.Info("Removing sticky of deleted channel", "guild_id", guildID, "channel_id", channelID)
	// Saved before the slow retire, which a shutdown doesn't wait for.
	b.saveStickiesForGuild(guildID)
	// The message went with the channel; deleting it again fails harmlessly.
	b.retireSticky(s)
}
