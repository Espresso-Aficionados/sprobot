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
	}
}

func (b *Bot) handleConfigProfiles(e *events.ApplicationCommandInteractionCreate) {
	guildID := e.GuildID()
	if guildID == nil {
		return
	}

	b.Log.Info("Config profiles", "user_id", e.User().ID, "guild_id", *guildID)

	url := fmt.Sprintf("%sadmin/%d/profiles", sprobot.WebEndpoint, *guildID)
	botutil.RespondEphemeral(e, fmt.Sprintf("Configure profile templates for this server:\n%s", url))
}
