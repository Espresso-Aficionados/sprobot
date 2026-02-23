package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

type posterRoleConfig struct {
	RoleID       snowflake.ID
	Threshold    int
	SkipChannels map[snowflake.ID]bool
}

func getPosterRoleConfig(env string) map[snowflake.ID]posterRoleConfig {
	thresholdStr := os.Getenv("SPROBOT_POSTER_ROLE_THRESHOLD")
	if thresholdStr == "" {
		slog.Info("SPROBOT_POSTER_ROLE_THRESHOLD not set, poster role disabled")
		return nil
	}
	threshold, err := strconv.Atoi(thresholdStr)
	if err != nil || threshold <= 0 {
		slog.Error("Invalid SPROBOT_POSTER_ROLE_THRESHOLD", "value", thresholdStr)
		return nil
	}

	switch env {
	case "prod":
		return map[snowflake.ID]posterRoleConfig{
			726985544038612993: {
				RoleID:       1367728202885365821,
				Threshold:    threshold,
				SkipChannels: map[snowflake.ID]bool{},
			},
		}
	case "dev":
		return map[snowflake.ID]posterRoleConfig{
			1013566342345019512: {
				RoleID:       1475379099307343882,
				Threshold:    threshold,
				SkipChannels: map[snowflake.ID]bool{},
			},
		}
	default:
		return nil
	}
}

type posterRoleState struct {
	mu        sync.Mutex
	Tracked   map[string]int `json:"tracked"`
	History   map[string]int `json:"history"`
	searching map[string]bool
}

func (b *Bot) checkPosterRole(guildID snowflake.ID, channelID snowflake.ID, msg discord.Message) {
	configs := b.posterRoleConfig
	cfg, ok := configs[guildID]
	if !ok {
		return
	}

	if cfg.SkipChannels[channelID] {
		return
	}

	if msg.Member == nil {
		return
	}
	for _, roleID := range msg.Member.RoleIDs {
		if roleID == cfg.RoleID {
			return
		}
	}

	userID := msg.Author.ID
	userIDStr := fmt.Sprintf("%d", userID)

	st := b.posterRole[guildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	st.Tracked[userIDStr]++
	tracked := st.Tracked[userIDStr]

	history, hasHistory := st.History[userIDStr]
	isSearching := st.searching[userIDStr]

	if !hasHistory && !isSearching {
		st.searching[userIDStr] = true
		st.mu.Unlock()
		go b.searchAndGrantPosterRole(guildID, userID, userIDStr, cfg)
		return
	}
	st.mu.Unlock()

	if hasHistory && tracked+history >= cfg.Threshold {
		if err := b.Client.Rest.AddMemberRole(guildID, userID, cfg.RoleID); err != nil {
			b.Log.Error("Failed to grant poster role", "user_id", userID, "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Granted poster role", "user_id", userID, "guild_id", guildID, "total", tracked+history)
		}
	}
}

type discordSearchResponse struct {
	TotalResults int `json:"total_results"`
}

func (b *Bot) searchAndGrantPosterRole(guildID snowflake.ID, userID snowflake.ID, userIDStr string, cfg posterRoleConfig) {
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in poster role search", "error", r)
		}
	}()

	url := fmt.Sprintf("https://discord.com/api/v10/guilds/%d/messages/search?author_id=%d", guildID, userID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		b.Log.Error("Failed to create search request", "user_id", userID, "error", err)
		b.clearPosterRoleSearching(guildID, userIDStr)
		return
	}
	req.Header.Set("Authorization", "Bot "+b.Client.Token)

	resp, err := b.searchClient.Do(req)
	if err != nil {
		b.Log.Error("Failed to execute search request", "user_id", userID, "error", err)
		b.clearPosterRoleSearching(guildID, userIDStr)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		resp.Body.Close()
		b.Log.Info("Search index not ready, will retry on next message", "user_id", userID)
		b.clearPosterRoleSearching(guildID, userIDStr)
		return
	}
	if resp.StatusCode != http.StatusOK {
		b.Log.Error("Search API returned non-200", "user_id", userID, "status", resp.StatusCode)
		b.clearPosterRoleSearching(guildID, userIDStr)
		return
	}

	var result discordSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		b.Log.Error("Failed to decode search response", "user_id", userID, "error", err)
		b.clearPosterRoleSearching(guildID, userIDStr)
		return
	}

	st := b.posterRole[guildID]
	if st == nil {
		return
	}

	st.mu.Lock()
	st.History[userIDStr] = result.TotalResults
	st.Tracked[userIDStr] = 0
	delete(st.searching, userIDStr)
	st.mu.Unlock()

	b.Log.Info("Cached historical post count", "user_id", userID, "guild_id", guildID, "count", result.TotalResults)

	if result.TotalResults >= cfg.Threshold {
		if err := b.Client.Rest.AddMemberRole(guildID, userID, cfg.RoleID); err != nil {
			b.Log.Error("Failed to grant poster role", "user_id", userID, "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Granted poster role", "user_id", userID, "guild_id", guildID, "total", result.TotalResults)
		}
	}
}

func (b *Bot) clearPosterRoleSearching(guildID snowflake.ID, userIDStr string) {
	st := b.posterRole[guildID]
	if st == nil {
		return
	}
	st.mu.Lock()
	delete(st.searching, userIDStr)
	st.mu.Unlock()
}

func (b *Bot) loadPosterRole() {
	configs := b.posterRoleConfig
	if configs == nil {
		return
	}

	ctx := context.Background()
	for guildID := range configs {
		st := &posterRoleState{
			Tracked:   make(map[string]int),
			History:   make(map[string]int),
			searching: make(map[string]bool),
		}

		data, err := b.S3.FetchGuildJSON(ctx, "posterroles", fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			b.Log.Info("No existing poster role data, starting fresh", "guild_id", guildID)
		} else if err != nil {
			b.Log.Error("Failed to load poster role data", "guild_id", guildID, "error", err)
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				b.Log.Error("Failed to decode poster role data", "guild_id", guildID, "error", err)
			}
			if st.Tracked == nil {
				st.Tracked = make(map[string]int)
			}
			if st.History == nil {
				st.History = make(map[string]int)
			}
			st.searching = make(map[string]bool)
		}

		b.posterRole[guildID] = st
		b.Log.Info("Loaded poster role state", "guild_id", guildID, "tracked", len(st.Tracked), "history", len(st.History))
	}
}

func (b *Bot) savePosterRole() {
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in poster role save", "error", r)
		}
	}()

	ctx := context.Background()
	for guildID, st := range b.posterRole {
		st.mu.Lock()
		data, err := json.Marshal(st)
		st.mu.Unlock()

		if err != nil {
			b.Log.Error("Failed to marshal poster role data", "guild_id", guildID, "error", err)
			continue
		}

		if err := b.S3.SaveGuildJSON(ctx, "posterroles", fmt.Sprintf("%d", guildID), data); err != nil {
			b.Log.Error("Failed to save poster role data", "guild_id", guildID, "error", err)
		} else {
			b.Log.Info("Saved poster role state", "guild_id", guildID)
		}
	}
}
