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
	topPosters           map[snowflake.ID]*guildPostCounts
	posterRole           map[snowflake.ID]*posterRoleState
	tickets              map[snowflake.ID]*ticketState
	shortcuts            map[snowflake.ID]*shortcutState
	welcome              map[snowflake.ID]*welcomeState
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
	starboard            map[snowflake.ID]*starboardState
	topPostersConfig     map[snowflake.ID]*topPostersConfigState
	renameLogs           map[snowflake.ID]*renameLogState
	templates            map[snowflake.ID][]sprobot.Template
	selfroles            map[snowflake.ID][]selfroleConfig
	ticketConfigs        map[snowflake.ID]ticketConfig
	autoRole             map[snowflake.ID]*autoRoleState
	eventLog             map[snowflake.ID]*eventLogState
	modLog               map[snowflake.ID]*modLogState
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
		BaseBot:              base,
		stop:                 make(chan struct{}),
		searchClient:         &http.Client{Timeout: 30 * time.Second},
		skipList:             make(map[snowflake.ID]string),
		topPosters:           make(map[snowflake.ID]*guildPostCounts),
		posterRole:           make(map[snowflake.ID]*posterRoleState),
		tickets:              make(map[snowflake.ID]*ticketState),
		shortcuts:            make(map[snowflake.ID]*shortcutState),
		welcome:              make(map[snowflake.ID]*welcomeState),
		welcomeSent:          make(map[snowflake.ID]time.Time),
		starboard:            make(map[snowflake.ID]*starboardState),
		renameLogs:           make(map[snowflake.ID]*renameLogState),
		templates:            make(map[snowflake.ID][]sprobot.Template),
		selfroles:            make(map[snowflake.ID][]selfroleConfig),
		ticketConfigs:        make(map[snowflake.ID]ticketConfig),
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
		topPostersConfig:     make(map[snowflake.ID]*topPostersConfigState),
		autoRole:             make(map[snowflake.ID]*autoRoleState),
		eventLog:             make(map[snowflake.ID]*eventLogState),
		modLog:               make(map[snowflake.ID]*modLogState),
		threadHelpConfig:     getThreadHelpConfig(),
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

	b.loadTemplates()
	b.loadSelfroles()
	b.loadTicketConfigs()
	b.loadAutoRole()
	b.loadEventLog()
	b.loadModLog()
	b.loadTopPostersConfig()
	b.loadTopPosters()
	b.loadPosterRole()
	b.loadTickets()
	b.loadShortcuts()
	b.loadWelcome()
	b.loadStarboard()
	b.loadRenameLogs()
	b.ensureTicketPanels()
	b.ensureSelfrolePanels()
	if err := b.registerAllCommands(); err != nil {
		return fmt.Errorf("registering commands: %w", err)
	}
	go b.forumReminderLoop()
	go botutil.RunSaveLoop(&b.Ready, 30*time.Second, b.stop, b.PingHealthcheck)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveTopPosters)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.savePosterRole)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveShortcuts)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveTickets)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveWelcome)
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
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveRenameLogs)

	botutil.WaitForShutdown(b.Log, "Bot")
	close(b.stop)
	b.saveAutoRole()
	b.saveEventLog()
	b.saveModLog()
	b.saveTopPostersConfig()
	b.saveTopPosters()
	b.savePosterRole()
	b.saveTickets()
	b.saveShortcuts()
	b.saveWelcome()
	b.saveStarboard()
	b.saveRenameLogs()
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
