package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

func (b *Bot) handleDelete(e *events.ApplicationCommandInteractionCreate, tmpl sprobot.Template) {
	b.Log.Info("Processing delete",
		"user_id", userIDStr(e),
		"template", tmpl.Name,
		"guild_id", guildIDStr(e),
	)

	e.CreateMessage(discord.MessageCreate{
		Content: "What would you like to delete?",
		Flags:   discord.MessageFlagEphemeral,
		Components: []discord.LayoutComponent{
			discord.NewActionRow(
				discord.NewPrimaryButton(
					"Delete Entire Profile",
					fmt.Sprintf("del_profile_%s", tmpl.ShortName),
				),
				discord.NewPrimaryButton(
					"Delete Profile Picture",
					fmt.Sprintf("del_image_%s", tmpl.ShortName),
				),
				discord.NewSecondaryButton(
					"Cancel",
					"del_cancel",
				),
			),
		},
	})
}

func (b *Bot) handleComponentInteraction(e *events.ComponentInteractionCreate) {
	customID := e.Data.CustomID()

	if roleID, ok := isSelfroleInteraction(customID); ok {
		b.handleSelfroleToggle(e, roleID)
		return
	}

	if customID == "ticket_open" {
		b.handleTicketOpen(e)
		return
	}
	if customID == "ticket_close" {
		b.handleTicketCloseConfirm(e)
		return
	}
	if customID == "ticket_close_yes" {
		b.handleTicketClose(e)
		return
	}
	if customID == "ticket_close_no" {
		b.handleTicketCloseCancel(e)
		return
	}

	if customID == "del_cancel" {
		content := "No worries!"
		e.UpdateMessage(discord.MessageUpdate{
			Content:    &content,
			Components: &[]discord.LayoutComponent{},
		})
		return
	}

	for _, tmpls := range b.templates {
		for _, tmpl := range tmpls {
			switch customID {
			case fmt.Sprintf("del_profile_%s", tmpl.ShortName):
				content := "Are you sure?"
				e.UpdateMessage(discord.MessageUpdate{
					Content: &content,
					Components: &[]discord.LayoutComponent{
						discord.NewActionRow(
							discord.NewDangerButton(
								"Confirm",
								fmt.Sprintf("del_confirm_profile_%s", tmpl.ShortName),
							),
							discord.NewSecondaryButton(
								"Cancel",
								"del_cancel",
							),
						),
					},
				})
				return

			case fmt.Sprintf("del_image_%s", tmpl.ShortName):
				content := "Are you sure?"
				e.UpdateMessage(discord.MessageUpdate{
					Content: &content,
					Components: &[]discord.LayoutComponent{
						discord.NewActionRow(
							discord.NewDangerButton(
								"Confirm",
								fmt.Sprintf("del_confirm_image_%s", tmpl.ShortName),
							),
							discord.NewSecondaryButton(
								"Cancel",
								"del_cancel",
							),
						),
					},
				})
				return

			case fmt.Sprintf("del_confirm_profile_%s", tmpl.ShortName):
				b.confirmDeleteProfile(e, tmpl)
				return

			case fmt.Sprintf("del_confirm_image_%s", tmpl.ShortName):
				b.confirmDeleteImage(e, tmpl)
				return
			}
		}
	}
}

func (b *Bot) confirmDeleteProfile(e *events.ComponentInteractionCreate, tmpl sprobot.Template) {
	guildStr := guildIDStr(e)
	userStr := userIDStr(e)

	respond, ok := b.deferDelete(e)
	if !ok {
		return
	}

	lock := b.profileLock(guildStr, userStr)
	lock.Lock()
	defer lock.Unlock()
	defer b.profileDeleted(guildStr, userStr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := b.S3.DeleteProfile(ctx, tmpl, guildStr, userStr)
	if err != nil {
		b.Log.Error("Failed to delete profile", "error", err)
		respond("Oops! Something went wrong.")
		return
	}

	b.Log.Info("Profile deleted", "user_id", userStr, "guild_id", guildStr, "template", tmpl.Name)
	respond("Deleted!")
}

// deferDelete acknowledges a delete confirmation by swapping its buttons for a
// progress note, since the delete may wait behind a slow save (say, an image
// still uploading). The returned func replaces the note with the result.
func (b *Bot) deferDelete(e *events.ComponentInteractionCreate) (respond func(string), ok bool) {
	content := "Deleting…"
	if err := e.UpdateMessage(discord.MessageUpdate{
		Content:    &content,
		Components: &[]discord.LayoutComponent{},
	}); err != nil {
		b.Log.Error("Failed to acknowledge delete", "error", err)
		return nil, false
	}
	return func(content string) {
		if _, err := b.Client.Rest.UpdateInteractionResponse(b.Client.ApplicationID, e.Token(), discord.MessageUpdate{
			Content: &content,
		}); err != nil {
			b.Log.Error("Failed to update delete response", "error", err)
		}
	}, true
}

func (b *Bot) confirmDeleteImage(e *events.ComponentInteractionCreate, tmpl sprobot.Template) {
	guildStr := guildIDStr(e)
	userStr := userIDStr(e)

	respond, ok := b.deferDelete(e)
	if !ok {
		return
	}

	lock := b.profileLock(guildStr, userStr)
	lock.Lock()
	defer lock.Unlock()
	defer b.profileDeleted(guildStr, userStr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	profile, err := b.S3.FetchProfile(ctx, tmpl, guildStr, userStr)
	if err != nil {
		if errors.Is(err, s3client.ErrNotFound) {
			respond("Deleted!")
			return
		}
		b.Log.Error("Failed to fetch profile for image delete", "error", err)
		respond("Something went wrong, please try again.")
		return
	}

	delete(profile, tmpl.Image.Name)

	hasFields := false
	for _, f := range tmpl.Fields {
		if v, ok := profile[f.Name]; ok && strings.TrimSpace(v) != "" {
			hasFields = true
			break
		}
	}

	if hasFields {
		_, userErr, err := b.S3.SaveProfile(ctx, tmpl, guildStr, userStr, profile)
		if err != nil {
			b.Log.Error("Failed to save profile after image delete", "error", err)
			respond("Something went wrong, please try again.")
			return
		}
		if userErr != "" {
			respond(userErr)
			return
		}
	} else {
		if err := b.S3.DeleteProfile(ctx, tmpl, guildStr, userStr); err != nil {
			b.Log.Error("Failed to delete empty profile", "error", err)
			respond("Something went wrong, please try again.")
			return
		}
	}

	b.Log.Info("Profile image deleted", "user_id", userStr, "guild_id", guildStr, "template", tmpl.Name)
	respond("Deleted!")
}
