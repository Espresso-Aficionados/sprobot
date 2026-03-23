package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

// guildStateStore provides generic per-guild state management backed by S3.
// T is the state type; it must have a sync.Mutex field accessible via getMu.
type guildStateStore[T any] struct {
	s3Key     string
	states    map[snowflake.ID]*T
	getMu     func(*T) *sync.Mutex
	newState  func() *T
	postLoad  func(*T)               // called after successful unmarshal
	onMissing func(snowflake.ID, *T) // called when S3 key not found or fetch error
	s3        *s3client.Client
	log       *slog.Logger
}

func newGuildStateStore[T any](
	s3 *s3client.Client,
	log *slog.Logger,
	s3Key string,
	newState func() *T,
	getMu func(*T) *sync.Mutex,
) *guildStateStore[T] {
	return &guildStateStore[T]{
		s3Key:    s3Key,
		states:   make(map[snowflake.ID]*T),
		getMu:    getMu,
		newState: newState,
		s3:       s3,
		log:      log,
	}
}

func (s *guildStateStore[T]) get(guildID snowflake.ID) *T {
	return s.states[guildID]
}

func (s *guildStateStore[T]) set(guildID snowflake.ID, st *T) {
	s.states[guildID] = st
}

func (s *guildStateStore[T]) persist(guildID snowflake.ID) error {
	st := s.states[guildID]
	if st == nil {
		return nil
	}
	mu := s.getMu(st)
	mu.Lock()
	data, err := json.Marshal(st)
	mu.Unlock()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return s.s3.SaveGuildJSON(context.Background(), s.s3Key, fmt.Sprintf("%d", guildID), data)
}

func (s *guildStateStore[T]) save() {
	for guildID := range s.states {
		if err := s.persist(guildID); err != nil {
			s.log.Error("Failed to save "+s.s3Key, "guild_id", guildID, "error", err)
		}
	}
}

func (s *guildStateStore[T]) load(guildIDs []snowflake.ID) {
	ctx := context.Background()
	for _, guildID := range guildIDs {
		st := s.newState()

		data, err := s.s3.FetchGuildJSON(ctx, s.s3Key, fmt.Sprintf("%d", guildID))
		if errors.Is(err, s3client.ErrNotFound) {
			if s.onMissing != nil {
				s.onMissing(guildID, st)
			}
			s.log.Info("No existing "+s.s3Key+" data, starting fresh", "guild_id", guildID)
		} else if err != nil {
			s.log.Error("Failed to load "+s.s3Key, "guild_id", guildID, "error", err)
			if s.onMissing != nil {
				s.onMissing(guildID, st)
			}
		} else {
			if err := json.Unmarshal(data, st); err != nil {
				s.log.Error("Failed to decode "+s.s3Key, "guild_id", guildID, "error", err)
			}
			if s.postLoad != nil {
				s.postLoad(st)
			}
		}

		s.states[guildID] = st
	}
}

func (s *guildStateStore[T]) each(fn func(snowflake.ID, *T)) {
	for guildID, st := range s.states {
		fn(guildID, st)
	}
}
