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
	if len(b.templates) == 0 {
		b.Log.Info("No templates configured")
		return nil
	}

	for guildID, tmpls := range b.templates {
		var commands []discord.ApplicationCommandCreate

		for _, tmpl := range tmpls {
			commands = append(commands, templateCommands(tmpl)...)
		}

		// Build type choices from template ShortNames
		var typeChoices []discord.ApplicationCommandOptionChoiceString
		for _, tmpl := range tmpls {
			typeChoices = append(typeChoices, discord.ApplicationCommandOptionChoiceString{
				Name:  tmpl.Name,
				Value: tmpl.ShortName,
			})
		}
		typeOpt := discord.ApplicationCommandOptionString{
			Name:        "type",
			Description: "Profile type",
			Choices:     typeChoices,
		}

		// /profile edit|view|delete
		commands = append(commands, discord.SlashCommandCreate{
			Name:        "profile",
			Description: "Manage your profile",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionSubCommand{
					Name:        "edit",
					Description: "Edit or create your profile",
					Options: []discord.ApplicationCommandOption{
						typeOpt,
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "view",
					Description: "View a user's profile",
					Options: []discord.ApplicationCommandOption{
						typeOpt,
						discord.ApplicationCommandOptionUser{
							Name:        "name",
							Description: "User to get profile for",
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "delete",
					Description: "Delete profile or profile image",
					Options: []discord.ApplicationCommandOption{
						typeOpt,
					},
				},
			},
		})

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

		// /marketprogress, /marketleaderboard, /marketconfig, /marketblacklist
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
				discord.ApplicationCommandOptionBool{
					Name:        "public",
					Description: "Post the result publicly in the channel (default: hidden)",
				},
			},
		})
		commands = append(commands, discord.SlashCommandCreate{
			Name:                     "marketleaderboard",
			Description:              "Show top users by progress toward marketplace access",
			DefaultMemberPermissions: omit.NewPtr(perm),
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionBool{
					Name:        "public",
					Description: "Post the result publicly in the channel (default: hidden)",
				},
			},
		})
		commands = append(commands, discord.SlashCommandCreate{
			Name:                     "marketconfig",
			Description:              "Configure marketplace poster role",
			DefaultMemberPermissions: omit.NewPtr(perm),
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionSubCommand{
					Name:        "set",
					Description: "Update marketplace settings",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionRole{
							Name:        "role",
							Description: "Role to grant when threshold is reached",
						},
						discord.ApplicationCommandOptionInt{
							Name:        "threshold",
							Description: "Number of posts required (1-10000)",
							MinValue:    intPtr(1),
							MaxValue:    intPtr(10000),
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "show",
					Description: "Show current marketplace configuration",
				},
			},
		})
		commands = append(commands, discord.SlashCommandCreate{
			Name:                     "marketblacklist",
			Description:              "Manage marketplace channel blacklist",
			DefaultMemberPermissions: omit.NewPtr(perm),
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionSubCommand{
					Name:        "add",
					Description: "Add a channel to the blacklist",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionChannel{
							Name:        "channel",
							Description: "Channel to blacklist",
							Required:    true,
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "remove",
					Description: "Remove a channel from the blacklist",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionChannel{
							Name:        "channel",
							Description: "Channel to remove from blacklist",
							Required:    true,
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "list",
					Description: "List all blacklisted channels",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "clear",
					Description: "Remove all channels from the blacklist",
				},
			},
		})

		// /sbconfig and /sbblacklist
		emojiMaxLen := 50
		if _, ok := b.starboardConfig[guildID]; ok {
			commands = append(commands, discord.SlashCommandCreate{
				Name:                     "sbconfig",
				Description:              "Configure starboard",
				DefaultMemberPermissions: omit.NewPtr(perm),
				Options: []discord.ApplicationCommandOption{
					discord.ApplicationCommandOptionSubCommand{
						Name:        "set",
						Description: "Update starboard settings",
						Options: []discord.ApplicationCommandOption{
							discord.ApplicationCommandOptionChannel{
								Name:        "channel",
								Description: "Channel to post starboard entries in",
							},
							discord.ApplicationCommandOptionString{
								Name:        "emoji",
								Description: "Reaction emoji (e.g. ⭐ or paste a custom emoji)",
								MaxLength:   &emojiMaxLen,
							},
							discord.ApplicationCommandOptionInt{
								Name:        "threshold",
								Description: "Number of reactions to trigger starboard (1-100)",
								MinValue:    intPtr(1),
								MaxValue:    intPtr(100),
							},
						},
					},
					discord.ApplicationCommandOptionSubCommand{
						Name:        "show",
						Description: "Show current starboard configuration",
					},
					discord.ApplicationCommandOptionSubCommand{
						Name:        "disable",
						Description: "Disable starboard posting",
					},
				},
			})

			commands = append(commands, discord.SlashCommandCreate{
				Name:                     "sbblacklist",
				Description:              "Manage starboard channel blacklist",
				DefaultMemberPermissions: omit.NewPtr(perm),
				Options: []discord.ApplicationCommandOption{
					discord.ApplicationCommandOptionSubCommand{
						Name:        "add",
						Description: "Add a channel to the blacklist",
						Options: []discord.ApplicationCommandOption{
							discord.ApplicationCommandOptionChannel{
								Name:        "channel",
								Description: "Channel to blacklist",
								Required:    true,
							},
						},
					},
					discord.ApplicationCommandOptionSubCommand{
						Name:        "remove",
						Description: "Remove a channel from the blacklist",
						Options: []discord.ApplicationCommandOption{
							discord.ApplicationCommandOptionChannel{
								Name:        "channel",
								Description: "Channel to remove from blacklist",
								Required:    true,
							},
						},
					},
					discord.ApplicationCommandOptionSubCommand{
						Name:        "list",
						Description: "List all blacklisted channels",
					},
					discord.ApplicationCommandOptionSubCommand{
						Name:        "clear",
						Description: "Remove all channels from the blacklist",
					},
				},
			})
		}

		// /renamelog
		commands = append(commands, discord.SlashCommandCreate{
			Name:                     "renamelog",
			Description:              "Configure channel/thread rename logging",
			DefaultMemberPermissions: omit.NewPtr(perm),
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionSubCommand{
					Name:        "set",
					Description: "Set the destination channel for rename logs",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionChannel{
							Name:        "channel",
							Description: "Channel to post rename logs in",
							Required:    true,
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "add",
					Description: "Add a channel to monitor for renames",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionChannel{
							Name:        "channel",
							Description: "Channel to monitor",
							Required:    true,
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "remove",
					Description: "Remove a channel from rename monitoring",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionChannel{
							Name:        "channel",
							Description: "Channel to stop monitoring",
							Required:    true,
						},
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "list",
					Description: "Show rename log configuration",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "clear",
					Description: "Remove all rename log configuration",
				},
			},
		})

		// /config
		commands = append(commands, discord.SlashCommandCreate{
			Name:                     "config",
			Description:              "Configure bot settings",
			DefaultMemberPermissions: omit.NewPtr(perm),
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionSubCommand{
					Name:        "profiles",
					Description: "Open the profile template configuration page",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "selfroles",
					Description: "Open the self-assign roles configuration page",
				},
				discord.ApplicationCommandOptionSubCommand{
					Name:        "tickets",
					Description: "Open the ticket system configuration page",
				},
			},
		})

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
		// Context menus: "Get <Name> Profile" (user + message)
		discord.UserCommandCreate{
			Name: fmt.Sprintf("Get %s Profile", tmpl.Name),
		},
		discord.MessageCommandCreate{
			Name: fmt.Sprintf("Get %s Profile", tmpl.Name),
		},
	}
}

func resolveTemplate(tmpls []sprobot.Template, typeName string) (sprobot.Template, bool) {
	if typeName != "" {
		for _, t := range tmpls {
			if t.ShortName == typeName {
				return t, true
			}
		}
		return sprobot.Template{}, false
	}
	if len(tmpls) > 0 {
		return tmpls[0], true
	}
	return sprobot.Template{}, false
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

	tmpls, ok := b.templates[guildID]
	if !ok {
		return
	}

	// /profile edit|view|delete
	if name == "profile" {
		data, ok := e.Data.(discord.SlashCommandInteractionData)
		if !ok || data.SubCommandName == nil {
			return
		}
		typeName, _ := data.OptString("type")
		tmpl, ok := resolveTemplate(tmpls, typeName)
		if !ok {
			return
		}
		switch *data.SubCommandName {
		case "edit":
			b.handleEdit(e, tmpl)
		case "view":
			b.handleGet(e, tmpl)
		case "delete":
			b.handleDelete(e, tmpl)
		}
		return
	}

	// Context menus stay per-template
	for _, tmpl := range tmpls {
		if name == fmt.Sprintf("Get %s Profile", tmpl.Name) {
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
	case "marketleaderboard":
		b.handleMarketLeaderboard(e)
	case "s":
		b.handleShortcut(e)
	case "sconfig":
		b.handleShortcutConfig(e)
	case "welcome":
		b.handleWelcome(e)
	case "warn":
		b.handleWarn(e)
	case "marketconfig":
		b.handleMarketConfig(e)
	case "marketblacklist":
		b.handleMarketBlacklist(e)
	case "sbconfig":
		b.handleStarboardConfig(e)
	case "sbblacklist":
		b.handleStarboardBlacklist(e)
	case "renamelog":
		b.handleRenameLog(e)
	case "config":
		b.handleConfig(e)
	}
}

func (b *Bot) onModal(e *events.ModalSubmitInteractionCreate) {
	customID := e.Data.CustomID

	if e.GuildID() == nil {
		return
	}
	guildID := *e.GuildID()
	tmpls := b.templates[guildID]

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
