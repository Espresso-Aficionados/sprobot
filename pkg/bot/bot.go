package bot

import (
	"context"
	"errors"
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
	"github.com/sadbox/svc/telemetry"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/envconfig"
	"github.com/Espresso-Aficionados/sprobot/pkg/sprobot"
	"github.com/Espresso-Aficionados/sprobot/pkg/statever"
)

type Bot struct {
	*botutil.BaseBot
	stop                 chan struct{}
	loops                sync.WaitGroup // the loops started by startLoop
	searchClient         *http.Client
	forumReminders       forumReminderState
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
	guildCache           *cappedCache[discord.CacheGuild]
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

func New(cfg envconfig.Bot) (*Bot, error) {
	base, err := botutil.NewBaseBot(cfg)
	if err != nil {
		return nil, err
	}

	b := &Bot{
		BaseBot:          base,
		stop:             make(chan struct{}),
		searchClient:     telemetry.Client("discord", &http.Client{Timeout: 30 * time.Second}),
		welcomeSent:      make(map[snowflake.ID]time.Time),
		templates:        sprobot.AllTemplates(),
		selfroles:        getSelfroleConfig(),
		ticketConfigs:    getTicketConfig(),
		cacheDir:         defaultCacheDir,
		threadHelpConfig: getThreadHelpConfig(),
	}
	b.initStateStores()

	b.initCaches()
	// Before anything in the cache dir is read, removed or rewritten.
	if _, _, err := cacheDirVersion(b.cacheDir); statever.IsNewer(err) {
		return nil, err
	} else if err != nil {
		b.Log.Error("Failed to read the cache dir's version; the next save rewrites it", "error", err)
	}
	b.removeRetiredCacheFiles()
	b.loadCaches()

	client, err := disgo.New(cfg.Token,
		botutil.RESTClient(),
		botutil.CountEvents(),
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
		bot.WithEventListenerFunc(botutil.TraceEvent("onGuildLeave", b.onGuildLeave)),
		bot.WithEventListenerFunc(botutil.TraceCommands(b.onCommand)),
		bot.WithEventListenerFunc(botutil.TraceModals(modalNames, b.onModal)),
		bot.WithEventListenerFunc(botutil.TraceComponents(componentNames, b.onComponent)),
		bot.WithEventListenerFunc(botutil.TraceAutocomplete(b.onAutocomplete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onMessage", b.onMessage)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onMemberJoin", b.onMemberJoin)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onMessageDelete", b.onMessageDelete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onMessageUpdate", b.onMessageUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onMemberLeave", b.onMemberLeave)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onMemberUpdate", b.onMemberUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onAuditLogEntry", b.onAuditLogEntry)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onChannelCreate", b.onChannelCreate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onChannelUpdate", b.onChannelUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onThreadCreate", b.onThreadCreate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onForumThreadCreate", b.onForumThreadCreate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onForumReminderReady", b.onForumReminderReady)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onForumThreadMessage", b.onForumThreadMessage)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onThreadUpdate", b.onThreadUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onThreadDelete", b.onThreadDelete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onModLogThreadCreate", b.onModLogThreadCreate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onModLogThreadUpdate", b.onModLogThreadUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onModLogThreadDelete", b.onModLogThreadDelete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onModLogReady", b.onModLogReady)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onRoleUpdate", b.onRoleUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onGuildUpdate", b.onGuildUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onEmojiCreate", b.onEmojiCreate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onEmojiUpdate", b.onEmojiUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onEmojiDelete", b.onEmojiDelete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onStickerCreate", b.onStickerCreate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onStickerUpdate", b.onStickerUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onStickerDelete", b.onStickerDelete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onStageInstanceCreate", b.onStageInstanceCreate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onStageInstanceUpdate", b.onStageInstanceUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onStageInstanceDelete", b.onStageInstanceDelete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onScheduledEventCreate", b.onScheduledEventCreate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onScheduledEventUpdate", b.onScheduledEventUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onScheduledEventDelete", b.onScheduledEventDelete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onSoundboardSoundCreate", b.onSoundboardSoundCreate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onSoundboardSoundUpdate", b.onSoundboardSoundUpdate)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onSoundboardSoundDelete", b.onSoundboardSoundDelete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onReactionAdd", b.onReactionAdd)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onReactionRemove", b.onReactionRemove)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onReactionRemoveAll", b.onReactionRemoveAll)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onReactionRemoveEmoji", b.onReactionRemoveEmoji)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onStarboardSourceDelete", b.onStarboardSourceDelete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onStarboardChannelDelete", b.onStarboardChannelDelete)),
		bot.WithEventListenerFunc(botutil.TraceEvent("onStarboardThreadDelete", b.onStarboardThreadDelete)),
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
		backfillPostChannels(st)
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
	load(ctx context.Context, guildIDs []snowflake.ID) error
	retryFailedLoads(ctx context.Context) error
	prefix() string
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
// fresh state over the real one. It returns the refusals of state a newer
// build wrote.
func (b *Bot) loadState(ctx context.Context) error {
	stores := b.stateStores()
	refused := make([]error, len(stores))
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Go(func() { refused[i] = s.load(ctx, botutil.AllowedGuildIDs) })
	}
	wg.Wait()
	return errors.Join(refused...)
}

// retryFailedLoads retries every failed load, the stores concurrently as in
// loadState, so a hung S3 holds up shutdown by about a storeS3Timeout per
// guild rather than per guild and store.
func (b *Bot) retryFailedLoads(ctx context.Context) error {
	stores := b.stateStores()
	errs := make([]error, len(stores))
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Go(func() { errs[i] = s.retryFailedLoads(ctx) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// saveState saves every per-guild state store. Each logs its own failures.
func (b *Bot) saveState(ctx context.Context) {
	_ = errors.Join(
		b.autoRole.save(ctx),
		b.eventLog.save(ctx),
		b.modLog.save(ctx),
		b.topPostersConfig.save(ctx),
		b.saveTopPosters(ctx),
		b.posterRole.save(ctx),
		b.tickets.save(ctx),
		b.shortcuts.save(ctx),
		b.welcome.save(ctx),
		b.saveStarboard(ctx),
		b.renameLogs.save(ctx),
		b.tempRoleConfig.save(ctx),
		b.tempRoles.save(ctx),
	)
}

// startLoop runs fn every interval, each run a run of job, until b.stop is
// closed. stopLoops waits for it, so a run in progress finishes before the
// final saves.
func (b *Bot) startLoop(interval time.Duration, job *telemetry.Job, fn func(context.Context) error) {
	b.loops.Go(func() {
		botutil.RunJobLoop(&b.Ready, interval, b.stop, job, fn)
	})
}

// stopLoops stops the loops started by startLoop and waits for them to return.
func (b *Bot) stopLoops() {
	close(b.stop)
	b.loops.Wait()
}

func (b *Bot) Run() error {
	ctx := context.Background()
	// Startup's loads, gateway open and registrations, in one span. Its
	// context reaches nothing that runs past startup but the owner listing.
	start, startSpan := botutil.StartSpan("startup")
	defer startSpan.End() // on a failed start; a second End does nothing

	b.Log.InfoContext(start, fmt.Sprintf("Invite: https://discord.com/oauth2/authorize?client_id=%d&scope=bot%%20applications.commands&permissions=361045773440", b.Client.ApplicationID))

	// Nothing has been saved yet, and nothing will be: a newer build's state
	// is left as it is.
	if err := b.loadState(start); err != nil {
		return err
	}
	// Lists stored profiles from S3 only; name updates probe authors until it
	// finishes.
	go b.loadProfileOwners(start, botutil.AllowedGuildIDs)

	b.ServeHealth()
	if err := b.OpenGateway(start); err != nil {
		return err
	}
	defer b.Client.Close(ctx)

	b.WaitForGuilds(30 * time.Second)

	b.ensureTicketPanels(start)
	b.ensureSelfrolePanels(start)
	if err := b.registerAllCommands(start); err != nil {
		return fmt.Errorf("registering commands: %w", err)
	}
	startSpan.End()
	go b.forumReminderLoop()
	// Not a startLoop: it saves nothing, and a hung ping would hold up shutdown.
	go botutil.RunSaveLoop(&b.Ready, 30*time.Second, b.stop, b.Heartbeat)
	b.startLoop(1*time.Minute, retryFailedLoadsJob, b.retryFailedLoads)
	b.startLoop(5*time.Minute, saveTopPostersJob, b.saveTopPosters)
	b.startLoop(5*time.Minute, savePosterRoleJob, b.posterRole.save)
	b.startLoop(5*time.Minute, saveShortcutsJob, b.shortcuts.save)
	b.startLoop(5*time.Minute, saveTicketsJob, b.tickets.save)
	b.startLoop(5*time.Minute, saveWelcomeJob, b.welcome.save)
	b.startLoop(5*time.Minute, saveCachesJob, func(context.Context) error { return b.saveCaches() }) // local files only; also prunes expired messages
	b.startLoop(1*time.Hour, pruneCorruptBackupsJob, func(ctx context.Context) error { return pruneCorruptBackups(ctx, b.S3, b.Log, time.Now()) })
	b.startLoop(5*time.Minute, saveStarboardJob, b.saveStarboard)
	b.startLoop(5*time.Minute, retryStarboardDeletesJob, b.retryStarboardDeletes)
	b.startLoop(5*time.Minute, saveRenameLogsJob, b.renameLogs.save)
	b.startLoop(5*time.Minute, saveTempRolesJob, b.tempRoles.save)
	b.startLoop(1*time.Minute, processTempRoleExpiriesJob, b.processTempRoleExpiries)

	shutdownErr := b.WaitForShutdown("Bot")
	stop, stopSpan := botutil.StartSpan("shutdown")
	defer stopSpan.End()
	b.stopLoops()
	b.saveState(stop)
	_ = b.saveCaches() // it logs its own failures
	return shutdownErr
}
