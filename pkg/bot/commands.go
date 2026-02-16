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

func (b *Bot) registerAllCommands() {
	templates := sprobot.AllTemplates(b.env)
	if templates == nil {
		b.log.Info("No templates configured for env", "env", b.env)
		return
	}

	for guildID, tmpls := range templates {
		guildSnowflake := snowflake.ID(guildID)
		for _, tmpl := range tmpls {
			b.registerTemplateCommands(guildSnowflake, tmpl)
		}
		b.registerModLogCommand(guildSnowflake)
		b.registerWikiCommand(guildSnowflake)
	}
}

func (b *Bot) registerTemplateCommands(guildID snowflake.ID, tmpl sprobot.Template) {
	appID := b.client.ApplicationID

	// /editprofile, /editroaster
	editCmd := discord.SlashCommandCreate{
		Name:        "edit" + tmpl.ShortName,
		Description: tmpl.Description,
	}
	if _, err := b.client.Rest.CreateGuildCommand(appID, guildID, editCmd); err != nil {
		b.log.Error("Failed to create edit command", "error", err, "template", tmpl.ShortName)
	}

	// /getprofile, /getroaster
	getCmd := discord.SlashCommandCreate{
		Name:        "get" + tmpl.ShortName,
		Description: tmpl.Description,
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionUser{
				Name:        "name",
				Description: "User to get profile for",
			},
		},
	}
	if _, err := b.client.Rest.CreateGuildCommand(appID, guildID, getCmd); err != nil {
		b.log.Error("Failed to create get command", "error", err, "template", tmpl.ShortName)
	}

	// /deleteprofile, /deleteroaster
	deleteCmd := discord.SlashCommandCreate{
		Name:        "delete" + tmpl.ShortName,
		Description: "Delete profile or profile image",
	}
	if _, err := b.client.Rest.CreateGuildCommand(appID, guildID, deleteCmd); err != nil {
		b.log.Error("Failed to create delete command", "error", err, "template", tmpl.ShortName)
	}

	// /saveprofileimage, /saveroasterimage
	saveCmd := discord.SlashCommandCreate{
		Name:        "save" + tmpl.ShortName + "image",
		Description: fmt.Sprintf("Add a profile image to %s", tmpl.Name),
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionAttachment{
				Name:        "image",
				Description: "Image to save",
				Required:    true,
			},
		},
	}
	if _, err := b.client.Rest.CreateGuildCommand(appID, guildID, saveCmd); err != nil {
		b.log.Error("Failed to create save image command", "error", err, "template", tmpl.ShortName)
	}

	// Context menus: "Get <Name> Profile" (user + message)
	getUserMenu := discord.UserCommandCreate{
		Name: fmt.Sprintf("Get %s Profile", tmpl.Name),
	}
	if _, err := b.client.Rest.CreateGuildCommand(appID, guildID, getUserMenu); err != nil {
		b.log.Error("Failed to create get profile user menu", "error", err, "template", tmpl.ShortName)
	}

	getMsgMenu := discord.MessageCommandCreate{
		Name: fmt.Sprintf("Get %s Profile", tmpl.Name),
	}
	if _, err := b.client.Rest.CreateGuildCommand(appID, guildID, getMsgMenu); err != nil {
		b.log.Error("Failed to create get profile message menu", "error", err, "template", tmpl.ShortName)
	}

	// Context menu: "Save as <Name> Image"
	saveMenu := discord.MessageCommandCreate{
		Name: fmt.Sprintf("Save as %s Image", tmpl.Name),
	}
	if _, err := b.client.Rest.CreateGuildCommand(appID, guildID, saveMenu); err != nil {
		b.log.Error("Failed to create save image menu", "error", err, "template", tmpl.ShortName)
	}
}

func (b *Bot) registerModLogCommand(guildID snowflake.ID) {
	perm := discord.PermissionManageMessages
	modLogMenu := discord.MessageCommandCreate{
		Name:                     "Save message to mod log",
		DefaultMemberPermissions: omit.NewPtr(perm),
	}
	if _, err := b.client.Rest.CreateGuildCommand(b.client.ApplicationID, guildID, modLogMenu); err != nil {
		b.log.Error("Failed to create mod log menu", "error", err)
	}
}

func (b *Bot) registerWikiCommand(guildID snowflake.ID) {
	wikiCmd := discord.SlashCommandCreate{
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
	}
	if _, err := b.client.Rest.CreateGuildCommand(b.client.ApplicationID, guildID, wikiCmd); err != nil {
		b.log.Error("Failed to create wiki command", "error", err)
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

	templates := sprobot.AllTemplates(b.env)
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
		case "save" + tmpl.ShortName + "image":
			b.handleSaveImageCommand(e, tmpl)
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
	}
}

func (b *Bot) onModal(e *events.ModalSubmitInteractionCreate) {
	customID := e.Data.CustomID

	templates := sprobot.AllTemplates(b.env)
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
