package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
)

// errNotLoaded is returned by persist for a guild whose state failed to load.
var errNotLoaded = errors.New("state not loaded from S3 yet")

// guildStateStore provides generic per-guild state management backed by S3.
// T is the state type; it must have a sync.Mutex field accessible via getMu.
//
// A guild whose state fails to load has no state until retryFailedLoads
// succeeds: get and getOrCreate return nil, set is refused and persist fails,
// so fresh state is never saved over the real data.
type guildStateStore[T any] struct {
	s3Key     string
	getMu     func(*T) *sync.Mutex
	newState  func() *T
	postLoad  func(*T)               // called after successful unmarshal
	onMissing func(snowflake.ID, *T) // called on fresh state: S3 key not found, or its data undecodable
	s3        *s3client.Client
	log       *slog.Logger

	mu        sync.RWMutex
	states    map[snowflake.ID]*T
	failed    map[snowflake.ID]bool        // load failed; retried by retryFailedLoads
	persistMu map[snowflake.ID]*sync.Mutex // held across marshal and PUT, so saves land in order
}

func newGuildStateStore[T any](
	s3 *s3client.Client,
	log *slog.Logger,
	s3Key string,
	newState func() *T,
	getMu func(*T) *sync.Mutex,
) *guildStateStore[T] {
	return &guildStateStore[T]{
		s3Key:     s3Key,
		states:    make(map[snowflake.ID]*T),
		failed:    make(map[snowflake.ID]bool),
		persistMu: make(map[snowflake.ID]*sync.Mutex),
		getMu:     getMu,
		newState:  newState,
		s3:        s3,
		log:       log,
	}
}

func (s *guildStateStore[T]) get(guildID snowflake.ID) *T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.states[guildID]
}

// set replaces the guild's state, unless its load failed.
func (s *guildStateStore[T]) set(guildID snowflake.ID, st *T) {
	s.mu.Lock()
	failed := s.failed[guildID]
	if !failed {
		s.states[guildID] = st
	}
	s.mu.Unlock()
	if failed {
		s.log.Error("Refusing to replace "+s.s3Key+" state that failed to load", "guild_id", guildID)
	}
}

// getOrCreate returns the guild's state, creating it with newState if there
// is none. It returns nil if the guild's load failed.
func (s *guildStateStore[T]) getOrCreate(guildID snowflake.ID) *T {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed[guildID] {
		return nil
	}
	st := s.states[guildID]
	if st == nil {
		st = s.newState()
		s.states[guildID] = st
	}
	return st
}

func (s *guildStateStore[T]) persistLock(guildID snowflake.ID) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.persistMu[guildID]
	if l == nil {
		l = &sync.Mutex{}
		s.persistMu[guildID] = l
	}
	return l
}

func (s *guildStateStore[T]) persist(guildID snowflake.ID) error {
	lock := s.persistLock(guildID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.RLock()
	st, failed := s.states[guildID], s.failed[guildID]
	s.mu.RUnlock()
	if failed {
		return errNotLoaded
	}
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
	return s.s3.SaveGuildJSON(context.Background(), s.s3Key, guildID.String(), data)
}

func (s *guildStateStore[T]) save() {
	s.mu.RLock()
	guildIDs := slices.Collect(maps.Keys(s.states))
	s.mu.RUnlock()
	for _, guildID := range guildIDs {
		if err := s.persist(guildID); err != nil {
			s.log.Error("Failed to save "+s.s3Key, "guild_id", guildID, "error", err)
		}
	}
}

// load fetches each guild's state from S3. A guild that fails to load is
// left without state and marked for retryFailedLoads.
func (s *guildStateStore[T]) load(guildIDs []snowflake.ID) {
	for _, guildID := range guildIDs {
		st, err := s.fetch(guildID)
		s.mu.Lock()
		retried := s.failed[guildID]
		if err != nil {
			s.failed[guildID] = true
		} else {
			s.states[guildID] = st
			delete(s.failed, guildID)
		}
		s.mu.Unlock()

		switch {
		case err != nil:
			s.log.Error("Failed to load "+s.s3Key+", will retry", "guild_id", guildID, "error", err)
		case retried:
			s.log.Info("Loaded "+s.s3Key+" after an earlier failure", "guild_id", guildID)
		}
	}
}

// retryFailedLoads loads again every guild whose load failed.
func (s *guildStateStore[T]) retryFailedLoads() {
	s.mu.RLock()
	guildIDs := slices.Collect(maps.Keys(s.failed))
	s.mu.RUnlock()
	s.load(guildIDs)
}

// fetch reads a guild's state from S3. A missing key gives fresh state.
// Undecodable data is copied to "<s3Key>-corrupt/" and then also replaced by
// fresh state, so the feature keeps working; recovering it is left to a human.
func (s *guildStateStore[T]) fetch(guildID snowflake.ID) (*T, error) {
	ctx := context.Background()
	data, err := s.s3.FetchGuildJSON(ctx, s.s3Key, guildID.String())
	if errors.Is(err, s3client.ErrNotFound) {
		s.log.Info("No existing "+s.s3Key+" data, starting fresh", "guild_id", guildID)
		return s.fresh(guildID), nil
	}
	if err != nil {
		return nil, err
	}

	st := s.newState()
	if decodeErr := json.Unmarshal(data, st); decodeErr != nil {
		backupPrefix := s.s3Key + "-corrupt"
		backupID := fmt.Sprintf("%d-%d", guildID, time.Now().Unix())
		if err := s.s3.SaveGuildJSON(ctx, backupPrefix, backupID, data); err != nil {
			return nil, fmt.Errorf("decoding: %w; backing up: %w", decodeErr, err)
		}
		s.log.Error("Failed to decode "+s.s3Key+", backed it up and starting fresh",
			"guild_id", guildID, "backup", backupPrefix+"/"+backupID+".json", "error", decodeErr)
		return s.fresh(guildID), nil
	}
	if s.postLoad != nil {
		s.postLoad(st)
	}
	return st, nil
}

func (s *guildStateStore[T]) fresh(guildID snowflake.ID) *T {
	st := s.newState()
	if s.onMissing != nil {
		s.onMissing(guildID, st)
	}
	return st
}

// each calls fn for every guild with state. It holds no store lock while
// calling fn, so fn may use the store.
func (s *guildStateStore[T]) each(fn func(snowflake.ID, *T)) {
	s.mu.RLock()
	states := maps.Clone(s.states)
	s.mu.RUnlock()
	for guildID, st := range states {
		fn(guildID, st)
	}
}
