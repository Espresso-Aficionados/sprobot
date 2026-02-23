package bot

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

type selfroleButton struct {
	Label  string
	Emoji  string
	RoleID snowflake.ID
}

type selfroleConfig struct {
	ChannelID snowflake.ID
	Message   string
	Buttons   []selfroleButton
}

func getSelfroleConfig(env string) map[snowflake.ID][]selfroleConfig {
	switch env {
	case "prod":
		return map[snowflake.ID][]selfroleConfig{
			726985544038612993: {
				{
					ChannelID: 727325278820368456,
					Message: `Want to share your pronouns? Clicking the reaction below will add a role that will allow other people to click your username and identify your pronouns! Please note that there is no need to share your pronouns if you don't want to for any reason.

:one: "Ask Me/Check .getespresso"
:two: "They/them"
:three: "She/her"
:four: "He/him"
:five: "It/its"

If your chosen pronouns are not present and you would like them to be please make a ticket to let us know. We do ask you to respect other people and not make a joke of pronouns here on in bot profiles.

Made a mistake? Just click again to remove the role`,
					Buttons: []selfroleButton{
						{Label: "Ask Me/Check .getespresso", Emoji: "1️⃣", RoleID: 807495977362653214},
						{Label: "They/them", Emoji: "2️⃣", RoleID: 807495948405178379},
						{Label: "She/her", Emoji: "3️⃣", RoleID: 807495895499014165},
						{Label: "He/him", Emoji: "4️⃣", RoleID: 807495784756936745},
						{Label: "It/its", Emoji: "5️⃣", RoleID: 1088661493685432391},
					},
				},
				{
					ChannelID: 727325278820368456,
					Message: `Are you excellent at dialing shots in? Do you know a lot about fixing espresso machines? Want to help people? Don't mind getting pings? Clicking the reaction below will add a role that will allow other people to request your help. You'll be able to be pinged via this role, and you'll get automatically pinged when a help thread hasn't been responded to in 24 hours.

Made a mistake? Hate pings? Just click again to remove the role.`,
					Buttons: []selfroleButton{
						{Label: "Helper", Emoji: "🔧", RoleID: 1020401507121774722},
					},
				},
			},
		}
	case "dev":
		return map[snowflake.ID][]selfroleConfig{
			1013566342345019512: {
				{
					ChannelID: 1019680095893471322,
					Message:   "Click a button below to toggle a role on or off.",
					Buttons: []selfroleButton{
						{Label: "BOTBROS", Emoji: "🤖", RoleID: 1015493549430685706},
					},
				},
			},
		}
	default:
		return nil
	}
}

func selfrolePanelEmbed(cfg selfroleConfig) discord.Embed {
	return discord.Embed{Description: cfg.Message}
}

func selfrolePanelButtons(cfg selfroleConfig) []discord.LayoutComponent {
	var btns []discord.InteractiveComponent
	for _, b := range cfg.Buttons {
		btns = append(btns, discord.ButtonComponent{
			Style:    discord.ButtonStyleSecondary,
			Label:    b.Label,
			CustomID: fmt.Sprintf("selfrole_%d", b.RoleID),
			Emoji:    &discord.ComponentEmoji{Name: b.Emoji},
		})
	}
	return []discord.LayoutComponent{discord.NewActionRow(btns...)}
}

func (b *Bot) ensureSelfrolePanels() {
	configs := getSelfroleConfig(b.Env)
	if configs == nil {
		return
	}

	for guildID, cfgs := range configs {
		for _, cfg := range cfgs {
			if cfg.ChannelID == 0 {
				continue
			}
			b.ensureSelfrolePanel(guildID, cfg)
		}
	}
}

func (b *Bot) ensureSelfrolePanel(guildID snowflake.ID, cfg selfroleConfig) {
	messages, err := b.Client.Rest.GetMessages(cfg.ChannelID, 0, 0, 0, 25)
	if err != nil {
		b.Log.Error("Failed to fetch messages for selfrole panel", "guild_id", guildID, "channel_id", cfg.ChannelID, "error", err)
		return
	}

	embed := selfrolePanelEmbed(cfg)
	components := selfrolePanelButtons(cfg)

	for _, msg := range messages {
		if msg.Author.ID != b.Client.ApplicationID {
			continue
		}
		if len(msg.Embeds) == 1 && msg.Embeds[0].Description == embed.Description {
			if !selfrolePanelNeedsUpdate(msg, cfg) {
				b.Log.Info("Selfrole panel already exists", "guild_id", guildID, "channel_id", cfg.ChannelID)
				return
			}
			b.Log.Info("Selfrole panel outdated, updating", "guild_id", guildID, "channel_id", cfg.ChannelID)
			content := ""
			embeds := []discord.Embed{embed}
			_, err := b.Client.Rest.UpdateMessage(cfg.ChannelID, msg.ID, discord.MessageUpdate{
				Content:    &content,
				Embeds:     &embeds,
				Components: &components,
			})
			if err != nil {
				b.Log.Error("Failed to update selfrole panel", "guild_id", guildID, "error", err)
			}
			return
		}
	}

	_, err = b.Client.Rest.CreateMessage(cfg.ChannelID, discord.MessageCreate{
		Embeds:     []discord.Embed{embed},
		Components: components,
	})
	if err != nil {
		b.Log.Error("Failed to post selfrole panel", "guild_id", guildID, "channel_id", cfg.ChannelID, "error", err)
	} else {
		b.Log.Info("Posted selfrole panel", "guild_id", guildID, "channel_id", cfg.ChannelID)
	}
}

func selfrolePanelNeedsUpdate(msg discord.Message, cfg selfroleConfig) bool {
	if msg.Content != "" {
		return true
	}

	wantButtons := selfrolePanelButtons(cfg)
	if len(msg.Components) != len(wantButtons) {
		return true
	}

	wantRow, ok := wantButtons[0].(discord.ActionRowComponent)
	if !ok {
		return true
	}
	gotRow, ok := msg.Components[0].(discord.ActionRowComponent)
	if !ok {
		return true
	}
	if len(gotRow.Components) != len(wantRow.Components) {
		return true
	}
	for i, c := range gotRow.Components {
		gotBtn, ok := c.(discord.ButtonComponent)
		if !ok {
			return true
		}
		wantBtn, ok := wantRow.Components[i].(discord.ButtonComponent)
		if !ok {
			return true
		}
		if gotBtn.Label != wantBtn.Label || gotBtn.Style != wantBtn.Style || gotBtn.CustomID != wantBtn.CustomID {
			return true
		}
		if gotBtn.Emoji == nil || gotBtn.Emoji.Name != wantBtn.Emoji.Name {
			return true
		}
	}
	return false
}

func (b *Bot) handleSelfroleToggle(e *events.ComponentInteractionCreate, roleID snowflake.ID) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	label := selfroleLabel(*guildID, roleID, b.Env)

	hasRole := false
	if e.Member() != nil {
		for _, r := range e.Member().RoleIDs {
			if r == roleID {
				hasRole = true
				break
			}
		}
	}

	if hasRole {
		if err := b.Client.Rest.RemoveMemberRole(*guildID, e.User().ID, roleID); err != nil {
			b.Log.Error("Failed to remove selfrole", "user_id", e.User().ID, "role_id", roleID, "error", err)
			e.CreateMessage(discord.MessageCreate{
				Content: "Something went wrong, please try again.",
				Flags:   discord.MessageFlagEphemeral,
			})
			return
		}
		e.CreateMessage(discord.MessageCreate{
			Content: fmt.Sprintf("Removed **%s**", label),
			Flags:   discord.MessageFlagEphemeral,
		})
	} else {
		if err := b.Client.Rest.AddMemberRole(*guildID, e.User().ID, roleID); err != nil {
			b.Log.Error("Failed to add selfrole", "user_id", e.User().ID, "role_id", roleID, "error", err)
			e.CreateMessage(discord.MessageCreate{
				Content: "Something went wrong, please try again.",
				Flags:   discord.MessageFlagEphemeral,
			})
			return
		}
		e.CreateMessage(discord.MessageCreate{
			Content: fmt.Sprintf("Added **%s**", label),
			Flags:   discord.MessageFlagEphemeral,
		})
	}
}

func selfroleLabel(guildID, roleID snowflake.ID, env string) string {
	configs := getSelfroleConfig(env)
	if configs == nil {
		return "role"
	}
	for _, cfgs := range configs {
		for _, cfg := range cfgs {
			for _, btn := range cfg.Buttons {
				if btn.RoleID == roleID {
					return btn.Label
				}
			}
		}
	}
	return "role"
}

func isSelfroleInteraction(customID string) (snowflake.ID, bool) {
	if !strings.HasPrefix(customID, "selfrole_") {
		return 0, false
	}
	idStr := strings.TrimPrefix(customID, "selfrole_")
	id, err := snowflake.Parse(idStr)
	if err != nil {
		return 0, false
	}
	return id, true
}
