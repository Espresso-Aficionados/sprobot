package stickybot

import (
	"context"
	"fmt"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

type Bot struct {
	*botutil.BaseBot
	stickies map[snowflake.ID]map[snowflake.ID]*stickyMessage // guild -> channel -> sticky
}

func New(token string) (*Bot, error) {
	base, err := botutil.NewBaseBot("STICKYBOT_ENV")
	if err != nil {
		return nil, err
	}

	b := &Bot{
		BaseBot:  base,
		stickies: make(map[snowflake.ID]map[snowflake.ID]*stickyMessage),
	}

	client, err := disgo.New(token,
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMessages,
				gateway.IntentMessageContent,
			),
		),
		bot.WithEventListenerFunc(b.OnReady),
		bot.WithEventListenerFunc(b.onCommand),
		bot.WithEventListenerFunc(b.onModal),
		bot.WithEventListenerFunc(b.onMessage),
	)
	if err != nil {
		return nil, err
	}

	b.Client = client
	return b, nil
}

func (b *Bot) Run() error {
	ctx := context.Background()

	if err := b.Client.OpenGateway(ctx); err != nil {
		return err
	}
	defer b.Client.Close(ctx)

	b.loadStickies()
	if err := b.registerAllCommands(); err != nil {
		return fmt.Errorf("registering commands: %w", err)
	}
	go botutil.RunSaveLoop(&b.Ready, 30*time.Second, b.PingHealthcheck)
	go botutil.RunSaveLoop(&b.Ready, 5*time.Minute, b.saveAllStickies)

	b.Log.Info(fmt.Sprintf("Invite: https://discord.com/oauth2/authorize?client_id=%d&scope=bot%%20applications.commands&permissions=68608", b.Client.ApplicationID))
	botutil.WaitForShutdown(b.Log, "Stickybot")
	b.stopAllStickyGoroutines()
	b.saveAllStickies()
	return nil
}

func (b *Bot) onMessage(e *events.MessageCreate) {
	if e.Message.Author.Bot {
		return
	}
	if e.GuildID == nil {
		return
	}

	guildID := *e.GuildID
	channels, ok := b.stickies[guildID]
	if !ok {
		return
	}

	s, ok := channels[e.ChannelID]
	if !ok || !s.Active {
		return
	}

	s.handle.Signal()
}
