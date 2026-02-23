package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

type welcomeState struct {
	mu      sync.Mutex
	Message string `json:"message"`
	Enabled bool   `json:"enabled"`
}

func (b *Bot) loadWelcome() {
	templates := sprobot.AllTemplates(b.Env)
	if templates == nil {
		return
	}

	ctx := context.Background()
	for guildID := range templates {
		st := &welcomeState{}

		data, err := b.S3.FetchGuildJSON(ctx, "welcome", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing welcome data, starting fresh", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load welcome data", "guild_id", guildID, "error", err)
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode welcome data", "guild_id", guildID, "error", err)
			}
		}

		b.welcome[guildID] = st
		b.Log.Info("Loaded welcome state", "guild_id", guildID, "message_set", st.Message != "")
	}
}

func (b *Bot) persistWelcome(guildID snowflake.ID, st *welcomeState) error {
	st.mu.Lock()
	data, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return b.S3.SaveGuildJSON(ctx, "welcome", fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) saveWelcome() {
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in welcome save", "error", r)
		}
	}()

	for guildID, st := range b.welcome {
		if err := b.persistWelcome(guildID, st); err != nil {
			b.Log.Error("Failed to save welcome data", "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Saved welcome state", "guild_id", guildID)
		}
	}
}

func (b *Bot) sendWelcomeDM(guildID, userID snowflake.ID) {
	// Clean up stale entries and check for recent welcome
	now := time.Now()
	for uid, t := range b.welcomeSent {
		if now.Sub(t) > 5*time.Minute {
			delete(b.welcomeSent, uid)
		}
	}
	if _, seen := b.welcomeSent[userID]; seen {
		return
	}

	st := b.welcome[guildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	msg := st.Message
	enabled := st.Enabled
	st.mu.Unlock()

	if !enabled || msg == "" {
		return
	}

	b.welcomeSent[userID] = now

	ch, err := b.Client.Rest.CreateDMChannel(userID)
	if err != nil {
		b.Log.Error("Failed to create DM channel for welcome", "user_id", userID, "guild_id", guildID, "error", err)
		return
	}

	if _, err := b.Client.Rest.CreateMessage(ch.ID(), discord.MessageCreate{Content: msg}); err != nil {
		b.Log.Error("Failed to send welcome DM", "user_id", userID, "guild_id", guildID, "error", err)
	}
}

func (b *Bot) handleWelcome(e *events.ApplicationCommandInteractionCreate) {
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
		b.handleWelcomeSet(e)
	case "clear":
		b.handleWelcomeClear(e)
	case "show":
		b.handleWelcomeShow(e)
	case "test":
		b.handleWelcomeTest(e)
	case "enable":
		b.handleWelcomeEnable(e)
	case "disable":
		b.handleWelcomeDisable(e)
	}
}

func (b *Bot) handleWelcomeSet(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	// Pre-fill with existing message
	var prefill string
	st := b.welcome[*guildID]
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
	})
	if err != nil {
		b.Log.Error("Failed to respond with welcome modal", "error", err)
	}
}

func (b *Bot) handleWelcomeSetModal(e *events.ModalSubmitInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	msg := e.Data.Text("message")
	if strings.TrimSpace(msg) == "" {
		botutil.RespondEphemeral(e, "Message cannot be blank.")
		return
	}

	st := b.welcome[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	st.Message = msg
	enabled := st.Enabled
	st.mu.Unlock()

	if err := b.persistWelcome(*guildID, st); err != nil {
		b.Log.Error("Failed to save welcome data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save welcome message.")
		return
	}

	if enabled {
		botutil.RespondEphemeral(e, "Welcome message saved.")
	} else {
		botutil.RespondEphemeral(e, "Welcome message saved. Use `/welcome enable` to activate it.")
	}
}

func (b *Bot) handleWelcomeClear(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.welcome[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	st.Message = ""
	st.mu.Unlock()

	if err := b.persistWelcome(*guildID, st); err != nil {
		b.Log.Error("Failed to save welcome data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to clear welcome message.")
		return
	}

	botutil.RespondEphemeral(e, "Welcome message cleared.")
}

func (b *Bot) handleWelcomeShow(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.welcome[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "No welcome message configured.")
		return
	}

	st.mu.Lock()
	msg := st.Message
	enabled := st.Enabled
	st.mu.Unlock()

	if msg == "" {
		botutil.RespondEphemeral(e, "No welcome message configured.")
		return
	}

	status := "disabled"
	if enabled {
		status = "enabled"
	}
	botutil.RespondEphemeral(e, fmt.Sprintf("Status: **%s**\n\n%s", status, msg))
}

func (b *Bot) handleWelcomeTest(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.welcome[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "No welcome message configured.")
		return
	}

	st.mu.Lock()
	msg := st.Message
	enabled := st.Enabled
	st.mu.Unlock()

	if msg == "" {
		botutil.RespondEphemeral(e, "No welcome message configured. Use `/welcome set` first.")
		return
	}

	// Send the test DM regardless of enabled state
	ch, err := b.Client.Rest.CreateDMChannel(e.User().ID)
	if err != nil {
		b.Log.Error("Failed to create DM channel for welcome test", "user_id", e.User().ID, "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to send test DM.")
		return
	}
	if _, err := b.Client.Rest.CreateMessage(ch.ID(), discord.MessageCreate{Content: msg}); err != nil {
		b.Log.Error("Failed to send welcome test DM", "user_id", e.User().ID, "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to send test DM.")
		return
	}

	note := ""
	if !enabled {
		note = " (currently disabled — new members will not receive this)"
	}
	botutil.RespondEphemeral(e, fmt.Sprintf("Welcome DM sent. Check your DMs!%s", note))
}

func (b *Bot) handleWelcomeEnable(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.welcome[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	msg := st.Message
	st.Enabled = true
	st.mu.Unlock()

	if msg == "" {
		botutil.RespondEphemeral(e, "Welcome DM enabled, but no message is configured. Use `/welcome set` to set one.")
		return
	}

	if err := b.persistWelcome(*guildID, st); err != nil {
		b.Log.Error("Failed to save welcome data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to enable welcome DM.")
		return
	}

	botutil.RespondEphemeral(e, "Welcome DM enabled.")
}

func (b *Bot) handleWelcomeDisable(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.welcome[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	st.Enabled = false
	st.mu.Unlock()

	if err := b.persistWelcome(*guildID, st); err != nil {
		b.Log.Error("Failed to save welcome data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to disable welcome DM.")
		return
	}

	botutil.RespondEphemeral(e, "Welcome DM disabled. Message preserved.")
}
