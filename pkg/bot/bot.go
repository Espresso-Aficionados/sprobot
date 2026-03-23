package bot

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

type Bot struct {
	*botutil.BaseBot
	stop                 chan struct{}
	searchClient         *http.Client
	skipList             map[snowflake.ID]string // goroutine-confined to forumReminderLoop
	topPosters           *guildStateStore[guildPostCounts]
	posterRole           *guildStateStore[posterRoleState]
	tickets              *guildStateStore[ticketState]
	shortcuts            *guildStateStore[shortcutState]
	welcome              *guildStateStore[welcomeState]
	welcomeSentMu        sync.Mutex
	welcomeSent          map[snowflake.ID]time.Time
	msgCache             *cappedGroupedCache[discord.Message]
	memberCache          *cappedGroupedCache[discord.Member]
	guildCache           *cappedCache[discord.Guild]
	channelCache         *cappedCache[discord.GuildChannel]
	roleCache            *cappedGroupedCache[discord.Role]
	emojiCache           *cappedGroupedCache[discord.Emoji]
	stickerCache         *cappedGroupedCache[discord.Sticker]
	voiceStateCache      *cappedGroupedCache[discord.VoiceState]
	presenceCache        *cappedGroupedCache[discord.Presence]
	threadMemberCache    *cappedGroupedCache[discord.ThreadMember]
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
	tempRoleConfig       *guildStateStore[tempRoleConfigState]
	tempRoles            *guildStateStore[tempRoleState]
	threadHelpConfig     map[snowflake.ID]threadHelpInfo
}

func New(token string) (*Bot, error) {
	base, err := botutil.NewBaseBot("SPROBOT")
	if err != nil {
		return nil, err
	}

	msgCache := newCappedGroupedCache[discord.Message](1000000)
	memberCache := newCappedGroupedCache[discord.Member](100000)
	guildCache := newCappedCache[discord.Guild](100)
	channelCache := newCappedCache[discord.GuildChannel](10000)
	roleCache := newCappedGroupedCache[discord.Role](5000)
	emojiCache := newCappedGroupedCache[discord.Emoji](5000)
	stickerCache := newCappedGroupedCache[discord.Sticker](1000)
	voiceStateCache := newCappedGroupedCache[discord.VoiceState](10000)
	presenceCache := newCappedGroupedCache[discord.Presence](10000)
	threadMemberCache := newCappedGroupedCache[discord.ThreadMember](10000)
	stageInstanceCache := newCappedGroupedCache[discord.StageInstance](1000)
	scheduledEventCache := newCappedGroupedCache[discord.GuildScheduledEvent](1000)
	soundboardSoundCache := newCappedGroupedCache[discord.SoundboardSound](1000)

	b := &Bot{
		BaseBot:      base,
		stop:         make(chan struct{}),
		searchClient: &http.Client{Timeout: 30 * time.Second},
		skipList:     make(map[snowflake.ID]string),
		topPosters: newGuildStateStore(base.S3, base.Log, "topposters",
			func() *guildPostCounts {
				return &guildPostCounts{Counts: make(map[string]map[string]int), Usernames: make(map[string]string)}
			},
			func(gc *guildPostCounts) *sync.Mutex { return &gc.mu }),
		posterRole: newGuildStateStore(base.S3, base.Log, "posterroles",
			func() *posterRoleState {
				return &posterRoleState{Counts: make(map[string]int), Fetched: make(map[string]bool)}
			},
			func(st *posterRoleState) *sync.Mutex { return &st.mu }),
		tickets: newGuildStateStore(base.S3, base.Log, "tickets",
			func() *ticketState { return &ticketState{Counter: 1} },
			func(st *ticketState) *sync.Mutex { return &st.mu }),
		shortcuts: newGuildStateStore(base.S3, base.Log, "shortcuts",
			func() *shortcutState {
				return &shortcutState{Shortcuts: make(map[string]shortcutEntry), indices: make(map[string]int)}
			},
			func(st *shortcutState) *sync.Mutex { return &st.mu }),
		welcome: newGuildStateStore(base.S3, base.Log, "welcome",
			func() *welcomeState { return &welcomeState{} },
			func(st *welcomeState) *sync.Mutex { return &st.mu }),
		welcomeSent: make(map[snowflake.ID]time.Time),
		starboard: newGuildStateStore(base.S3, base.Log, "starboard",
			func() *starboardState { return &starboardState{Entries: make(map[snowflake.ID]starboardEntry)} },
			func(st *starboardState) *sync.Mutex { return &st.mu }),
		renameLogs: newGuildStateStore(base.S3, base.Log, "renamelogs",
			func() *renameLogState { return &renameLogState{} },
			func(st *renameLogState) *sync.Mutex { return &st.mu }),
		templates:     make(map[snowflake.ID][]sprobot.Template),
		selfroles:     make(map[snowflake.ID][]selfroleConfig),
		ticketConfigs: make(map[snowflake.ID]ticketConfig),
		topPostersConfig: newGuildStateStore(base.S3, base.Log, "toppostersconfig",
			func() *topPostersConfigState { return &topPostersConfigState{} },
			func(st *topPostersConfigState) *sync.Mutex { return &st.mu }),
		autoRole: newGuildStateStore(base.S3, base.Log, "autorole",
			func() *autoRoleState { return &autoRoleState{} },
			func(st *autoRoleState) *sync.Mutex { return &st.mu }),
		eventLog: newGuildStateStore(base.S3, base.Log, "eventlog",
			func() *eventLogState { return &eventLogState{} },
			func(st *eventLogState) *sync.Mutex { return &st.mu }),
		modLog: newGuildStateStore(base.S3, base.Log, "modlog",
			func() *modLogState { return &modLogState{} },
			func(st *modLogState) *sync.Mutex { return &st.mu }),
		tempRoleConfig: newGuildStateStore(base.S3, base.Log, "temproleconfig",
			func() *tempRoleConfigState { return &tempRoleConfigState{} },
			func(st *tempRoleConfigState) *sync.Mutex { return &st.mu }),
		tempRoles: newGuildStateStore(base.S3, base.Log, "temproles",
			func() *tempRoleState { return &tempRoleState{} },
			func(st *tempRoleState) *sync.Mutex { return &st.mu }),
		msgCache:             msgCache,
		memberCache:          memberCache,
		guildCache:           guildCache,
		channelCache:         channelCache,
		roleCache:            roleCache,
		emojiCache:           emojiCache,
		stickerCache:         stickerCache,
		voiceStateCache:      voiceStateCache,
		presenceCache:        presenceCache,
		threadMemberCache:    threadMemberCache,
		stageInstanceCache:   stageInstanceCache,
		scheduledEventCache:  scheduledEventCache,
		soundboardSoundCache: soundboardSoundCache,
		threadHelpConfig:     getThreadHelpConfig(),
	}

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
			st.TargetRoleID = roleID
		}
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

	b.loadMessageCache()
	b.loadMemberCache()
	b.loadGuildCache()
	b.loadChannelCache()
	b.loadRoleCache()
	b.loadEmojiCache()
	b.loadStickerCache()
	b.loadVoiceStateCache()
	b.loadPresenceCache()
	b.loadThreadMemberCache()
	b.loadStageInstanceCache()
	b.loadScheduledEventCache()
	b.loadSoundboardSoundCache()

	client, err := disgo.New(token,
		bot.WithEventManagerConfigOpts(bot.WithAsyncEventsEnabled()),
		bot.WithCacheConfigOpts(
			cache.WithCaches(cache.FlagsAll),
			cache.WithGuildCache(cache.NewGuildCache(guildCache, cache.NewSet[snowflake.ID](), cache.NewSet[snowflake.ID]())),
			cache.WithChannelCache(cache.NewChannelCache(channelCache)),
			cache.WithMessageCache(cache.NewMessageCache(msgCache)),
			cache.WithMemberCache(cache.NewMemberCache(memberCache)),
			cache.WithRoleCache(cache.NewRoleCache(roleCache)),
			cache.WithEmojiCache(cache.NewEmojiCache(emojiCache)),
			cache.WithStickerCache(cache.NewStickerCache(stickerCache)),
			cache.WithVoiceStateCache(cache.NewVoiceStateCache(voiceStateCache)),
			cache.WithPresenceCache(cache.NewPresenceCache(presenceCache)),
			cache.WithThreadMemberCache(cache.NewThreadMemberCache(threadMemberCache)),
			cache.WithStageInstanceCache(cache.NewStageInstanceCache(stageInstanceCache)),
			cache.WithGuildScheduledEventCache(cache.NewGuildScheduledEventCache(scheduledEventCache)),
			cache.WithGuildSoundboardSoundCache(cache.NewGuildSoundboardSoundCache(soundboardSoundCache)),
		),
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
		),
		bot.WithEventListenerFunc(b.OnReady),
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
	)
	if err != nil {
		return nil, err
	}

	b.Client = client
	return b, nil
}

func (b *Bot) Run() error {
	ctx := context.Background()

	b.Log.Info(fmt.Sprintf("Invite: https://discord.com/oauth2/authorize?client_id=%d&scope=bot%%20applications.commands&permissions=361045773440", b.Client.ApplicationID))

	if err := b.Client.OpenGateway(ctx); err != nil {
		return err
	}
	defer b.Client.Close(ctx)

	b.WaitForGuilds(30 * time.Second)

	guildIDs := b.GuildIDs()
	b.loadTemplates()
	b.loadSelfroles()
	b.loadTicketConfigs()
	b.autoRole.load(guildIDs)
	b.eventLog.load(guildIDs)
	b.modLog.load(guildIDs)
	b.topPostersConfig.load(guildIDs)
	b.topPosters.load(guildIDs)
	b.posterRole.load(guildIDs)
	b.tickets.load(guildIDs)
	b.shortcuts.load(guildIDs)
	b.welcome.load(guildIDs)
	b.starboard.load(guildIDs)
	b.renameLogs.load(guildIDs)
	b.tempRoleConfig.load(guildIDs)
	b.tempRoles.load(guildIDs)
	b.ensureTicketPanels()
	b.ensureSelfrolePanels()
	if err := b.registerAllCommands(); err != nil {
		return fmt.Errorf("registering commands: %w", err)
	}
	go b.forumReminderLoop()
	go botutil.RunSaveLoop(&b.Ready, 30*time.Second, b.stop, b.PingHealthcheck)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveTopPosters)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.posterRole.save)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.shortcuts.save)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.tickets.save)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.welcome.save)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveMessageCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveMemberCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveGuildCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveChannelCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveRoleCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveEmojiCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveStickerCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveVoiceStateCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.savePresenceCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveThreadMemberCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveStageInstanceCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveScheduledEventCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveSoundboardSoundCache)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveStarboard)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.renameLogs.save)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.tempRoles.save)
	go botutil.RunSaveLoop(&b.Ready, 1*time.Minute, b.stop, b.processTempRoleExpiries)

	botutil.WaitForShutdown(b.Log, "Bot")
	close(b.stop)
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
	b.saveMessageCache()
	b.saveMemberCache()
	b.saveGuildCache()
	b.saveChannelCache()
	b.saveRoleCache()
	b.saveEmojiCache()
	b.saveStickerCache()
	b.saveVoiceStateCache()
	b.savePresenceCache()
	b.saveThreadMemberCache()
	b.saveStageInstanceCache()
	b.saveScheduledEventCache()
	b.saveSoundboardSoundCache()
	return nil
}
