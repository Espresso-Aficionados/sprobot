package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/sadbox/sprobot/pkg/botutil"
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

	profile := make(map[string]string)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Preserve existing image URL from the saved profile
	existing, err := b.S3.FetchProfile(ctx, tmpl, guildStr, userStr)
	if err == nil {
		if img, ok := existing[tmpl.Image.Name]; ok && img != "" {
			profile[tmpl.Image.Name] = img
		}
	}

	for _, field := range tmpl.Fields {
		profile[field.Name] = e.Data.Text("field_" + field.Name)
	}

	// Check for file upload attachment (overrides existing image)
	if attachments, ok := e.Data.OptAttachments("field_image"); ok && len(attachments) > 0 {
		profile[tmpl.Image.Name] = attachments[0].ProxyURL
	}

	_, userErr, err := b.S3.SaveProfile(ctx, tmpl, guildStr, userStr, profile)
	if err != nil {
		b.Log.Error("Failed to save profile", "error", err)
		botutil.RespondEphemeral(e, "Oops! Something went wrong.")
		return
	}

	if userErr != "" {
		botutil.RespondEphemeral(e, userErr)
		return
	}

	username := getNickOrName(e.Member())
	embed := buildProfileEmbed(tmpl, username, profile, guildStr, userStr, b.S3.Bucket())
	err = e.CreateMessage(discord.MessageCreate{
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
