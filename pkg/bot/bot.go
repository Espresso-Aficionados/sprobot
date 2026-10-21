package bot

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

type Bot struct {
	*botutil.BaseBot
	stop                 chan struct{}
	loops                sync.WaitGroup // the loops started by startLoop
	searchClient         *http.Client
	skipList             map[snowflake.ID]string // goroutine-confined to forumReminderLoop
	topPosters           *guildStateStore[guildPostCounts]
	posterRole           *guildStateStore[posterRoleState]
	tickets              *guildStateStore[ticketState]
	shortcuts            *guildStateStore[shortcutState]
	welcome              *guildStateStore[welcomeState]
	welcomeSentMu        sync.Mutex
	welcomeSent          map[snowflake.ID]time.Time
	cacheDir             string // where the caches below are persisted
	msgCache             *messageCache
	memberCache          *cappedGroupedCache[discord.Member]
	guildCache           *cappedCache[discord.Guild]
	channelCache         *cappedCache[discord.GuildChannel]
	roleCache            *cappedGroupedCache[discord.Role]
	emojiCache           *cappedGroupedCache[discord.Emoji]
	stickerCache         *cappedGroupedCache[discord.Sticker]
	voiceStateCache      *cappedGroupedCache[discord.VoiceState]
	stageInstanceCache   *cappedGroupedCache[discord.StageInstance]
	scheduledEventCache  *cappedGroupedCache[discord.GuildScheduledEvent]
	soundboardSoundCache *cappedGroupedCache[discord.SoundboardSound]
	starboard            *guildStateStore[starboardState]
	topPostersConfig     *guildStateStore[topPostersConfigState]
	renameLogs           *guildStateStore[renameLogState]
	templates            map[snowflake.ID][]sprobot.Template
	selfroles            map[snowflake.ID][]selfroleConfig
	ticketConfigs        map[snowflake.ID]ticketConfig
	autoRole             *guildStateStore[autoRoleState]
	eventLog             *guildStateStore[eventLogState]
	modLog               *guildStateStore[modLogState]
	modLogPending        botutil.MessageStash // context-menu targets awaiting the mod note modal
	modLogThreads        modLogThreadIndex    // user -> mod log thread, in memory only
	tempRoleConfig       *guildStateStore[tempRoleConfigState]
	tempRoles            *guildStateStore[tempRoleState]
	threadHelpConfig     map[snowflake.ID]threadHelpInfo
	profileNames         profileNames
	linkMigrations       sync.Map // guild ID -> running; temporary: remove with migratelinks.go
}

func New(token string) (*Bot, error) {
	base, err := botutil.NewBaseBot("SPROBOT")
	if err != nil {
		return nil, err
	}

	b := &Bot{
		BaseBot:          base,
		stop:             make(chan struct{}),
		searchClient:     &http.Client{Timeout: 30 * time.Second},
		skipList:         make(map[snowflake.ID]string),
		welcomeSent:      make(map[snowflake.ID]time.Time),
		templates:        sprobot.AllTemplates(),
		selfroles:        getSelfroleConfig(),
		ticketConfigs:    getTicketConfig(),
		cacheDir:         defaultCacheDir,
		threadHelpConfig: getThreadHelpConfig(),
	}
	b.initStateStores()

	b.initCaches()
	b.removeRetiredCacheFiles()
	b.loadCaches()

	client, err := disgo.New(token,
		bot.WithEventManagerConfigOpts(bot.WithAsyncEventsEnabled()),
		bot.WithCacheConfigOpts(b.cacheConfig()...),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMembers,
				gateway.IntentGuildModeration,
				gateway.IntentGuildMessages,
				gateway.IntentGuildMessageReactions,
				gateway.IntentMessageContent,
				gateway.IntentGuildExpressions,
				gateway.IntentGuildVoiceStates,
				gateway.IntentGuildScheduledEvents,
			),
			gateway.WithPresenceOpts(gateway.WithCustomActivity(botutil.PrivacyPolicyStatus)),
			gateway.WithCloseHandler(b.OnGatewayClose),
		),
		bot.WithEventListenerFunc(b.OnReady),
		bot.WithEventListenerFunc(b.OnGuildJoin),
		bot.WithEventListenerFunc(b.onGuildLeave),
		bot.WithEventListenerFunc(b.onCommand),
		bot.WithEventListenerFunc(b.onModal),
		bot.WithEventListenerFunc(b.onComponent),
		bot.WithEventListenerFunc(b.onAutocomplete),
		bot.WithEventListenerFunc(b.onMessage),
		bot.WithEventListenerFunc(b.onMemberJoin),
		bot.WithEventListenerFunc(b.onMessageDelete),
		bot.WithEventListenerFunc(b.onMessageUpdate),
		bot.WithEventListenerFunc(b.onMemberLeave),
		bot.WithEventListenerFunc(b.onMemberUpdate),
		bot.WithEventListenerFunc(b.onAuditLogEntry),
		bot.WithEventListenerFunc(b.onChannelCreate),
		bot.WithEventListenerFunc(b.onChannelUpdate),
		bot.WithEventListenerFunc(b.onThreadCreate),
		bot.WithEventListenerFunc(b.onThreadUpdate),
		bot.WithEventListenerFunc(b.onThreadDelete),
		bot.WithEventListenerFunc(b.onModLogThreadCreate),
		bot.WithEventListenerFunc(b.onModLogThreadUpdate),
		bot.WithEventListenerFunc(b.onModLogThreadDelete),
		bot.WithEventListenerFunc(b.onModLogReady),
		bot.WithEventListenerFunc(b.onRoleUpdate),
		bot.WithEventListenerFunc(b.onGuildUpdate),
		bot.WithEventListenerFunc(b.onEmojiCreate),
		bot.WithEventListenerFunc(b.onEmojiUpdate),
		bot.WithEventListenerFunc(b.onEmojiDelete),
		bot.WithEventListenerFunc(b.onStickerCreate),
		bot.WithEventListenerFunc(b.onStickerUpdate),
		bot.WithEventListenerFunc(b.onStickerDelete),
		bot.WithEventListenerFunc(b.onStageInstanceCreate),
		bot.WithEventListenerFunc(b.onStageInstanceUpdate),
		bot.WithEventListenerFunc(b.onStageInstanceDelete),
		bot.WithEventListenerFunc(b.onScheduledEventCreate),
		bot.WithEventListenerFunc(b.onScheduledEventUpdate),
		bot.WithEventListenerFunc(b.onScheduledEventDelete),
		bot.WithEventListenerFunc(b.onSoundboardSoundCreate),
		bot.WithEventListenerFunc(b.onSoundboardSoundUpdate),
		bot.WithEventListenerFunc(b.onSoundboardSoundDelete),
		bot.WithEventListenerFunc(b.onReactionAdd),
		bot.WithEventListenerFunc(b.onReactionRemove),
		bot.WithEventListenerFunc(b.onReactionRemoveAll),
		bot.WithEventListenerFunc(b.onReactionRemoveEmoji),
		bot.WithEventListenerFunc(b.onStarboardSourceDelete),
	)
	if err != nil {
		return nil, err
	}

	b.Client = client
	return b, nil
}

// initStateStores creates the per-guild state stores and their load hooks.
func (b *Bot) initStateStores() {
	s3, log := b.S3, b.Log
	b.topPosters = newGuildStateStore(s3, log, "topposters",
		func() *guildPostCounts {
			return &guildPostCounts{Counts: make(map[string]map[string]int), Usernames: make(map[string]string)}
		},
		func(gc *guildPostCounts) *sync.Mutex { return &gc.mu })
	b.posterRole = newGuildStateStore(s3, log, "posterroles",
		func() *posterRoleState {
			return &posterRoleState{Counts: make(map[string]int), Fetched: make(map[string]bool)}
		},
		func(st *posterRoleState) *sync.Mutex { return &st.mu })
	b.tickets = newGuildStateStore(s3, log, "tickets",
		func() *ticketState { return &ticketState{Counter: 1} },
		func(st *ticketState) *sync.Mutex { return &st.mu })
	b.shortcuts = newGuildStateStore(s3, log, "shortcuts",
		func() *shortcutState {
			return &shortcutState{Shortcuts: make(map[string]shortcutEntry), indices: make(map[string]int)}
		},
		func(st *shortcutState) *sync.Mutex { return &st.mu })
	b.welcome = newGuildStateStore(s3, log, "welcome",
		func() *welcomeState { return &welcomeState{} },
		func(st *welcomeState) *sync.Mutex { return &st.mu })
	b.starboard = newGuildStateStore(s3, log, "starboard",
		func() *starboardState { return &starboardState{Entries: make(map[snowflake.ID]starboardEntry)} },
		func(st *starboardState) *sync.Mutex { return &st.mu })
	b.renameLogs = newGuildStateStore(s3, log, "renamelogs",
		func() *renameLogState { return &renameLogState{} },
		func(st *renameLogState) *sync.Mutex { return &st.mu })
	b.topPostersConfig = newGuildStateStore(s3, log, "toppostersconfig",
		func() *topPostersConfigState { return &topPostersConfigState{} },
		func(st *topPostersConfigState) *sync.Mutex { return &st.mu })
	b.autoRole = newGuildStateStore(s3, log, "autorole",
		func() *autoRoleState { return &autoRoleState{} },
		func(st *autoRoleState) *sync.Mutex { return &st.mu })
	b.eventLog = newGuildStateStore(s3, log, "eventlog",
		func() *eventLogState { return &eventLogState{} },
		func(st *eventLogState) *sync.Mutex { return &st.mu })
	b.modLog = newGuildStateStore(s3, log, "modlog",
		func() *modLogState { return &modLogState{} },
		func(st *modLogState) *sync.Mutex { return &st.mu })
	b.tempRoleConfig = newGuildStateStore(s3, log, "temproleconfig",
		func() *tempRoleConfigState { return &tempRoleConfigState{} },
		func(st *tempRoleConfigState) *sync.Mutex { return &st.mu })
	b.tempRoles = newGuildStateStore(s3, log, "temproles",
		func() *tempRoleState { return &tempRoleState{} },
		func(st *tempRoleState) *sync.Mutex { return &st.mu })

	// Set up default-fallback hooks for features with hardcoded defaults.
	defaults := defaultEventLogConfig()
	b.eventLog.onMissing = func(guildID snowflake.ID, st *eventLogState) {
		if chID, ok := defaults[guildID]; ok {
			st.ChannelID = chID
		}
	}
	modDefaults := defaultModLogConfig()
	b.modLog.onMissing = func(guildID snowflake.ID, st *modLogState) {
		if chID, ok := modDefaults[guildID]; ok {
			st.ChannelID = chID
		}
	}
	autoRoleDefaults := defaultAutoRoleConfig()
	b.autoRole.onMissing = func(guildID snowflake.ID, st *autoRoleState) {
		if roleID, ok := autoRoleDefaults[guildID]; ok {
			st.RoleID = roleID
		}
	}
	tpDefaults := defaultTopPostersConfig()
	b.topPostersConfig.onMissing = func(guildID snowflake.ID, st *topPostersConfigState) {
		if roleID, ok := tpDefaults[guildID]; ok {
			st.FilteredRoles = append(st.FilteredRoles, roleID)
		}
	}
	b.topPostersConfig.postLoad = func(st *topPostersConfigState) {
		st.migrateLegacy()
	}

	// Set up post-load hooks for features that need nil-map initialization.
	b.shortcuts.postLoad = func(st *shortcutState) {
		if st.Shortcuts == nil {
			st.Shortcuts = make(map[string]shortcutEntry)
		}
		st.indices = make(map[string]int)
	}
	b.posterRole.postLoad = func(st *posterRoleState) {
		if st.Counts == nil {
			st.Counts = make(map[string]int)
		}
		if st.Fetched == nil {
			st.Fetched = make(map[string]bool)
		}
	}
	b.starboard.postLoad = func(st *starboardState) {
		if st.Entries == nil {
			st.Entries = make(map[snowflake.ID]starboardEntry)
		}
	}
	b.topPosters.postLoad = func(gc *guildPostCounts) {
		if gc.Counts == nil {
			gc.Counts = make(map[string]map[string]int)
		}
		if gc.Usernames == nil {
			gc.Usernames = make(map[string]string)
		}
	}
	b.tickets.postLoad = func(st *ticketState) {
		if st.Counter < 1 {
			st.Counter = 1
		}
	}

	for _, s := range b.stateStores() {
		s.setGuilds(b.servedGuilds)
	}
}

// servedGuilds returns the allowed guilds the bot is in: those in the gateway
// cache, plus those the gateway listed that are still loading or unavailable
// through an outage. The stores' periodic and shutdown saves cover only these.
func (b *Bot) servedGuilds() []snowflake.ID {
	guildIDs := b.GuildIDs()
	if b.Client == nil || b.TestGuildIDsOverride != nil {
		return guildIDs
	}
	for _, id := range slices.Concat(b.Client.Caches.UnreadyGuildIDs(), b.Client.Caches.UnavailableGuildIDs()) {
		if botutil.GuildAllowed(id) && !slices.Contains(guildIDs, id) {
			guildIDs = append(guildIDs, id)
		}
	}
	return guildIDs
}

// guildStateLoader is the part of a guildStateStore that doesn't depend on
// its state type.
type guildStateLoader interface {
	load(guildIDs []snowflake.ID)
	retryFailedLoads()
	setGuilds(guilds func() []snowflake.ID)
}

// stateStores lists every per-guild state store.
func (b *Bot) stateStores() []guildStateLoader {
	return []guildStateLoader{
		b.autoRole, b.eventLog, b.modLog, b.topPostersConfig, b.topPosters, b.posterRole, b.tickets,
		b.shortcuts, b.welcome, b.starboard, b.renameLogs, b.tempRoleConfig, b.tempRoles,
	}
}

// loadState loads every store for every allowed guild, the stores
// concurrently. It needs only S3, so Run calls it before opening the gateway:
// handlers start as soon as it opens, and one that found no state could save
// fresh state over the real one.
func (b *Bot) loadState() {
	var wg sync.WaitGroup
	for _, s := range b.stateStores() {
		wg.Go(func() { s.load(botutil.AllowedGuildIDs) })
	}
	wg.Wait()
}

// retryFailedLoads retries every failed load, the stores concurrently as in
// loadState, so a hung S3 holds up shutdown by about a storeS3Timeout per
// guild rather than per guild and store.
func (b *Bot) retryFailedLoads() {
	var wg sync.WaitGroup
	for _, s := range b.stateStores() {
		wg.Go(s.retryFailedLoads)
	}
	wg.Wait()
}

// saveState saves every per-guild state store.
func (b *Bot) saveState() {
	b.autoRole.save()
	b.eventLog.save()
	b.modLog.save()
	b.topPostersConfig.save()
	b.saveTopPosters()
	b.posterRole.save()
	b.tickets.save()
	b.shortcuts.save()
	b.welcome.save()
	b.saveStarboard()
	b.renameLogs.save()
	b.tempRoleConfig.save()
	b.tempRoles.save()
}

// startLoop runs fn every interval until b.stop is closed. stopLoops waits
// for it, so a run in progress finishes before the final saves.
func (b *Bot) startLoop(interval time.Duration, fn func()) {
	b.loops.Go(func() { botutil.RunSaveLoop(&b.Ready, interval, b.stop, fn) })
}

// stopLoops stops the loops started by startLoop and waits for them to return.
func (b *Bot) stopLoops() {
	close(b.stop)
	b.loops.Wait()
}

func (b *Bot) Run() error {
	ctx := context.Background()

	b.Log.Info(fmt.Sprintf("Invite: https://discord.com/oauth2/authorize?client_id=%d&scope=bot%%20applications.commands&permissions=361045773440", b.Client.ApplicationID))

	b.loadState()

	if err := b.OpenGateway(ctx); err != nil {
		return err
	}
	defer b.Client.Close(ctx)

	b.WaitForGuilds(30 * time.Second)

	b.ensureTicketPanels()
	b.ensureSelfrolePanels()
	if err := b.registerAllCommands(); err != nil {
		return fmt.Errorf("registering commands: %w", err)
	}
	go b.forumReminderLoop()
	// Not a startLoop: it saves nothing, and a hung ping would hold up shutdown.
	go botutil.RunSaveLoop(&b.Ready, 30*time.Second, b.stop, b.PingHealthcheck)
	b.startLoop(1*time.Minute, b.retryFailedLoads)
	b.startLoop(5*time.Minute, b.saveTopPosters)
	b.startLoop(5*time.Minute, b.posterRole.save)
	b.startLoop(5*time.Minute, b.shortcuts.save)
	b.startLoop(5*time.Minute, b.tickets.save)
	b.startLoop(5*time.Minute, b.welcome.save)
	b.startLoop(5*time.Minute, b.saveCaches) // also prunes expired messages
	b.startLoop(1*time.Hour, func() { pruneCorruptBackups(b.S3, b.Log, time.Now()) })
	b.startLoop(5*time.Minute, b.saveStarboard)
	b.startLoop(5*time.Minute, b.renameLogs.save)
	b.startLoop(5*time.Minute, b.tempRoles.save)
	b.startLoop(1*time.Minute, b.processTempRoleExpiries)

	shutdownErr := b.WaitForShutdown("Bot")
	b.stopLoops()
	b.saveState()
	b.saveCaches()
	return shutdownErr
}
