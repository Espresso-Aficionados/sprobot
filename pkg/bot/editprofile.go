package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

func (b *Bot) handleEdit(e *events.ApplicationCommandInteractionCreate, tmpl sprobot.Template) {
	guildStr := guildIDStr(e)
	userStr := userIDStr(e)

	b.Log.Info("Processing edit",
		"user_id", userStr,
		"template", tmpl.Name,
		"guild_id", guildStr,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	profile := make(map[string]string)
	existing, err := b.S3.FetchProfile(ctx, tmpl, guildStr, userStr)
	if err == nil {
		profile = existing
	}

	// Build the modal with text inputs for each field
	var components []discord.LayoutComponent
	for _, field := range tmpl.Fields {
		style := discord.TextInputStyleShort
		if field.Style == sprobot.TextStyleLong {
			style = discord.TextInputStyleParagraph
		}

		defaultVal := profile[field.Name]

		components = append(components, discord.NewLabel(
			field.Name,
			discord.TextInputComponent{
				CustomID:    "field_" + field.Name,
				Placeholder: field.Placeholder,
				Style:       style,
				MaxLength:   1024,
				Required:    false,
				Value:       defaultVal,
			},
		))
	}

	// Add file upload component for profile image
	components = append(components, discord.NewLabel(
		tmpl.Image.Name,
		discord.NewFileUpload("field_image").WithRequired(false),
	))

	err = e.Modal(discord.ModalCreate{
		CustomID:   fmt.Sprintf("edit_%s", tmpl.ShortName),
		Title:      "Edit Profile",
		Components: components,
	})
	if err != nil {
		b.Log.Error("Failed to respond with modal", "error", err)
	}
}

func (b *Bot) handleEditModalSubmit(e *events.ModalSubmitInteractionCreate, tmpl sprobot.Template) {
	guildStr := guildIDStr(e)
	userStr := userIDStr(e)

	// Defer: re-hosting the image, and waiting on a running name update, can
	// outlast the 3-second response window.
	if err := e.DeferCreateMessage(true); err != nil {
		b.Log.Error("Failed to defer profile save response", "error", err)
		return
	}

	profile := make(map[string]string)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	lock := b.profileLock(guildStr, userStr)
	lock.Lock()
	defer lock.Unlock()

	// Preserve existing image URL from the saved profile
	existing, err := b.S3.FetchProfile(ctx, tmpl, guildStr, userStr)
	if err == nil {
		if img, ok := existing[tmpl.Image.Name]; ok && img != "" {
			profile[tmpl.Image.Name] = img
			profile[sprobot.ImageVersionKey] = existing[sprobot.ImageVersionKey]
		}
	}

	for _, field := range tmpl.Fields {
		profile[field.Name] = e.Data.Text("field_" + field.Name)
	}

	username := getNickOrName(e.Member())
	if e.Member() != nil {
		// Stored so the web page's link preview can say whose profile it is.
		profile[sprobot.DisplayNameKey] = username
	}

	// Check for file upload attachment (overrides existing image)
	if attachments, ok := e.Data.OptAttachments("field_image"); ok && len(attachments) > 0 {
		profile[tmpl.Image.Name] = attachments[0].ProxyURL
	}

	_, userErr, err := b.S3.SaveProfile(ctx, tmpl, guildStr, userStr, profile)
	if err != nil {
		b.Log.Error("Failed to save profile", "error", err)
		b.ephemeralFollowup(e.Token(), "Oops! Something went wrong.")
		return
	}

	if e.Member() != nil {
		b.profileSaved(guildStr, userStr, username)
	}

	if userErr != "" {
		b.ephemeralFollowup(e.Token(), userErr)
		return
	}

	embed := buildProfileEmbed(tmpl, username, profile, guildStr, userStr, b.S3)
	_, err = b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
		Embeds: []discord.Embed{embed},
		Flags:  discord.MessageFlagEphemeral,
	})
	if err != nil {
		b.Log.Error("Failed to respond with embed", "error", err)
	}
}

func getNickOrName(member *discord.ResolvedMember) string {
	if member == nil {
		return "Unknown"
	}
	return member.EffectiveName()
}
