package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

func (b *Bot) loadTemplates() {
	ctx := context.Background()

	guildIDs, err := b.S3.ListGuildJSONKeys(ctx, "config_templates")
	if err != nil {
		b.Log.Error("Failed to list template configs from S3", "error", err)
	}

	for _, gidStr := range guildIDs {
		gid, err := snowflake.Parse(gidStr)
		if err != nil {
			b.Log.Error("Invalid guild ID in template config", "value", gidStr, "error", err)
			continue
		}

		data, err := b.S3.FetchTemplates(ctx, gidStr)
		if err != nil {
			if errors.Is(err, s3client.ErrNotFound) {
				continue
			}
			b.Log.Error("Failed to load templates from S3", "guild_id", gidStr, "error", err)
			continue
		}

		var tmpls []sprobot.Template
		if err := json.Unmarshal(data, &tmpls); err != nil {
			b.Log.Error("Failed to decode templates", "guild_id", gidStr, "error", err)
			continue
		}

		b.templates[gid] = tmpls
		b.Log.Info("Loaded templates from S3", "guild_id", gid, "count", len(tmpls))
	}

	// Fallback: if S3 had nothing, use hardcoded templates for backward compatibility
	if len(b.templates) == 0 {
		hardcoded := sprobot.AllTemplates(b.Env)
		if hardcoded != nil {
			for gid, tmpls := range hardcoded {
				b.templates[gid] = tmpls
			}
			b.Log.Info("Using hardcoded templates as fallback", "guilds", len(b.templates))
		}
	}
}

func (b *Bot) loadSelfroles() {
	ctx := context.Background()

	guildIDs, err := b.S3.ListGuildJSONKeys(ctx, "config_selfroles")
	if err != nil {
		b.Log.Error("Failed to list selfrole configs from S3", "error", err)
	}

	for _, gidStr := range guildIDs {
		gid, err := snowflake.Parse(gidStr)
		if err != nil {
			b.Log.Error("Invalid guild ID in selfrole config", "value", gidStr, "error", err)
			continue
		}

		data, err := b.S3.FetchSelfroles(ctx, gidStr)
		if err != nil {
			if errors.Is(err, s3client.ErrNotFound) {
				continue
			}
			b.Log.Error("Failed to load selfroles from S3", "guild_id", gidStr, "error", err)
			continue
		}

		var cfgs []selfroleConfig
		if err := json.Unmarshal(data, &cfgs); err != nil {
			b.Log.Error("Failed to decode selfroles", "guild_id", gidStr, "error", err)
			continue
		}

		b.selfroles[gid] = cfgs
		b.Log.Info("Loaded selfroles from S3", "guild_id", gid, "panels", len(cfgs))
	}

	if len(b.selfroles) == 0 {
		hardcoded := getSelfroleConfig(b.Env)
		if hardcoded != nil {
			for gid, cfgs := range hardcoded {
				b.selfroles[gid] = cfgs
			}
			b.Log.Info("Using hardcoded selfroles as fallback", "guilds", len(b.selfroles))
		}
	}
}

func (b *Bot) loadTicketConfigs() {
	ctx := context.Background()

	guildIDs, err := b.S3.ListGuildJSONKeys(ctx, "config_tickets")
	if err != nil {
		b.Log.Error("Failed to list ticket configs from S3", "error", err)
	}

	for _, gidStr := range guildIDs {
		gid, err := snowflake.Parse(gidStr)
		if err != nil {
			b.Log.Error("Invalid guild ID in ticket config", "value", gidStr, "error", err)
			continue
		}

		data, err := b.S3.FetchTicketConfig(ctx, gidStr)
		if err != nil {
			if errors.Is(err, s3client.ErrNotFound) {
				continue
			}
			b.Log.Error("Failed to load ticket config from S3", "guild_id", gidStr, "error", err)
			continue
		}

		var cfg ticketConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			b.Log.Error("Failed to decode ticket config", "guild_id", gidStr, "error", err)
			continue
		}

		b.ticketConfigs[gid] = cfg
		b.Log.Info("Loaded ticket config from S3", "guild_id", gid)
	}

	if len(b.ticketConfigs) == 0 {
		hardcoded := getTicketConfig(b.Env)
		if hardcoded != nil {
			for gid, cfg := range hardcoded {
				b.ticketConfigs[gid] = cfg
			}
			b.Log.Info("Using hardcoded ticket configs as fallback", "guilds", len(b.ticketConfigs))
		}
	}
}

func (b *Bot) handleConfig(e *events.ApplicationCommandInteractionCreate) {
	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		return
	}

	subCmd := data.SubCommandName
	if subCmd == nil {
		return
	}

	switch *subCmd {
	case "profiles":
		b.handleConfigProfiles(e)
	case "selfroles":
		b.handleConfigSelfroles(e)
	case "tickets":
		b.handleConfigTickets(e)
	}
}

func (b *Bot) handleConfigProfiles(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.Info("Config profiles", "user_id", e.User().ID, "guild_id", *guildID)

	url := fmt.Sprintf("%sadmin/%d/profiles", sprobot.WebEndpointForEnv(b.Env), *guildID)
	botutil.RespondEphemeral(e, fmt.Sprintf("Configure profile templates for this server:\n%s", url))
}

func (b *Bot) handleConfigSelfroles(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.Info("Config selfroles", "user_id", e.User().ID, "guild_id", *guildID)

	url := fmt.Sprintf("%sadmin/%d/selfroles", sprobot.WebEndpointForEnv(b.Env), *guildID)
	botutil.RespondEphemeral(e, fmt.Sprintf("Configure self-assign roles for this server:\n%s", url))
}

func (b *Bot) handleConfigTickets(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.Info("Config tickets", "user_id", e.User().ID, "guild_id", *guildID)

	url := fmt.Sprintf("%sadmin/%d/tickets", sprobot.WebEndpointForEnv(b.Env), *guildID)
	botutil.RespondEphemeral(e, fmt.Sprintf("Configure ticket system for this server:\n%s", url))
}
