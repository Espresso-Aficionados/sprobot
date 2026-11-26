package bot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
)

type welcomeState struct {
	mu      sync.Mutex
	Message string `json:"message"`
	Enabled bool   `json:"enabled"`
}

func (b *Bot) sendWelcomeDM(ctx context.Context, guildID, userID snowflake.ID) {
	// Clean up stale entries and check for recent welcome
	b.welcomeSentMu.Lock()
	now := time.Now()
	for uid, t := range b.welcomeSent {
		if now.Sub(t) > 5*time.Minute {
			delete(b.welcomeSent, uid)
		}
	}
	if _, seen := b.welcomeSent[userID]; seen {
		b.welcomeSentMu.Unlock()
		return
	}

	st := b.welcome.get(guildID)
	if st == nil {
		b.welcomeSentMu.Unlock()
		return
	}

	st.mu.Lock()
	msg := st.Message
	enabled := st.Enabled
	st.mu.Unlock()

	if !enabled || msg == "" {
		b.welcomeSentMu.Unlock()
		return
	}

	b.welcomeSent[userID] = now
	b.welcomeSentMu.Unlock()

	ch, err := b.Client.Rest.CreateDMChannel(userID, rest.WithCtx(ctx))
	if err != nil {
		b.Log.ErrorContext(ctx, "Failed to create DM channel for welcome", "user_id", userID, "guild_id", guildID, "error", err)
		return
	}

	if _, err := b.Client.Rest.CreateMessage(ch.ID(), discord.MessageCreate{Content: msg}, rest.WithCtx(ctx)); err != nil {
		b.Log.ErrorContext(ctx, "Failed to send welcome DM", "user_id", userID, "guild_id", guildID, "error", err)
	}
}

func (b *Bot) handleWelcome(ctx context.Context, e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "set":
		b.handleWelcomeSet(ctx, e)
	case "clear":
		b.handleWelcomeClear(ctx, e)
	case "show":
		b.handleWelcomeShow(ctx, e)
	case "test":
		b.handleWelcomeTest(ctx, e)
	case "enable":
		b.handleWelcomeEnable(ctx, e)
	case "disable":
		b.handleWelcomeDisable(ctx, e)
	}
}

func (b *Bot) handleWelcomeSet(ctx context.Context, e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.InfoContext(ctx, "Welcome set", "user_id", e.User().ID, "guild_id", *guildID)

	// Pre-fill with existing message
	var prefill string
	st := b.welcome.get(*guildID)
	if st != nil {
		st.mu.Lock()
		prefill = st.Message
		st.mu.Unlock()
	}

	err := e.Modal(discord.ModalCreate{
		CustomID: "welcome_set",
		Title:    "Set welcome message",
		Components: []discord.LayoutComponent{
			discord.NewLabel(
				"Welcome message",
				discord.TextInputComponent{
					CustomID: "message",
					Style:    discord.TextInputStyleParagraph,
					Required: true,
					Value:    prefill,
				},
			),
		},
	}, rest.WithCtx(ctx))
	if err != nil {
		b.Log.ErrorContext(ctx, "Failed to respond with welcome modal", "error", err)
	}
}

func (b *Bot) handleWelcomeSetModal(ctx context.Context, e *events.ModalSubmitInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.InfoContext(ctx, "Welcome set modal submitted", "user_id", e.User().ID, "guild_id", *guildID)

	msg := e.Data.Text("message")
	if strings.TrimSpace(msg) == "" {
		botutil.RespondEphemeral(ctx, e, "Message cannot be blank.")
		return
	}

	st := b.welcome.get(*guildID)
	if st == nil {
		botutil.RespondEphemeral(ctx, e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	st.Message = msg
	enabled := st.Enabled
	st.mu.Unlock()

	if err := b.welcome.persist(ctx, *guildID); err != nil {
		b.Log.ErrorContext(ctx, "Failed to save welcome data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(ctx, e, "Failed to save welcome message.")
		return
	}

	if enabled {
		botutil.RespondEphemeral(ctx, e, "Welcome message saved.")
	} else {
		botutil.RespondEphemeral(ctx, e, "Welcome message saved. Use `/config welcome enable` to activate it.")
	}
}

func (b *Bot) handleWelcomeClear(ctx context.Context, e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.InfoContext(ctx, "Welcome clear", "user_id", e.User().ID, "guild_id", *guildID)

	st := b.welcome.get(*guildID)
	if st == nil {
		botutil.RespondEphemeral(ctx, e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	st.Message = ""
	st.mu.Unlock()

	if err := b.welcome.persist(ctx, *guildID); err != nil {
		b.Log.ErrorContext(ctx, "Failed to save welcome data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(ctx, e, "Failed to clear welcome message.")
		return
	}

	botutil.RespondEphemeral(ctx, e, "Welcome message cleared.")
}

func (b *Bot) handleWelcomeShow(ctx context.Context, e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.InfoContext(ctx, "Welcome show", "user_id", e.User().ID, "guild_id", *guildID)

	st := b.welcome.get(*guildID)
	if st == nil {
		botutil.RespondEphemeral(ctx, e, "No welcome message configured.")
		return
	}

	st.mu.Lock()
	msg := st.Message
	enabled := st.Enabled
	st.mu.Unlock()

	if msg == "" {
		botutil.RespondEphemeral(ctx, e, "No welcome message configured.")
		return
	}

	status := "disabled"
	if enabled {
		status = "enabled"
	}
	botutil.RespondEphemeral(ctx, e, fmt.Sprintf("Status: **%s**\n\n%s", status, msg))
}

func (b *Bot) handleWelcomeTest(ctx context.Context, e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.InfoContext(ctx, "Welcome test", "user_id", e.User().ID, "guild_id", *guildID)

	st := b.welcome.get(*guildID)
	if st == nil {
		botutil.RespondEphemeral(ctx, e, "No welcome message configured.")
		return
	}

	st.mu.Lock()
	msg := st.Message
	enabled := st.Enabled
	st.mu.Unlock()

	if msg == "" {
		botutil.RespondEphemeral(ctx, e, "No welcome message configured. Use `/config welcome set` first.")
		return
	}

	// Defer since CreateDMChannel + CreateMessage are two sequential REST calls.
	if err := e.DeferCreateMessage(true, rest.WithCtx(ctx)); err != nil {
		b.Log.ErrorContext(ctx, "Failed to defer welcome test response", "error", err)
		return
	}

	// Send the test DM regardless of enabled state
	ch, err := b.Client.Rest.CreateDMChannel(e.User().ID, rest.WithCtx(ctx))
	if err != nil {
		b.Log.ErrorContext(ctx, "Failed to create DM channel for welcome test", "user_id", e.User().ID, "guild_id", *guildID, "error", err)
		botutil.FollowupEphemeral(ctx, b.Client, e.Token(), "Failed to send test DM.")
		return
	}
	if _, err := b.Client.Rest.CreateMessage(ch.ID(), discord.MessageCreate{Content: msg}, rest.WithCtx(ctx)); err != nil {
		b.Log.ErrorContext(ctx, "Failed to send welcome test DM", "user_id", e.User().ID, "guild_id", *guildID, "error", err)
		botutil.FollowupEphemeral(ctx, b.Client, e.Token(), "Failed to send test DM.")
		return
	}

	note := ""
	if !enabled {
		note = " (currently disabled — new members will not receive this)"
	}
	botutil.FollowupEphemeral(ctx, b.Client, e.Token(), fmt.Sprintf("Welcome DM sent. Check your DMs!%s", note))
}

func (b *Bot) handleWelcomeEnable(ctx context.Context, e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.InfoContext(ctx, "Welcome enable", "user_id", e.User().ID, "guild_id", *guildID)

	st := b.welcome.get(*guildID)
	if st == nil {
		botutil.RespondEphemeral(ctx, e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	msg := st.Message
	st.Enabled = true
	st.mu.Unlock()

	if msg == "" {
		botutil.RespondEphemeral(ctx, e, "Welcome DM enabled, but no message is configured. Use `/config welcome set` to set one.")
		return
	}

	if err := b.welcome.persist(ctx, *guildID); err != nil {
		b.Log.ErrorContext(ctx, "Failed to save welcome data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(ctx, e, "Failed to enable welcome DM.")
		return
	}

	botutil.RespondEphemeral(ctx, e, "Welcome DM enabled.")
}

func (b *Bot) handleWelcomeDisable(ctx context.Context, e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.InfoContext(ctx, "Welcome disable", "user_id", e.User().ID, "guild_id", *guildID)

	st := b.welcome.get(*guildID)
	if st == nil {
		botutil.RespondEphemeral(ctx, e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	st.Enabled = false
	st.mu.Unlock()

	if err := b.welcome.persist(ctx, *guildID); err != nil {
		b.Log.ErrorContext(ctx, "Failed to save welcome data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(ctx, e, "Failed to disable welcome DM.")
		return
	}

	botutil.RespondEphemeral(ctx, e, "Welcome DM disabled. Message preserved.")
}
