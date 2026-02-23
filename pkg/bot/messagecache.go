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

const messageCacheDir = "/sprobot-cache"
const messageCacheFile = "messagecache.json"

type cacheKey struct {
	GroupID snowflake.ID
	ID      snowflake.ID
}

var _ cache.GroupedCache[discord.Message] = (*cappedGroupedCache)(nil)

type cappedGroupedCache struct {
	mu      sync.RWMutex
	data    map[snowflake.ID]map[snowflake.ID]discord.Message
	order   []cacheKey
	head    int
	size    int
	maxSize int
}

func newCappedGroupedCache(maxSize int) *cappedGroupedCache {
	return &cappedGroupedCache{
		data:    make(map[snowflake.ID]map[snowflake.ID]discord.Message),
		maxSize: maxSize,
	}
}

func (c *cappedGroupedCache) Get(groupID snowflake.ID, id snowflake.ID) (discord.Message, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if group, ok := c.data[groupID]; ok {
		if msg, ok := group[id]; ok {
			return msg, true
		}
	}
	var zero discord.Message
	return zero, false
}

func (c *cappedGroupedCache) Put(groupID snowflake.ID, id snowflake.ID, entity discord.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if group, ok := c.data[groupID]; ok {
		if _, exists := group[id]; !exists {
			c.order = append(c.order, cacheKey{GroupID: groupID, ID: id})
			c.size++
		}
		group[id] = entity
	} else {
		group = map[snowflake.ID]discord.Message{id: entity}
		c.data[groupID] = group
		c.order = append(c.order, cacheKey{GroupID: groupID, ID: id})
		c.size++
	}

	c.evict()
}

func (c *cappedGroupedCache) evict() {
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

func (c *cappedGroupedCache) len() int {
	return c.size
}

func (c *cappedGroupedCache) Remove(groupID snowflake.ID, id snowflake.ID) (discord.Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if group, ok := c.data[groupID]; ok {
		if msg, ok := group[id]; ok {
			delete(group, id)
			c.size--
			if len(group) == 0 {
				delete(c.data, groupID)
			}
			return msg, true
		}
	}
	var zero discord.Message
	return zero, false
}

func (c *cappedGroupedCache) GroupRemove(groupID snowflake.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if group, ok := c.data[groupID]; ok {
		c.size -= len(group)
		delete(c.data, groupID)
	}
}

func (c *cappedGroupedCache) RemoveIf(filterFunc cache.GroupedFilterFunc[discord.Message]) {
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

func (c *cappedGroupedCache) GroupRemoveIf(groupID snowflake.ID, filterFunc cache.GroupedFilterFunc[discord.Message]) {
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

func (c *cappedGroupedCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.len()
}

func (c *cappedGroupedCache) GroupLen(groupID snowflake.ID) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if group, ok := c.data[groupID]; ok {
		return len(group)
	}
	return 0
}

func (c *cappedGroupedCache) All() iter.Seq2[snowflake.ID, discord.Message] {
	return func(yield func(snowflake.ID, discord.Message) bool) {
		c.mu.RLock()
		defer c.mu.RUnlock()
		for groupID, group := range c.data {
			for _, msg := range group {
				if !yield(groupID, msg) {
					return
				}
			}
		}
	}
}

func (c *cappedGroupedCache) GroupAll(groupID snowflake.ID) iter.Seq[discord.Message] {
	return func(yield func(discord.Message) bool) {
		c.mu.RLock()
		defer c.mu.RUnlock()
		if group, ok := c.data[groupID]; ok {
			for _, msg := range group {
				if !yield(msg) {
					return
				}
			}
		}
	}
}

// snapshot returns all cached messages for persistence.
func (c *cappedGroupedCache) snapshot() []discord.Message {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var msgs []discord.Message
	for _, group := range c.data {
		for _, msg := range group {
			msgs = append(msgs, msg)
		}
	}
	return msgs
}

// load populates the cache from a slice of messages.
func (c *cappedGroupedCache) load(msgs []discord.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = make(map[snowflake.ID]map[snowflake.ID]discord.Message)
	c.order = nil
	c.head = 0
	c.size = 0
	for _, msg := range msgs {
		groupID := msg.ChannelID
		id := msg.ID
		if _, ok := c.data[groupID]; !ok {
			c.data[groupID] = make(map[snowflake.ID]discord.Message)
		}
		c.data[groupID][id] = msg
		c.order = append(c.order, cacheKey{GroupID: groupID, ID: id})
		c.size++
	}
}

// loadMessageCache reads the message cache from disk.
func (b *Bot) loadMessageCache() {
	path := filepath.Join(messageCacheDir, messageCacheFile)
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
	b.msgCache.load(msgs)
	b.Log.Info("Loaded message cache", "count", len(msgs))
}

// saveMessageCache writes the message cache to disk.
func (b *Bot) saveMessageCache() {
	defer func() {
		if r := recover(); r != nil {
			b.Log.Error("Panic in message cache save", "error", r)
		}
	}()

	if err := os.MkdirAll(messageCacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}

	msgs := b.msgCache.snapshot()
	data, err := json.Marshal(msgs)
	if err != nil {
		b.Log.Error("Failed to marshal message cache", "error", err)
		return
	}

	path := filepath.Join(messageCacheDir, messageCacheFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		b.Log.Error("Failed to write message cache file", "error", err)
	}
}
