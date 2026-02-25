package bot

import (
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"sync"

	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

const cacheDir = "/sprobot-cache"
const messageCacheFile = "messagecache.json"
const memberCacheFile = "membercache.json"

type cacheKey struct {
	GroupID snowflake.ID
	ID      snowflake.ID
}

var _ cache.GroupedCache[discord.Message] = (*cappedGroupedCache[discord.Message])(nil)
var _ cache.GroupedCache[discord.Member] = (*cappedGroupedCache[discord.Member])(nil)

type cappedGroupedCache[T any] struct {
	mu      sync.RWMutex
	data    map[snowflake.ID]map[snowflake.ID]T
	order   []cacheKey
	head    int
	size    int
	maxSize int
}

func newCappedGroupedCache[T any](maxSize int) *cappedGroupedCache[T] {
	return &cappedGroupedCache[T]{
		data:    make(map[snowflake.ID]map[snowflake.ID]T),
		maxSize: maxSize,
	}
}

func (c *cappedGroupedCache[T]) Get(groupID snowflake.ID, id snowflake.ID) (T, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if group, ok := c.data[groupID]; ok {
		if entity, ok := group[id]; ok {
			return entity, true
		}
	}
	var zero T
	return zero, false
}

func (c *cappedGroupedCache[T]) Put(groupID snowflake.ID, id snowflake.ID, entity T) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if group, ok := c.data[groupID]; ok {
		if _, exists := group[id]; !exists {
			c.order = append(c.order, cacheKey{GroupID: groupID, ID: id})
			c.size++
		}
		group[id] = entity
	} else {
		group = map[snowflake.ID]T{id: entity}
		c.data[groupID] = group
		c.order = append(c.order, cacheKey{GroupID: groupID, ID: id})
		c.size++
	}

	c.evict()
}

func (c *cappedGroupedCache[T]) evict() {
	for c.size > c.maxSize && c.head < len(c.order) {
		key := c.order[c.head]
		c.order[c.head] = cacheKey{} // clear reference
		c.head++
		if group, ok := c.data[key.GroupID]; ok {
			if _, ok := group[key.ID]; ok {
				delete(group, key.ID)
				c.size--
				if len(group) == 0 {
					delete(c.data, key.GroupID)
				}
				break
			}
		}
		// Entry already removed — skip stale and continue loop
	}

	// Compact when the dead prefix is at least half the slice.
	if c.head >= len(c.order)/2 && c.head > 0 {
		remaining := copy(c.order, c.order[c.head:])
		for i := remaining; i < len(c.order); i++ {
			c.order[i] = cacheKey{}
		}
		c.order = c.order[:remaining]
		c.head = 0
	}
}

func (c *cappedGroupedCache[T]) len() int {
	return c.size
}

func (c *cappedGroupedCache[T]) Remove(groupID snowflake.ID, id snowflake.ID) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if group, ok := c.data[groupID]; ok {
		if entity, ok := group[id]; ok {
			delete(group, id)
			c.size--
			if len(group) == 0 {
				delete(c.data, groupID)
			}
			return entity, true
		}
	}
	var zero T
	return zero, false
}

func (c *cappedGroupedCache[T]) GroupRemove(groupID snowflake.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if group, ok := c.data[groupID]; ok {
		c.size -= len(group)
		delete(c.data, groupID)
	}
}

func (c *cappedGroupedCache[T]) RemoveIf(filterFunc cache.GroupedFilterFunc[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for groupID, group := range c.data {
		for id, entity := range group {
			if filterFunc(groupID, entity) {
				delete(group, id)
				c.size--
			}
		}
		if len(group) == 0 {
			delete(c.data, groupID)
		}
	}
}

func (c *cappedGroupedCache[T]) GroupRemoveIf(groupID snowflake.ID, filterFunc cache.GroupedFilterFunc[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if group, ok := c.data[groupID]; ok {
		for id, entity := range group {
			if filterFunc(groupID, entity) {
				delete(group, id)
				c.size--
			}
		}
		if len(group) == 0 {
			delete(c.data, groupID)
		}
	}
}

func (c *cappedGroupedCache[T]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.len()
}

func (c *cappedGroupedCache[T]) GroupLen(groupID snowflake.ID) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if group, ok := c.data[groupID]; ok {
		return len(group)
	}
	return 0
}

func (c *cappedGroupedCache[T]) All() iter.Seq2[snowflake.ID, T] {
	return func(yield func(snowflake.ID, T) bool) {
		c.mu.RLock()
		defer c.mu.RUnlock()
		for groupID, group := range c.data {
			for _, entity := range group {
				if !yield(groupID, entity) {
					return
				}
			}
		}
	}
}

func (c *cappedGroupedCache[T]) GroupAll(groupID snowflake.ID) iter.Seq[T] {
	return func(yield func(T) bool) {
		c.mu.RLock()
		defer c.mu.RUnlock()
		if group, ok := c.data[groupID]; ok {
			for _, entity := range group {
				if !yield(entity) {
					return
				}
			}
		}
	}
}

// snapshot returns all cached entities for persistence.
func (c *cappedGroupedCache[T]) snapshot() []T {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var items []T
	for _, group := range c.data {
		for _, entity := range group {
			items = append(items, entity)
		}
	}
	return items
}

// load populates the cache from a slice of entities.
// keyFn extracts the group ID and entity ID from each item.
func (c *cappedGroupedCache[T]) load(items []T, keyFn func(T) (groupID, id snowflake.ID)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = make(map[snowflake.ID]map[snowflake.ID]T)
	c.order = nil
	c.head = 0
	c.size = 0
	for _, item := range items {
		groupID, id := keyFn(item)
		if _, ok := c.data[groupID]; !ok {
			c.data[groupID] = make(map[snowflake.ID]T)
		}
		c.data[groupID][id] = item
		c.order = append(c.order, cacheKey{GroupID: groupID, ID: id})
		c.size++
	}
}

// loadMessageCache reads the message cache from disk.
func (b *Bot) loadMessageCache() {
	path := filepath.Join(cacheDir, messageCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read message cache file", "error", err)
		}
		return
	}
	var msgs []discord.Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		b.Log.Error("Failed to decode message cache", "error", err)
		return
	}
	b.msgCache.load(msgs, func(msg discord.Message) (snowflake.ID, snowflake.ID) {
		return msg.ChannelID, msg.ID
	})
	b.Log.Info("Loaded message cache", "count", len(msgs))
}

// saveMessageCache writes the message cache to disk.
func (b *Bot) saveMessageCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}

	msgs := b.msgCache.snapshot()
	data, err := json.Marshal(msgs)
	if err != nil {
		b.Log.Error("Failed to marshal message cache", "error", err)
		return
	}

	path := filepath.Join(cacheDir, messageCacheFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		b.Log.Error("Failed to write message cache file", "error", err)
	}
}

// loadMemberCache reads the member cache from disk.
func (b *Bot) loadMemberCache() {
	path := filepath.Join(cacheDir, memberCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read member cache file", "error", err)
		}
		return
	}
	var members []discord.Member
	if err := json.Unmarshal(data, &members); err != nil {
		b.Log.Error("Failed to decode member cache", "error", err)
		return
	}
	b.memberCache.load(members, func(m discord.Member) (snowflake.ID, snowflake.ID) {
		return m.GuildID, m.User.ID
	})
	b.Log.Info("Loaded member cache", "count", len(members))
}

// saveMemberCache writes the member cache to disk.
func (b *Bot) saveMemberCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}

	members := b.memberCache.snapshot()
	data, err := json.Marshal(members)
	if err != nil {
		b.Log.Error("Failed to marshal member cache", "error", err)
		return
	}

	path := filepath.Join(cacheDir, memberCacheFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		b.Log.Error("Failed to write member cache file", "error", err)
	}
}
