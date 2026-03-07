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
const guildCacheFile = "guildcache.json"
const channelCacheFile = "channelcache.json"
const roleCacheFile = "rolecache.json"
const emojiCacheFile = "emojicache.json"
const stickerCacheFile = "stickercache.json"
const voiceStateCacheFile = "voicestatecache.json"
const presenceCacheFile = "presencecache.json"
const threadMemberCacheFile = "threadmembercache.json"
const stageInstanceCacheFile = "stageinstancecache.json"
const scheduledEventCacheFile = "scheduledeventcache.json"
const soundboardSoundCacheFile = "soundboardsoundcache.json"

type cacheKey struct {
	GroupID snowflake.ID
	ID      snowflake.ID
}

var _ cache.GroupedCache[discord.Message] = (*cappedGroupedCache[discord.Message])(nil)
var _ cache.GroupedCache[discord.Member] = (*cappedGroupedCache[discord.Member])(nil)
var _ cache.Cache[discord.Guild] = (*cappedCache[discord.Guild])(nil)
var _ cache.Cache[discord.GuildChannel] = (*cappedCache[discord.GuildChannel])(nil)

type cappedCache[T any] struct {
	mu      sync.RWMutex
	data    map[snowflake.ID]T
	order   []snowflake.ID
	head    int
	size    int
	maxSize int
}

func newCappedCache[T any](maxSize int) *cappedCache[T] {
	return &cappedCache[T]{
		data:    make(map[snowflake.ID]T),
		maxSize: maxSize,
	}
}

func (c *cappedCache[T]) Get(id snowflake.ID) (T, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entity, ok := c.data[id]
	return entity, ok
}

func (c *cappedCache[T]) Put(id snowflake.ID, entity T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.data[id]; !exists {
		c.order = append(c.order, id)
		c.size++
	}
	c.data[id] = entity
	c.evict()
}

func (c *cappedCache[T]) evict() {
	for c.size > c.maxSize && c.head < len(c.order) {
		id := c.order[c.head]
		c.order[c.head] = 0
		c.head++
		if _, ok := c.data[id]; ok {
			delete(c.data, id)
			c.size--
			break
		}
	}
	if c.head >= len(c.order)/2 && c.head > 0 {
		remaining := copy(c.order, c.order[c.head:])
		for i := remaining; i < len(c.order); i++ {
			c.order[i] = 0
		}
		c.order = c.order[:remaining]
		c.head = 0
	}
}

func (c *cappedCache[T]) Remove(id snowflake.ID) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entity, ok := c.data[id]
	if ok {
		delete(c.data, id)
		c.size--
	}
	return entity, ok
}

func (c *cappedCache[T]) RemoveIf(filterFunc cache.FilterFunc[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, entity := range c.data {
		if filterFunc(entity) {
			delete(c.data, id)
			c.size--
		}
	}
}

func (c *cappedCache[T]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.size
}

func (c *cappedCache[T]) All() iter.Seq[T] {
	return func(yield func(T) bool) {
		c.mu.RLock()
		defer c.mu.RUnlock()
		for _, entity := range c.data {
			if !yield(entity) {
				return
			}
		}
	}
}

func (c *cappedCache[T]) snapshot() []T {
	c.mu.RLock()
	defer c.mu.RUnlock()
	items := make([]T, 0, len(c.data))
	for _, entity := range c.data {
		items = append(items, entity)
	}
	return items
}

func (c *cappedCache[T]) load(items []T, keyFn func(T) snowflake.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = make(map[snowflake.ID]T)
	c.order = nil
	c.head = 0
	c.size = 0
	for _, item := range items {
		id := keyFn(item)
		c.data[id] = item
		c.order = append(c.order, id)
		c.size++
	}
}

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

func (b *Bot) loadGuildCache() {
	path := filepath.Join(cacheDir, guildCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read guild cache file", "error", err)
		}
		return
	}
	var guilds []discord.Guild
	if err := json.Unmarshal(data, &guilds); err != nil {
		b.Log.Error("Failed to decode guild cache", "error", err)
		return
	}
	b.guildCache.load(guilds, func(g discord.Guild) snowflake.ID { return g.ID })
	b.Log.Info("Loaded guild cache", "count", len(guilds))
}

func (b *Bot) saveGuildCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}
	data, err := json.Marshal(b.guildCache.snapshot())
	if err != nil {
		b.Log.Error("Failed to marshal guild cache", "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, guildCacheFile), data, 0o644); err != nil {
		b.Log.Error("Failed to write guild cache file", "error", err)
	}
}

func (b *Bot) loadChannelCache() {
	path := filepath.Join(cacheDir, channelCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read channel cache file", "error", err)
		}
		return
	}
	var wrappers []discord.UnmarshalChannel
	if err := json.Unmarshal(data, &wrappers); err != nil {
		b.Log.Error("Failed to decode channel cache", "error", err)
		return
	}
	channels := make([]discord.GuildChannel, 0, len(wrappers))
	for _, w := range wrappers {
		if gc, ok := w.Channel.(discord.GuildChannel); ok {
			channels = append(channels, gc)
		}
	}
	b.channelCache.load(channels, func(ch discord.GuildChannel) snowflake.ID { return ch.ID() })
	b.Log.Info("Loaded channel cache", "count", len(channels))
}

func (b *Bot) saveChannelCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}
	data, err := json.Marshal(b.channelCache.snapshot())
	if err != nil {
		b.Log.Error("Failed to marshal channel cache", "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, channelCacheFile), data, 0o644); err != nil {
		b.Log.Error("Failed to write channel cache file", "error", err)
	}
}

func (b *Bot) loadRoleCache() {
	path := filepath.Join(cacheDir, roleCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read role cache file", "error", err)
		}
		return
	}
	var roles []discord.Role
	if err := json.Unmarshal(data, &roles); err != nil {
		b.Log.Error("Failed to decode role cache", "error", err)
		return
	}
	b.roleCache.load(roles, func(r discord.Role) (snowflake.ID, snowflake.ID) {
		return r.GuildID, r.ID
	})
	b.Log.Info("Loaded role cache", "count", len(roles))
}

func (b *Bot) saveRoleCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}
	data, err := json.Marshal(b.roleCache.snapshot())
	if err != nil {
		b.Log.Error("Failed to marshal role cache", "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, roleCacheFile), data, 0o644); err != nil {
		b.Log.Error("Failed to write role cache file", "error", err)
	}
}

func (b *Bot) loadEmojiCache() {
	path := filepath.Join(cacheDir, emojiCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read emoji cache file", "error", err)
		}
		return
	}
	var emojis []discord.Emoji
	if err := json.Unmarshal(data, &emojis); err != nil {
		b.Log.Error("Failed to decode emoji cache", "error", err)
		return
	}
	b.emojiCache.load(emojis, func(e discord.Emoji) (snowflake.ID, snowflake.ID) {
		return e.GuildID, e.ID
	})
	b.Log.Info("Loaded emoji cache", "count", len(emojis))
}

func (b *Bot) saveEmojiCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}
	data, err := json.Marshal(b.emojiCache.snapshot())
	if err != nil {
		b.Log.Error("Failed to marshal emoji cache", "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, emojiCacheFile), data, 0o644); err != nil {
		b.Log.Error("Failed to write emoji cache file", "error", err)
	}
}

func (b *Bot) loadStickerCache() {
	path := filepath.Join(cacheDir, stickerCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read sticker cache file", "error", err)
		}
		return
	}
	var stickers []discord.Sticker
	if err := json.Unmarshal(data, &stickers); err != nil {
		b.Log.Error("Failed to decode sticker cache", "error", err)
		return
	}
	b.stickerCache.load(stickers, func(s discord.Sticker) (snowflake.ID, snowflake.ID) {
		if s.GuildID == nil {
			return 0, s.ID
		}
		return *s.GuildID, s.ID
	})
	b.Log.Info("Loaded sticker cache", "count", len(stickers))
}

func (b *Bot) saveStickerCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}
	data, err := json.Marshal(b.stickerCache.snapshot())
	if err != nil {
		b.Log.Error("Failed to marshal sticker cache", "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, stickerCacheFile), data, 0o644); err != nil {
		b.Log.Error("Failed to write sticker cache file", "error", err)
	}
}

func (b *Bot) loadVoiceStateCache() {
	path := filepath.Join(cacheDir, voiceStateCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read voice state cache file", "error", err)
		}
		return
	}
	var states []discord.VoiceState
	if err := json.Unmarshal(data, &states); err != nil {
		b.Log.Error("Failed to decode voice state cache", "error", err)
		return
	}
	b.voiceStateCache.load(states, func(vs discord.VoiceState) (snowflake.ID, snowflake.ID) {
		return vs.GuildID, vs.UserID
	})
	b.Log.Info("Loaded voice state cache", "count", len(states))
}

func (b *Bot) saveVoiceStateCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}
	data, err := json.Marshal(b.voiceStateCache.snapshot())
	if err != nil {
		b.Log.Error("Failed to marshal voice state cache", "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, voiceStateCacheFile), data, 0o644); err != nil {
		b.Log.Error("Failed to write voice state cache file", "error", err)
	}
}

func (b *Bot) loadPresenceCache() {
	path := filepath.Join(cacheDir, presenceCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read presence cache file", "error", err)
		}
		return
	}
	var presences []discord.Presence
	if err := json.Unmarshal(data, &presences); err != nil {
		b.Log.Error("Failed to decode presence cache", "error", err)
		return
	}
	b.presenceCache.load(presences, func(p discord.Presence) (snowflake.ID, snowflake.ID) {
		return p.GuildID, p.PresenceUser.ID
	})
	b.Log.Info("Loaded presence cache", "count", len(presences))
}

func (b *Bot) savePresenceCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}
	data, err := json.Marshal(b.presenceCache.snapshot())
	if err != nil {
		b.Log.Error("Failed to marshal presence cache", "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, presenceCacheFile), data, 0o644); err != nil {
		b.Log.Error("Failed to write presence cache file", "error", err)
	}
}

func (b *Bot) loadThreadMemberCache() {
	path := filepath.Join(cacheDir, threadMemberCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read thread member cache file", "error", err)
		}
		return
	}
	var members []discord.ThreadMember
	if err := json.Unmarshal(data, &members); err != nil {
		b.Log.Error("Failed to decode thread member cache", "error", err)
		return
	}
	b.threadMemberCache.load(members, func(tm discord.ThreadMember) (snowflake.ID, snowflake.ID) {
		return tm.ThreadID, tm.UserID
	})
	b.Log.Info("Loaded thread member cache", "count", len(members))
}

func (b *Bot) saveThreadMemberCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}
	data, err := json.Marshal(b.threadMemberCache.snapshot())
	if err != nil {
		b.Log.Error("Failed to marshal thread member cache", "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, threadMemberCacheFile), data, 0o644); err != nil {
		b.Log.Error("Failed to write thread member cache file", "error", err)
	}
}

func (b *Bot) loadStageInstanceCache() {
	path := filepath.Join(cacheDir, stageInstanceCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read stage instance cache file", "error", err)
		}
		return
	}
	var instances []discord.StageInstance
	if err := json.Unmarshal(data, &instances); err != nil {
		b.Log.Error("Failed to decode stage instance cache", "error", err)
		return
	}
	b.stageInstanceCache.load(instances, func(s discord.StageInstance) (snowflake.ID, snowflake.ID) {
		return s.GuildID, s.ID
	})
	b.Log.Info("Loaded stage instance cache", "count", len(instances))
}

func (b *Bot) saveStageInstanceCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}
	data, err := json.Marshal(b.stageInstanceCache.snapshot())
	if err != nil {
		b.Log.Error("Failed to marshal stage instance cache", "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, stageInstanceCacheFile), data, 0o644); err != nil {
		b.Log.Error("Failed to write stage instance cache file", "error", err)
	}
}

func (b *Bot) loadScheduledEventCache() {
	path := filepath.Join(cacheDir, scheduledEventCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read scheduled event cache file", "error", err)
		}
		return
	}
	var events []discord.GuildScheduledEvent
	if err := json.Unmarshal(data, &events); err != nil {
		b.Log.Error("Failed to decode scheduled event cache", "error", err)
		return
	}
	b.scheduledEventCache.load(events, func(e discord.GuildScheduledEvent) (snowflake.ID, snowflake.ID) {
		return e.GuildID, e.ID
	})
	b.Log.Info("Loaded scheduled event cache", "count", len(events))
}

func (b *Bot) saveScheduledEventCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}
	data, err := json.Marshal(b.scheduledEventCache.snapshot())
	if err != nil {
		b.Log.Error("Failed to marshal scheduled event cache", "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, scheduledEventCacheFile), data, 0o644); err != nil {
		b.Log.Error("Failed to write scheduled event cache file", "error", err)
	}
}

func (b *Bot) loadSoundboardSoundCache() {
	path := filepath.Join(cacheDir, soundboardSoundCacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			b.Log.Error("Failed to read soundboard sound cache file", "error", err)
		}
		return
	}
	var sounds []discord.SoundboardSound
	if err := json.Unmarshal(data, &sounds); err != nil {
		b.Log.Error("Failed to decode soundboard sound cache", "error", err)
		return
	}
	b.soundboardSoundCache.load(sounds, func(s discord.SoundboardSound) (snowflake.ID, snowflake.ID) {
		if s.GuildID == nil {
			return 0, s.SoundID
		}
		return *s.GuildID, s.SoundID
	})
	b.Log.Info("Loaded soundboard sound cache", "count", len(sounds))
}

func (b *Bot) saveSoundboardSoundCache() {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		b.Log.Error("Failed to create cache directory", "error", err)
		return
	}
	data, err := json.Marshal(b.soundboardSoundCache.snapshot())
	if err != nil {
		b.Log.Error("Failed to marshal soundboard sound cache", "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, soundboardSoundCacheFile), data, 0o644); err != nil {
		b.Log.Error("Failed to write soundboard sound cache file", "error", err)
	}
}
