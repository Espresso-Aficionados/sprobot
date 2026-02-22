package threadbot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

type Bot struct {
	client    *bot.Client
	s3        *s3client.Client
	env       string
	log       *slog.Logger
	ready     atomic.Bool
	reminders map[snowflake.ID]map[snowflake.ID]*threadReminder // guild -> channel -> reminder
}

func New(token string) (*Bot, error) {
	s3, err := s3client.New()
	if err != nil {
		return nil, err
	}

	b := &Bot{
		s3:        s3,
		env:       os.Getenv("THREADBOT_ENV"),
		log:       slog.Default(),
		reminders: make(map[snowflake.ID]map[snowflake.ID]*threadReminder),
	}

	client, err := disgo.New(token,
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMessages,
			),
		),
		bot.WithEventListenerFunc(b.onReady),
		bot.WithEventListenerFunc(b.onCommand),
		bot.WithEventListenerFunc(b.onMessage),
	)
	if err != nil {
		return nil, err
	}

	b.client = client
	return b, nil
}

func (b *Bot) Run() error {
	ctx := context.Background()

	if err := b.client.OpenGateway(ctx); err != nil {
		return err
	}
	defer b.client.Close(ctx)

	b.loadReminders()
	if err := b.registerAllCommands(); err != nil {
		return fmt.Errorf("registering commands: %w", err)
	}
	go b.reminderSaveLoop()

	b.log.Info(fmt.Sprintf("Invite: https://discord.com/oauth2/authorize?client_id=%d&scope=bot%%20applications.commands&permissions=3072", b.client.ApplicationID))
	b.log.Info("Threadbot is running. Press Ctrl+C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	<-sc
	b.log.Info("Shutting down.")
	b.stopAllReminderGoroutines()
	b.saveAllReminders()
	return nil
}

func (b *Bot) onReady(_ *events.Ready) {
	b.log.Info("Logged in")
	b.ready.Store(true)
}

func (b *Bot) onMessage(e *events.MessageCreate) {
	if e.Message.Author.Bot {
		return
	}
	if e.GuildID == nil {
		return
	}

	guildID := *e.GuildID
	channels, ok := b.reminders[guildID]
	if !ok {
		return
	}

	r, ok := channels[e.ChannelID]
	if !ok || !r.Enabled {
		return
	}

	select {
	case r.msgCh <- struct{}{}:
	default:
	}
}
