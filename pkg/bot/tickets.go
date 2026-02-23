package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

type ticketConfig struct {
	ChannelID        snowflake.ID
	StaffRoleID      snowflake.ID
	CounterOffset    int
	PanelMessage     string
	PanelButtonLabel string
	TicketIntro      string // supports %s for user mention
	CloseButtonLabel string
}

func getTicketConfig(env string) map[snowflake.ID]ticketConfig {
	switch env {
	case "prod":
		return map[snowflake.ID]ticketConfig{
			726985544038612993: {
				ChannelID:        0, // TODO: fill in
				StaffRoleID:      0, // TODO: fill in
				CounterOffset:    0,
				PanelMessage:     "Need help? Click the button below to open a private ticket.",
				PanelButtonLabel: "Open Ticket",
				TicketIntro:      "Hello %s! A staff member will be with you shortly.",
				CloseButtonLabel: "Close Ticket",
			},
		}
	case "dev":
		return map[snowflake.ID]ticketConfig{
			1013566342345019512: {
				ChannelID:        1475318848956661921, // TODO: fill in
				StaffRoleID:      1015493549430685706, // TODO: fill in
				CounterOffset:    40,
				PanelMessage:     "Need help? Click the button below to open a private ticket.",
				PanelButtonLabel: "Open Ticket",
				TicketIntro:      "Hello %s! A staff member will be with you shortly.",
				CloseButtonLabel: "Close Ticket",
			},
		}
	default:
		return nil
	}
}

type ticketState struct {
	mu      sync.Mutex
	Counter int `json:"counter"`
}

func (b *Bot) loadTickets() {
	configs := getTicketConfig(b.Env)
	if configs == nil {
		return
	}

	ctx := context.Background()
	for guildID := range configs {
		st := &ticketState{Counter: 1}

		data, err := b.S3.FetchGuildJSON(ctx, "tickets", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing ticket data, starting fresh", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load ticket data", "guild_id", guildID, "error", err)
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode ticket data", "guild_id", guildID, "error", err)
			}
			if st.Counter < 1 {
				st.Counter = 1
			}
		}

		b.tickets[guildID] = st
		b.Log.Info("Loaded ticket state", "guild_id", guildID, "counter", st.Counter)
	}
}

func (b *Bot) saveTickets() {
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in ticket save", "error", r)
		}
	}()

	ctx := context.Background()
	for guildID, st := range b.tickets {
		st.mu.Lock()
		data, err := json.Marshal(st)
		st.mu.Unlock()

		if err != nil {
			b.Log.Error("Failed to marshal ticket data", "guild_id", guildID, "error", err)
			continue
		}

		if err := b.S3.SaveGuildJSON(ctx, "tickets", fmt.Sprintf("%d", guildID), data); err != nil {
			b.Log.Error("Failed to save ticket data", "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Saved ticket state", "guild_id", guildID)
		}
	}
}

func (b *Bot) ensureTicketPanels() {
	configs := getTicketConfig(b.Env)
	if configs == nil {
		return
	}

	for guildID, cfg := range configs {
		if cfg.ChannelID == 0 {
			continue
		}
		b.ensureTicketPanel(guildID, cfg)
	}
}

func ticketPanelEmbed(cfg ticketConfig) discord.Embed {
	return discord.Embed{Description: cfg.PanelMessage}
}

func ticketPanelButton(cfg ticketConfig) discord.ButtonComponent {
	return discord.ButtonComponent{
		Style:    discord.ButtonStylePrimary,
		Label:    cfg.PanelButtonLabel,
		CustomID: "ticket_open",
		Emoji:    &discord.ComponentEmoji{Name: "🎫"},
	}
}

func (b *Bot) panelNeedsUpdate(msg discord.Message, cfg ticketConfig) bool {
	if msg.Content != "" {
		return true
	}

	wantEmbed := ticketPanelEmbed(cfg)
	if len(msg.Embeds) != 1 || msg.Embeds[0].Description != wantEmbed.Description {
		return true
	}

	wantBtn := ticketPanelButton(cfg)
	if len(msg.Components) != 1 {
		return true
	}
	row, ok := msg.Components[0].(discord.ActionRowComponent)
	if !ok || len(row.Components) != 1 {
		return true
	}
	btn, ok := row.Components[0].(discord.ButtonComponent)
	if !ok {
		return true
	}
	if btn.Label != wantBtn.Label || btn.Style != wantBtn.Style {
		return true
	}
	if btn.Emoji == nil || btn.Emoji.Name != wantBtn.Emoji.Name {
		return true
	}
	return false
}

func (b *Bot) ensureTicketPanel(guildID snowflake.ID, cfg ticketConfig) {
	messages, err := b.Client.Rest.GetMessages(cfg.ChannelID, 0, 0, 0, 25)
	if err != nil {
		b.Log.Error("Failed to fetch messages for ticket panel", "guild_id", guildID, "channel_id", cfg.ChannelID, "error", err)
		return
	}

	embed := ticketPanelEmbed(cfg)
	components := []discord.LayoutComponent{
		discord.NewActionRow(ticketPanelButton(cfg)),
	}

	for _, msg := range messages {
		if msg.Author.ID != b.Client.ApplicationID {
			continue
		}
		for _, comp := range msg.Components {
			row, ok := comp.(discord.ActionRowComponent)
			if !ok {
				continue
			}
			for _, c := range row.Components {
				btn, ok := c.(discord.ButtonComponent)
				if !ok {
					continue
				}
				if btn.CustomID == "ticket_open" {
					if !b.panelNeedsUpdate(msg, cfg) {
						b.Log.Info("Ticket panel already exists", "guild_id", guildID, "channel_id", cfg.ChannelID)
						return
					}
					b.Log.Info("Ticket panel outdated, updating", "guild_id", guildID, "channel_id", cfg.ChannelID)
					content := ""
					embeds := []discord.Embed{embed}
					_, err := b.Client.Rest.UpdateMessage(cfg.ChannelID, msg.ID, discord.MessageUpdate{
						Content:    &content,
						Embeds:     &embeds,
						Components: &components,
					})
					if err != nil {
						b.Log.Error("Failed to update ticket panel", "guild_id", guildID, "error", err)
					}
					return
				}
			}
		}
	}

	_, err = b.Client.Rest.CreateMessage(cfg.ChannelID, discord.MessageCreate{
		Embeds:     []discord.Embed{embed},
		Components: components,
	})
	if err != nil {
		b.Log.Error("Failed to post ticket panel", "guild_id", guildID, "channel_id", cfg.ChannelID, "error", err)
	} else {
		b.Log.Info("Posted ticket panel", "guild_id", guildID, "channel_id", cfg.ChannelID)
	}
}

func (b *Bot) handleTicketOpen(e *events.ComponentInteractionCreate) {
	if err := e.DeferCreateMessage(true); err != nil {
		b.Log.Error("Failed to defer ticket open", "error", err)
		return
	}

	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	configs := getTicketConfig(b.Env)
	cfg, ok := configs[*guildID]
	if !ok {
		return
	}

	st := b.tickets[*guildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	ticketNum := st.Counter + cfg.CounterOffset
	st.Counter++
	st.mu.Unlock()

	// Save counter immediately
	data, err := json.Marshal(st)
	if err != nil {
		b.Log.Error("Failed to marshal ticket data", "guild_id", *guildID, "error", err)
	} else {
		if err := b.S3.SaveGuildJSON(context.Background(), "tickets", fmt.Sprintf("%d", *guildID), data); err != nil {
			b.Log.Error("Failed to save ticket data", "guild_id", *guildID, "error", err)
		}
	}

	threadName := fmt.Sprintf("ticket-%d", ticketNum)
	invitable := false
	thread, err := b.Client.Rest.CreateThread(cfg.ChannelID, discord.GuildPrivateThreadCreate{
		Name:      threadName,
		Invitable: &invitable,
	})
	if err != nil {
		b.Log.Error("Failed to create ticket thread", "guild_id", *guildID, "error", err)
		b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
			Content: "Something went wrong creating your ticket. Please try again.",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	userID := e.User().ID
	if err := b.Client.Rest.AddThreadMember(thread.ID(), userID); err != nil {
		b.Log.Error("Failed to add user to ticket thread", "user_id", userID, "thread_id", thread.ID(), "error", err)
	}

	intro := fmt.Sprintf(cfg.TicketIntro, fmt.Sprintf("<@%d>", userID))
	_, err = b.Client.Rest.CreateMessage(thread.ID(), discord.MessageCreate{
		Content: fmt.Sprintf("||<@&%d>||", cfg.StaffRoleID),
		Embeds:  []discord.Embed{{Description: intro}},
		Components: []discord.LayoutComponent{
			discord.NewActionRow(
				discord.ButtonComponent{
					Style:    discord.ButtonStyleDanger,
					Label:    cfg.CloseButtonLabel,
					CustomID: "ticket_close",
					Emoji:    &discord.ComponentEmoji{Name: "🔒"},
				},
			),
		},
	})
	if err != nil {
		b.Log.Error("Failed to post intro in ticket thread", "thread_id", thread.ID(), "error", err)
	}

	b.Client.Rest.CreateFollowupMessage(b.Client.ApplicationID, e.Token(), discord.MessageCreate{
		Content: fmt.Sprintf("Your ticket has been created: <#%d>", thread.ID()),
		Flags:   discord.MessageFlagEphemeral,
	})

	b.Log.Info("Opened ticket", "guild_id", *guildID, "thread_id", thread.ID(), "user_id", userID, "ticket_num", ticketNum)
}

func (b *Bot) handleTicketCloseConfirm(e *events.ComponentInteractionCreate) {
	e.UpdateMessage(discord.MessageUpdate{
		Embeds: &[]discord.Embed{{Description: "Are you sure you want to close this ticket?"}},
		Components: &[]discord.LayoutComponent{
			discord.NewActionRow(
				discord.NewDangerButton("Yes, close it", "ticket_close_yes"),
				discord.NewSecondaryButton("Cancel", "ticket_close_no"),
			),
		},
	})
}

func (b *Bot) handleTicketCloseCancel(e *events.ComponentInteractionCreate) {
	guildID := e.GuildID()
	cfg := ticketConfig{
		CloseButtonLabel: "Close Ticket",
		TicketIntro:      "Ticket",
	}
	if guildID != nil {
		configs := getTicketConfig(b.Env)
		if c, ok := configs[*guildID]; ok {
			cfg = c
		}
	}

	// Restore the intro embed with close button (we can't recover the original user mention,
	// so just show a generic message)
	e.UpdateMessage(discord.MessageUpdate{
		Embeds: &[]discord.Embed{{Description: "Use the button below when you're ready to close this ticket."}},
		Components: &[]discord.LayoutComponent{
			discord.NewActionRow(
				discord.ButtonComponent{
					Style:    discord.ButtonStyleDanger,
					Label:    cfg.CloseButtonLabel,
					CustomID: "ticket_close",
					Emoji:    &discord.ComponentEmoji{Name: "🔒"},
				},
			),
		},
	})
}

func (b *Bot) handleTicketClose(e *events.ComponentInteractionCreate) {
	e.UpdateMessage(discord.MessageUpdate{
		Embeds:     &[]discord.Embed{{Description: "Ticket closed."}},
		Components: &[]discord.LayoutComponent{},
	})

	channelID := e.Channel().ID()
	archived := true
	locked := true
	if _, err := b.Client.Rest.UpdateChannel(channelID, discord.GuildThreadUpdate{
		Archived: &archived,
		Locked:   &locked,
	}); err != nil {
		b.Log.Error("Failed to archive/lock ticket thread", "channel_id", channelID, "error", err)
	} else {
		b.Log.Info("Closed ticket", "channel_id", channelID, "user_id", e.User().ID)
	}
}
