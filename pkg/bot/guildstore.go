package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/Espresso-Aficionados/sprobot/pkg/s3client"
	"github.com/Espresso-Aficionados/sprobot/pkg/statever"
)

// errNotLoaded is returned by persist for a guild whose state failed to load.
var errNotLoaded = errors.New("state not loaded from S3 yet")

// storeS3Timeout bounds each S3 call a store makes. The SDK's HTTP client has
// no timeout, and persist holds the guild's save lock across its PUT while
// shutdown waits for the save loops, so an unanswered request would otherwise
// block both for good.
var storeS3Timeout = 30 * time.Second

// Stored state that fails to decode is copied to
// corrupt/<s3Key>/<guild>-<unix time>.json for a human to recover, and
// pruneCorruptBackups deletes the copy after corruptRetention, as PRIVACY.md
// states.
const (
	corruptPrefix    = "corrupt"
	corruptRetention = 30 * 24 * time.Hour
)

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

	// guilds, if set, returns the guilds this instance is in; save and each
	// skip every other guild. Every allowed guild is loaded before the gateway
	// says which ones the bot is in, and another deployment sharing the bucket
	// (prod or dev) may be the one changing a guild this one isn't in, so this
	// one's startup copy must never be written back or acted on.
	guilds func() []snowflake.ID

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
func (s *guildStateStore[T]) set(ctx context.Context, guildID snowflake.ID, st *T) {
	s.mu.Lock()
	failed := s.failed[guildID]
	if !failed {
		s.states[guildID] = st
	}
	s.mu.Unlock()
	if failed {
		s.log.ErrorContext(ctx, "Refusing to replace "+s.s3Key+" state that failed to load", "guild_id", guildID)
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

func (s *guildStateStore[T]) setGuilds(guilds func() []snowflake.ID) {
	s.guilds = guilds
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

func (s *guildStateStore[T]) persist(ctx context.Context, guildID snowflake.ID) error {
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
	if err == nil {
		data, err = statever.Stamp(data)
	}
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, storeS3Timeout)
	defer cancel()
	return s.s3.SaveGuildJSON(ctx, s.s3Key, guildID.String(), data)
}

// save persists every guild with state that this instance is in. It logs
// each failure and returns them all.
func (s *guildStateStore[T]) save(ctx context.Context) error {
	var errs []error
	for guildID := range s.served() {
		if err := s.persist(ctx, guildID); err != nil {
			s.log.ErrorContext(ctx, "Failed to save "+s.s3Key, "guild_id", guildID, "error", err)
			errs = append(errs, fmt.Errorf("saving %s: %w", s.s3Key, err))
		}
	}
	return errors.Join(errs...)
}

// load fetches each guild's state from S3. A guild that fails to load is
// left without state and marked for retryFailedLoads. It returns the
// refusals of state a newer build wrote (statever.NewerError), which
// startup exits on.
func (s *guildStateStore[T]) load(ctx context.Context, guildIDs []snowflake.ID) error {
	var refused []error
	for _, guildID := range guildIDs {
		st, err := s.fetch(ctx, guildID)
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
		case statever.IsNewer(err):
			refused = append(refused, err)
			s.log.ErrorContext(ctx, "Refusing "+s.s3Key+" state a newer build wrote; leaving it untouched", "guild_id", guildID, "error", err)
		case err != nil:
			s.log.ErrorContext(ctx, "Failed to load "+s.s3Key+", will retry", "guild_id", guildID, "error", err)
		case retried:
			s.log.InfoContext(ctx, "Loaded "+s.s3Key+" after an earlier failure", "guild_id", guildID)
		}
	}
	return errors.Join(refused...)
}

// retryFailedLoads loads again every guild whose load failed. It fails
// while any guild is still unloaded.
func (s *guildStateStore[T]) retryFailedLoads(ctx context.Context) error {
	s.mu.RLock()
	guildIDs := slices.Collect(maps.Keys(s.failed))
	s.mu.RUnlock()
	_ = s.load(ctx, guildIDs) // refusals are logged; only startup exits on them
	s.mu.RLock()
	unloaded := len(s.failed)
	s.mu.RUnlock()
	if unloaded > 0 {
		return fmt.Errorf("%s: %d guild(s) still not loaded", s.s3Key, unloaded)
	}
	return nil
}

// fetch reads a guild's state from S3. A missing key gives fresh state.
// State a newer build wrote, or with a version that makes no sense, is an
// error: the guild stays unloaded, so nothing saves over it. Undecodable
// data is copied under corruptPrefix and then also replaced by fresh state,
// so the feature keeps working; recovering it is left to a human.
func (s *guildStateStore[T]) fetch(ctx context.Context, guildID snowflake.ID) (*T, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, storeS3Timeout)
	data, err := s.s3.FetchGuildJSON(fetchCtx, s.s3Key, guildID.String())
	cancel()
	if errors.Is(err, s3client.ErrNotFound) {
		s.log.InfoContext(fetchCtx, "No existing "+s.s3Key+" data, starting fresh", "guild_id", guildID)
		return s.fresh(guildID), nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := statever.Of(s.docName(guildID), data); err != nil && !errors.Is(err, statever.ErrCorrupt) {
		return nil, err
	}

	st := s.newState()
	if decodeErr := json.Unmarshal(data, st); decodeErr != nil {
		backupPrefix := corruptPrefix + "/" + s.s3Key
		backupID := fmt.Sprintf("%d-%d", guildID, time.Now().Unix())
		backupCtx, cancel := context.WithTimeout(ctx, storeS3Timeout)
		defer cancel()
		if err := s.s3.SaveGuildJSON(backupCtx, backupPrefix, backupID, data); err != nil {
			return nil, fmt.Errorf("decoding: %w; backing up: %w", decodeErr, err)
		}
		s.log.ErrorContext(backupCtx, "Failed to decode "+s.s3Key+", backed it up and starting fresh",
			"guild_id", guildID, "backup", backupPrefix+"/"+backupID+".json", "error", decodeErr)
		return s.fresh(guildID), nil
	}
	if s.postLoad != nil {
		s.postLoad(st)
	}
	return st, nil
}

// prefix is the S3 prefix the store's objects are under.
func (s *guildStateStore[T]) prefix() string { return s.s3Key }

// docName is the S3 key of a guild's state, as errors name it.
func (s *guildStateStore[T]) docName(guildID snowflake.ID) string {
	return guildDocName(s.s3Key, guildID)
}

func guildDocName(prefix string, guildID snowflake.ID) string {
	return fmt.Sprintf("%s/%d.json", prefix, guildID)
}

func (s *guildStateStore[T]) fresh(guildID snowflake.ID) *T {
	st := s.newState()
	if s.onMissing != nil {
		s.onMissing(guildID, st)
	}
	return st
}

// each calls fn for every guild with state that this instance is in. It
// holds no store lock while calling fn, so fn may use the store.
func (s *guildStateStore[T]) each(fn func(snowflake.ID, *T)) {
	for guildID, st := range s.served() {
		fn(guildID, st)
	}
}

// served returns the state of every guild that this instance is in.
func (s *guildStateStore[T]) served() map[snowflake.ID]*T {
	s.mu.RLock()
	states := maps.Clone(s.states)
	s.mu.RUnlock()
	if s.guilds != nil {
		guildIDs := s.guilds()
		maps.DeleteFunc(states, func(guildID snowflake.ID, _ *T) bool { return !slices.Contains(guildIDs, guildID) })
	}
	return states
}

// pruneCorruptBackups deletes the copies fetch made of undecodable state once
// they are older than corruptRetention. Keys it can't date are left alone.
func pruneCorruptBackups(ctx context.Context, s3 *s3client.Client, log *slog.Logger, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, storeS3Timeout)
	defer cancel()
	objects, err := s3.ListObjects(ctx, corruptPrefix+"/")
	if err != nil {
		log.ErrorContext(ctx, "Failed to list corrupt state backups", "error", err)
		return err
	}
	var expired []string
	for _, o := range objects {
		_, created, _ := strings.Cut(strings.TrimSuffix(path.Base(o.Key), ".json"), "-")
		secs, err := strconv.ParseInt(created, 10, 64)
		if err == nil && now.Sub(time.Unix(secs, 0)) > corruptRetention {
			expired = append(expired, o.Key)
		}
	}
	if len(expired) == 0 {
		return nil
	}
	if err := s3.DeleteKeys(ctx, expired...); err != nil {
		log.ErrorContext(ctx, "Failed to delete expired corrupt state backups", "error", err)
		return err
	}
	log.InfoContext(ctx, "Deleted expired corrupt state backups", "keys", expired)
	return nil
}
