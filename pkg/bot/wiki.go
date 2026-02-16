package bot

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

func (b *Bot) handleWiki(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	page := data.String("page")
	if page == "" {
		respondEphemeral(e, "Please provide a page name.")
		return
	}

	for _, link := range sprobot.WikiLinks {
		if link.Shortcut == page {
			e.CreateMessage(discord.MessageCreate{
				Content: link.URL,
			})
			return
		}
	}

	respondEphemeral(e, fmt.Sprintf("Can't find a link for page %s!", page))
}

func (b *Bot) handleAutocomplete(e *events.AutocompleteInteractionCreate) {
	current := strings.ToLower(e.Data.String("page"))
	var choices []discord.AutocompleteChoice

	for _, link := range sprobot.WikiLinks {
		if len(choices) >= 20 {
			break
		}

		if strings.Contains(link.Shortcut, current) {
			choices = append(choices, discord.AutocompleteChoiceString{
				Name:  link.Shortcut,
				Value: link.Shortcut,
			})
			continue
		}

		for _, hint := range link.Hints {
			if strings.Contains(hint, current) {
				choices = append(choices, discord.AutocompleteChoiceString{
					Name:  link.Shortcut,
					Value: link.Shortcut,
				})
				break
			}
		}
	}

	e.AutocompleteResult(choices)
}
