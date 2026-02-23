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

	for guildID, tmpls := range templates {
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
		shortcutMaxLen := 80
		commands = append(commands, discord.SlashCommandCreate{
			Name:        "s",
			Description: "Post a shortcut response",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{
					Name:         "shortcut",
					Description:  "Shortcut name",
					Required:     true,
					Autocomplete: true,
					MaxLength:    &shortcutMaxLen,
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
							MaxLength:    &shortcutMaxLen,
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
							MaxLength:    &shortcutMaxLen,
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "list",
					Description: "List all shortcuts",
				},
			},
		})

		// /welcome
		commands = append(commands, discord.SlashCommandCreate{
			Name:                     "welcome",
			Description:              "Configure welcome DM for new members",
			DefaultMemberPermissions: omit.NewPtr(perm),
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionSubCommand{
					Name:        "set",
					Description: "Set the welcome message",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "clear",
					Description: "Clear the welcome message",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "show",
					Description: "Show the current welcome message",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "test",
					Description: "Send yourself the welcome DM",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "enable",
					Description: "Enable welcome DM for new members",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "disable",
					Description: "Disable welcome DM without clearing the message",
				},
			},
		})

		// /topposters
		if _, ok := b.topPostersConfig[guildID]; ok {
			commands = append(commands, discord.SlashCommandCreate{
				Name:                     "topposters",
				Description:              "Show top message posters over the last 7 days",
				DefaultMemberPermissions: omit.NewPtr(perm),
			})
		}

		// /marketprogress
		if _, ok := b.posterRoleConfig[guildID]; ok {
			commands = append(commands, discord.SlashCommandCreate{
				Name:                     "marketprogress",
				Description:              "Check a user's progress toward marketplace access",
				DefaultMemberPermissions: omit.NewPtr(perm),
				Options: []discord.ApplicationCommandOption{
					discord.ApplicationCommandOptionUser{
						Name:        "user",
						Description: "User to check progress for",
						Required:    true,
					},
				},
			})
		}

		// /warn
		reasonMaxLen := 1024
		commands = append(commands, discord.SlashCommandCreate{
			Name:                     "warn",
			Description:              "Warn a user",
			DefaultMemberPermissions: omit.NewPtr(perm),
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionUser{
					Name:        "user",
					Description: "User to warn",
					Required:    true,
				},
				discord.ApplicationCommandOptionString{
					Name:        "reason",
					Description: "Reason for the warning",
					Required:    true,
					MaxLength:   &reasonMaxLen,
				},
			},
		})

		if _, err := b.Client.Rest.SetGuildCommands(b.Client.ApplicationID, guildID, commands); err != nil {
			return fmt.Errorf("registering guild commands for %d: %w", guildID, err)
		}
		b.Log.Info("Registered guild commands", "guild_id", guildID, "count", len(commands))
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
	tmpls, ok := templates[guildID]
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
		}
	}

	switch name {
	case "wiki":
		b.handleWiki(e)
	case "Save message to mod log":
		b.handleModLogMenu(e)
	case "topposters":
		b.handleTopPosters(e)
	case "marketprogress":
		b.handleMarketProgress(e)
	case "s":
		b.handleShortcut(e)
	case "sconfig":
		b.handleShortcutConfig(e)
	case "welcome":
		b.handleWelcome(e)
	case "warn":
		b.handleWarn(e)
	}
}

func (b *Bot) onModal(e *events.ModalSubmitInteractionCreate) {
	customID := e.Data.CustomID

	templates := sprobot.AllTemplates(b.Env)
	if e.GuildID() == nil {
		return
	}
	guildID := *e.GuildID()
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

	if customID == "welcome_set" {
		b.handleWelcomeSetModal(e)
		return
	}

	if strings.HasPrefix(customID, "modlog_") {
		parts := strings.SplitN(strings.TrimPrefix(customID, "modlog_"), "_", 2)
		if len(parts) == 2 {
			channelID, err := snowflake.Parse(parts[0])
			if err != nil {
				b.Log.Error("Invalid channel ID in mod log modal", "value", parts[0], "error", err)
				return
			}
			messageID, err := snowflake.Parse(parts[1])
			if err != nil {
				b.Log.Error("Invalid message ID in mod log modal", "value", parts[1], "error", err)
				return
			}
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
