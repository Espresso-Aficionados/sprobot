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
	stop             chan struct{}
	searchClient     *http.Client
	skipList         map[snowflake.ID]string // goroutine-confined to forumReminderLoop
	topPosters       map[snowflake.ID]*guildPostCounts
	posterRole       map[snowflake.ID]*posterRoleState
	tickets          map[snowflake.ID]*ticketState
	shortcuts        map[snowflake.ID]*shortcutState
	welcome          map[snowflake.ID]*welcomeState
	welcomeSentMu    sync.Mutex
	welcomeSent      map[snowflake.ID]time.Time
	msgCache         *cappedGroupedCache[discord.Message]
	memberCache      *cappedGroupedCache[discord.Member]
	starboard        map[snowflake.ID]*starboardState
	topPostersConfig map[snowflake.ID]*topPostersConfigState
	renameLogs       map[snowflake.ID]*renameLogState
	templates        map[snowflake.ID][]sprobot.Template
	selfroles        map[snowflake.ID][]selfroleConfig
	ticketConfigs    map[snowflake.ID]ticketConfig
	autoRole         map[snowflake.ID]*autoRoleState
	eventLog         map[snowflake.ID]*eventLogState
	modLog           map[snowflake.ID]*modLogState
	threadHelpConfig map[snowflake.ID]threadHelpInfo
}

func New(token string) (*Bot, error) {
	base, err := botutil.NewBaseBot("SPROBOT")
	if err != nil {
		return nil, err
	}

	msgCache := newCappedGroupedCache[discord.Message](1000000)
	memberCache := newCappedGroupedCache[discord.Member](100000)

	b := &Bot{
		BaseBot:          base,
		stop:             make(chan struct{}),
		searchClient:     &http.Client{Timeout: 30 * time.Second},
		skipList:         make(map[snowflake.ID]string),
		topPosters:       make(map[snowflake.ID]*guildPostCounts),
		posterRole:       make(map[snowflake.ID]*posterRoleState),
		tickets:          make(map[snowflake.ID]*ticketState),
		shortcuts:        make(map[snowflake.ID]*shortcutState),
		welcome:          make(map[snowflake.ID]*welcomeState),
		welcomeSent:      make(map[snowflake.ID]time.Time),
		starboard:        make(map[snowflake.ID]*starboardState),
		renameLogs:       make(map[snowflake.ID]*renameLogState),
		templates:        make(map[snowflake.ID][]sprobot.Template),
		selfroles:        make(map[snowflake.ID][]selfroleConfig),
		ticketConfigs:    make(map[snowflake.ID]ticketConfig),
		msgCache:         msgCache,
		memberCache:      memberCache,
		topPostersConfig: make(map[snowflake.ID]*topPostersConfigState),
		autoRole:         make(map[snowflake.ID]*autoRoleState),
		eventLog:         make(map[snowflake.ID]*eventLogState),
		modLog:           make(map[snowflake.ID]*modLogState),
		threadHelpConfig: getThreadHelpConfig(),
	}

	b.loadMessageCache()
	b.loadMemberCache()

	client, err := disgo.New(token,
		bot.WithEventManagerConfigOpts(bot.WithAsyncEventsEnabled()),
		bot.WithCacheConfigOpts(
			cache.WithCaches(cache.FlagGuilds|cache.FlagMessages|cache.FlagMembers|cache.FlagChannels),
			cache.WithMessageCache(cache.NewMessageCache(msgCache)),
			cache.WithMemberCache(cache.NewMemberCache(memberCache)),
		),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMembers,
				gateway.IntentGuildModeration,
				gateway.IntentGuildMessages,
				gateway.IntentGuildMessageReactions,
				gateway.IntentMessageContent,
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
	return nil
}
