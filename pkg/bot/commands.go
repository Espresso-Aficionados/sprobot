package bot

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

func (b *Bot) registerAllCommands() error {
	templates := sprobot.AllTemplates(b.Env)
	if templates == nil {
		b.Log.Info("No templates configured for env", "env", b.Env)
		return nil
	}

	topPostersConfigs := getTopPostersConfig(b.Env)

	for guildID, tmpls := range templates {
		guildSnowflake := snowflake.ID(guildID)

		var commands []discord.ApplicationCommandCreate

		for _, tmpl := range tmpls {
			commands = append(commands, templateCommands(tmpl)...)
		}

		// Mod log context menu
		perm := discord.PermissionManageMessages
		commands = append(commands, discord.MessageCommandCreate{
			Name:                     "Save message to mod log",
			DefaultMemberPermissions: omit.NewPtr(perm),
		})

		// /wiki
		commands = append(commands, discord.SlashCommandCreate{
			Name:        "wiki",
			Description: "Post link to a page on the EAF wiki",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{
					Name:         "page",
					Description:  "Wiki page shortcut",
					Required:     true,
					Autocomplete: true,
				},
			},
		})

		// /s
		commands = append(commands, discord.SlashCommandCreate{
			Name:        "s",
			Description: "Post a shortcut response",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{
					Name:         "shortcut",
					Description:  "Shortcut name",
					Required:     true,
					Autocomplete: true,
				},
			},
		})

		// /sconfig
		commands = append(commands, discord.SlashCommandCreate{
			Name:                     "sconfig",
			Description:              "Configure shortcuts",
			DefaultMemberPermissions: omit.NewPtr(perm),
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionSubCommand{
					Name:        "set",
					Description: "Set a shortcut with one or more responses",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionString{
							Name:         "shortcut",
							Description:  "Shortcut name",
							Required:     true,
							Autocomplete: true,
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "remove",
					Description: "Remove a shortcut",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionString{
							Name:         "shortcut",
							Description:  "Shortcut name",
							Required:     true,
							Autocomplete: true,
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "list",
					Description: "List all shortcuts",
				},
			},
		})

		// /topposters
		if _, ok := topPostersConfigs[guildSnowflake]; ok {
			commands = append(commands, discord.SlashCommandCreate{
				Name:                     "topposters",
				Description:              "Show top message posters over the last 7 days",
				DefaultMemberPermissions: omit.NewPtr(perm),
			})
		}

		if _, err := b.Client.Rest.SetGuildCommands(b.Client.ApplicationID, guildSnowflake, commands); err != nil {
			return fmt.Errorf("registering guild commands for %d: %w", guildSnowflake, err)
		}
		b.Log.Info("Registered guild commands", "guild_id", guildSnowflake, "count", len(commands))
	}
	return nil
}

func templateCommands(tmpl sprobot.Template) []discord.ApplicationCommandCreate {
	return []discord.ApplicationCommandCreate{
		// /editprofile, /editroaster
		discord.SlashCommandCreate{
			Name:        "edit" + tmpl.ShortName,
			Description: tmpl.Description,
		},
		// /getprofile, /getroaster
		discord.SlashCommandCreate{
			Name:        "get" + tmpl.ShortName,
			Description: tmpl.Description,
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionUser{
					Name:        "name",
					Description: "User to get profile for",
				},
			},
		},
		// /deleteprofile, /deleteroaster
		discord.SlashCommandCreate{
			Name:        "delete" + tmpl.ShortName,
			Description: "Delete profile or profile image",
		},
		// Context menus: "Get <Name> Profile" (user + message)
		discord.UserCommandCreate{
			Name: fmt.Sprintf("Get %s Profile", tmpl.Name),
		},
		discord.MessageCommandCreate{
			Name: fmt.Sprintf("Get %s Profile", tmpl.Name),
		},
		// Context menu: "Save as <Name> Image"
		discord.MessageCommandCreate{
			Name: fmt.Sprintf("Save as %s Image", tmpl.Name),
		},
	}
}

func (b *Bot) onCommand(e *events.ApplicationCommandInteractionCreate) {
	if e.GuildID() == nil {
		return
	}
	guildID := *e.GuildID()

	var name string
	switch d := e.Data.(type) {
	case discord.SlashCommandInteractionData:
		name = d.CommandName()
	case discord.UserCommandInteractionData:
		name = d.CommandName()
	case discord.MessageCommandInteractionData:
		name = d.CommandName()
	default:
		return
	}

	templates := sprobot.AllTemplates(b.Env)
	tmpls, ok := templates[int64(guildID)]
	if !ok {
		return
	}

	for _, tmpl := range tmpls {
		switch name {
		case "edit" + tmpl.ShortName:
			b.handleEdit(e, tmpl)
			return
		case "get" + tmpl.ShortName:
			b.handleGet(e, tmpl)
			return
		case "delete" + tmpl.ShortName:
			b.handleDelete(e, tmpl)
			return
		case fmt.Sprintf("Get %s Profile", tmpl.Name):
			b.handleGetMenu(e, tmpl)
			return
		case fmt.Sprintf("Save as %s Image", tmpl.Name):
			b.handleSaveImageMenu(e, tmpl)
			return
		}
	}

	switch name {
	case "wiki":
		b.handleWiki(e)
	case "Save message to mod log":
		b.handleModLogMenu(e)
	case "topposters":
		b.handleTopPosters(e)
	case "s":
		b.handleShortcut(e)
	case "sconfig":
		b.handleShortcutConfig(e)
	}
}

func (b *Bot) onModal(e *events.ModalSubmitInteractionCreate) {
	customID := e.Data.CustomID

	templates := sprobot.AllTemplates(b.Env)
	if e.GuildID() == nil {
		return
	}
	guildID := int64(*e.GuildID())
	tmpls := templates[guildID]

	for _, tmpl := range tmpls {
		if customID == fmt.Sprintf("edit_%s", tmpl.ShortName) {
			b.handleEditModalSubmit(e, tmpl)
			return
		}
	}

	if strings.HasPrefix(customID, "sconfig_set_") {
		b.handleShortcutConfigSetModal(e)
		return
	}

	if strings.HasPrefix(customID, "modlog_") {
		parts := strings.SplitN(strings.TrimPrefix(customID, "modlog_"), "_", 2)
		if len(parts) == 2 {
			channelID, _ := snowflake.Parse(parts[0])
			messageID, _ := snowflake.Parse(parts[1])
			b.handleModLogModalSubmit(e, channelID, messageID)
			return
		}
	}
}

func (b *Bot) onComponent(e *events.ComponentInteractionCreate) {
	b.handleComponentInteraction(e)
}

func (b *Bot) onAutocomplete(e *events.AutocompleteInteractionCreate) {
	b.handleAutocomplete(e)
}

func guildIDStr(e interface{ GuildID() *snowflake.ID }) string {
	if id := e.GuildID(); id != nil {
		return strconv.FormatInt(int64(*id), 10)
	}
	return ""
}

func userIDStr(e interface{ User() discord.User }) string {
	return strconv.FormatInt(int64(e.User().ID), 10)
}
