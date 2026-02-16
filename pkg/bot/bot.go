package bot

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"

	"github.com/sadbox/sprobot/pkg/s3client"
)

type Bot struct {
	client   *bot.Client
	s3       *s3client.Client
	env      string
	log      *slog.Logger
	skipList map[int]string // forum reminder skip list, keyed by thread ID
	ready    atomic.Bool
}

func New(token string) (*Bot, error) {
	s3, err := s3client.New()
	if err != nil {
		return nil, err
	}

	b := &Bot{
		s3:       s3,
		env:      os.Getenv("SPROBOT_ENV"),
		log:      slog.Default(),
		skipList: make(map[int]string),
	}

	client, err := disgo.New(token,
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMembers,
				gateway.IntentGuildMessages,
			),
		),
		bot.WithEventListenerFunc(b.onReady),
		bot.WithEventListenerFunc(b.onCommand),
		bot.WithEventListenerFunc(b.onModal),
		bot.WithEventListenerFunc(b.onComponent),
		bot.WithEventListenerFunc(b.onAutocomplete),
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

	b.registerAllCommands()
	go b.forumReminderLoop()
	go b.healthcheckLoop()

	b.log.Info("Bot is running. Press Ctrl+C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	<-sc
	b.log.Info("Shutting down.")
	return nil
}

func (b *Bot) onReady(_ *events.Ready) {
	b.log.Info("Logged in")
	b.ready.Store(true)
}
