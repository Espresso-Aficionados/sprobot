package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

const shortcutResponseSlots = 5
const maxShortcutsPerGuild = 200

type shortcutEntry struct {
	Responses []string `json:"responses"`
}

type shortcutState struct {
	mu        sync.Mutex
	Shortcuts map[string]shortcutEntry `json:"shortcuts"`
	indices   map[string]int
}

func (b *Bot) loadShortcuts() {
	templates := sprobot.AllTemplates(b.Env)
	if templates == nil {
		return
	}

	ctx := context.Background()
	for guildID := range templates {
		st := &shortcutState{
			Shortcuts: make(map[string]shortcutEntry),
			indices:   make(map[string]int),
		}

		data, err := b.S3.FetchGuildJSON(ctx, "shortcuts", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing shortcut data, starting fresh", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load shortcut data", "guild_id", guildID, "error", err)
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode shortcut data", "guild_id", guildID, "error", err)
			}
			if st.Shortcuts == nil {
				st.Shortcuts = make(map[string]shortcutEntry)
			}
			st.indices = make(map[string]int)
		}

		b.shortcuts[guildID] = st
		b.Log.Info("Loaded shortcut state", "guild_id", guildID, "count", len(st.Shortcuts))
	}
}

// persistShortcuts marshals the shortcut state under the lock and saves to S3.
func (b *Bot) persistShortcuts(guildID snowflake.ID, st *shortcutState) error {
	st.mu.Lock()
	data, err := json.Marshal(st)
	st.mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return b.S3.SaveGuildJSON(context.Background(), "shortcuts", fmt.Sprintf("%d", guildID), data)
}

func (b *Bot) saveShortcuts() {
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in shortcut save", "error", r)
		}
	}()

	for guildID, st := range b.shortcuts {
		if err := b.persistShortcuts(guildID, st); err != nil {
			b.Log.Error("Failed to save shortcut data", "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Saved shortcut state", "guild_id", guildID)
		}
	}
}

func (b *Bot) handleShortcut(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	name := data.String("shortcut")
	if name == "" {
		botutil.RespondEphemeral(e, "Please provide a shortcut name.")
		return
	}

	st := b.shortcuts[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "No shortcuts configured.")
		return
	}

	st.mu.Lock()
	entry, ok := st.Shortcuts[name]
	if !ok || len(entry.Responses) == 0 {
		st.mu.Unlock()
		botutil.RespondEphemeral(e, fmt.Sprintf("Shortcut %q not found.", name))
		return
	}

	idx, started := st.indices[name]
	if !started {
		idx = rand.IntN(len(entry.Responses))
	}
	n := len(entry.Responses)
	response := entry.Responses[idx%n]
	st.indices[name] = (idx + 1) % n
	st.mu.Unlock()

	username := getNickOrName(e.Member())
	e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{{
			Description: response,
			Footer: &discord.EmbedFooter{
				Text:    fmt.Sprintf("%s used /s %s", username, name),
				IconURL: e.User().EffectiveAvatarURL(),
			},
		}},
	})
}

func (b *Bot) handleShortcutAutocomplete(e *events.AutocompleteInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	current := strings.ToLower(e.Data.String("shortcut"))

	st := b.shortcuts[*guildID]
	if st == nil {
		e.AutocompleteResult(nil)
		return
	}

	st.mu.Lock()
	var choices []discord.AutocompleteChoice
	for name := range st.Shortcuts {
		if len(choices) >= 25 {
			break
		}
		if strings.Contains(strings.ToLower(name), current) {
			choices = append(choices, discord.AutocompleteChoiceString{
				Name:  name,
				Value: name,
			})
		}
	}
	st.mu.Unlock()

	e.AutocompleteResult(choices)
}

func (b *Bot) handleShortcutConfig(e *events.ApplicationCommandInteractionCreate) {
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
		b.handleShortcutConfigSet(e)
	case "remove":
		b.handleShortcutConfigRemove(e)
	case "list":
		b.handleShortcutConfigList(e)
	}
}

func (b *Bot) handleShortcutConfigSet(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	name := data.String("shortcut")
	if name == "" {
		botutil.RespondEphemeral(e, "Please provide a shortcut name.")
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	// Pre-fill with existing responses if the shortcut already exists
	var prefills [shortcutResponseSlots]string
	st := b.shortcuts[*guildID]
	if st != nil {
		st.mu.Lock()
		if entry, ok := st.Shortcuts[name]; ok {
			for i, r := range entry.Responses {
				if i >= shortcutResponseSlots {
					break
				}
				prefills[i] = r
			}
		}
		st.mu.Unlock()
	}

	var components []discord.LayoutComponent
	for i := range shortcutResponseSlots {
		required := i == 0
		components = append(components, discord.NewLabel(
			fmt.Sprintf("Response %d", i+1),
			discord.TextInputComponent{
				CustomID: fmt.Sprintf("response_%d", i),
				Style:    discord.TextInputStyleParagraph,
				Required: required,
				Value:    prefills[i],
			},
		))
	}

	err := e.Modal(discord.ModalCreate{
		CustomID:   fmt.Sprintf("sconfig_set_%s", name),
		Title:      fmt.Sprintf("Set shortcut: %s", name),
		Components: components,
	})
	if err != nil {
		b.Log.Error("Failed to respond with shortcut config modal", "error", err)
	}
}

func (b *Bot) handleShortcutConfigSetModal(e *events.ModalSubmitInteractionCreate) {
	customID := e.Data.CustomID
	name := strings.TrimPrefix(customID, "sconfig_set_")

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	var responses []string
	for i := range shortcutResponseSlots {
		text := e.Data.Text(fmt.Sprintf("response_%d", i))
		if strings.TrimSpace(text) != "" {
			responses = append(responses, text)
		}
	}

	if len(responses) == 0 {
		botutil.RespondEphemeral(e, "No responses provided. Shortcut not saved.")
		return
	}

	st := b.shortcuts[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "Something went wrong.")
		return
	}

	st.mu.Lock()
	if _, exists := st.Shortcuts[name]; !exists && len(st.Shortcuts) >= maxShortcutsPerGuild {
		st.mu.Unlock()
		botutil.RespondEphemeral(e, fmt.Sprintf("Maximum of %d shortcuts reached.", maxShortcutsPerGuild))
		return
	}
	st.Shortcuts[name] = shortcutEntry{Responses: responses}
	st.indices[name] = 0
	st.mu.Unlock()

	if err := b.persistShortcuts(*guildID, st); err != nil {
		b.Log.Error("Failed to save shortcut data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save shortcut.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Shortcut %q saved with %d response(s).", name, len(responses)))
}

func (b *Bot) handleShortcutConfigRemove(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	name := data.String("shortcut")
	if name == "" {
		botutil.RespondEphemeral(e, "Please provide a shortcut name.")
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.shortcuts[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "No shortcuts configured.")
		return
	}

	st.mu.Lock()
	if _, ok := st.Shortcuts[name]; !ok {
		st.mu.Unlock()
		botutil.RespondEphemeral(e, fmt.Sprintf("Shortcut %q not found.", name))
		return
	}
	delete(st.Shortcuts, name)
	delete(st.indices, name)
	st.mu.Unlock()

	if err := b.persistShortcuts(*guildID, st); err != nil {
		b.Log.Error("Failed to save shortcut data", "guild_id", *guildID, "error", err)
		botutil.RespondEphemeral(e, "Failed to save shortcut.")
		return
	}

	botutil.RespondEphemeral(e, fmt.Sprintf("Shortcut %q removed.", name))
}

func (b *Bot) handleShortcutConfigList(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	st := b.shortcuts[*guildID]
	if st == nil {
		botutil.RespondEphemeral(e, "No shortcuts configured.")
		return
	}

	st.mu.Lock()
	if len(st.Shortcuts) == 0 {
		st.mu.Unlock()
		botutil.RespondEphemeral(e, "No shortcuts configured.")
		return
	}
	var lines []string
	for name, entry := range st.Shortcuts {
		lines = append(lines, fmt.Sprintf("**%s** — %d response(s)", name, len(entry.Responses)))
	}
	st.mu.Unlock()

	botutil.RespondEphemeral(e, strings.Join(lines, "\n"))
}
