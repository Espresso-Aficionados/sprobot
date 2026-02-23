package bot

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
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
	topPostersConfig map[snowflake.ID]topPostersConfig
	posterRoleConfig map[snowflake.ID]posterRoleConfig
	autoRoleID       snowflake.ID
}

func New(token string) (*Bot, error) {
	base, err := botutil.NewBaseBot("SPROBOT_ENV")
	if err != nil {
		return nil, err
	}

	b := &Bot{
		BaseBot:          base,
		stop:             make(chan struct{}),
		searchClient:     &http.Client{Timeout: 30 * time.Second},
		skipList:         make(map[snowflake.ID]string),
		topPosters:       make(map[snowflake.ID]*guildPostCounts),
		posterRole:       make(map[snowflake.ID]*posterRoleState),
		tickets:          make(map[snowflake.ID]*ticketState),
		shortcuts:        make(map[snowflake.ID]*shortcutState),
		topPostersConfig: getTopPostersConfig(base.Env),
		posterRoleConfig: getPosterRoleConfig(base.Env),
		autoRoleID:       getAutoRoleID(base.Env),
	}

	client, err := disgo.New(token,
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMembers,
				gateway.IntentGuildMessages,
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
	)
	if err != nil {
		return nil, err
	}

	b.Client = client
	return b, nil
}

func (b *Bot) Run() error {
	ctx := context.Background()

	b.Log.Info(fmt.Sprintf("Invite: https://discord.com/oauth2/authorize?client_id=%d&scope=bot%%20applications.commands&permissions=361045756928", b.Client.ApplicationID))

	if err := b.Client.OpenGateway(ctx); err != nil {
		return err
	}
	defer b.Client.Close(ctx)

	b.loadTopPosters()
	b.loadPosterRole()
	b.loadTickets()
	b.loadShortcuts()
	b.ensureTicketPanels()
	if err := b.registerAllCommands(); err != nil {
		return fmt.Errorf("registering commands: %w", err)
	}
	go b.forumReminderLoop()
	go botutil.RunSaveLoop(&b.Ready, 30*time.Second, b.stop, b.PingHealthcheck)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveTopPosters)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.savePosterRole)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.stop, b.saveShortcuts)

	botutil.WaitForShutdown(b.Log, "Bot")
	close(b.stop)
	b.saveTopPosters()
	b.savePosterRole()
	b.saveTickets()
	b.saveShortcuts()
	return nil
}
