package bot

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

// messageRetention bounds how long cached message content is kept, per the
// data-retention practices disclosed in PRIVACY.md and Discord's
// message-content intent requirements.
const messageRetention = 30 * 24 * time.Hour

// defaultCacheDir is where the caches are persisted (Bot.cacheDir).
const defaultCacheDir = "/sprobot-cache"

// enabledCacheFlags lists the disgo caches sprobot keeps; each is persisted to
// the cache dir. It is explicit rather than cache.FlagsAll so that a cache
// added by a disgo upgrade stays off until someone decides to keep it.
// TestCacheFlagsClassified fails until every disgo flag is either here or in
// disabledCacheFlags.
const enabledCacheFlags = cache.FlagGuilds |
	cache.FlagChannels |
	cache.FlagMessages |
	cache.FlagMembers |
	cache.FlagRoles |
	cache.FlagEmojis |
	cache.FlagStickers |
	cache.FlagVoiceStates |
	cache.FlagStageInstances |
	cache.FlagGuildScheduledEvents |
	cache.FlagGuildSoundboardSounds

// disabledCacheFlags lists the disgo caches deliberately left off, and why.
// Storing user data no feature reads costs compliance and buys nothing; see
// docs/privileged-intents/sprobot.md.
var disabledCacheFlags = map[cache.Flags]string{
	cache.FlagPresences:     "no presence intent, and no feature reads presences",
	cache.FlagThreadMembers: "no feature reads thread membership",
}

// retiredCacheFiles are files written by caches that have since been disabled.
// They are deleted on startup so the data does not linger on disk.
var retiredCacheFiles = []string{"presencecache.json", "threadmembercache.json"}

// removeRetiredCacheFiles also deletes the temp files of saves that were
// killed partway: they hold cache data, and only the next successful save of
// that cache, which waits for the gateway, would replace them.
func (b *Bot) removeRetiredCacheFiles() {
	names := slices.Clone(retiredCacheFiles)
	entries, err := os.ReadDir(b.cacheDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		b.Log.Error("Failed to list the cache dir", "error", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			names = append(names, e.Name())
		}
	}
	for _, name := range names {
		err := os.Remove(filepath.Join(b.cacheDir, name))
		switch {
		case err == nil:
			b.Log.Info("Removed retired cache file", "file", name)
		case !os.IsNotExist(err):
			b.Log.Error("Failed to remove retired cache file", "file", name, "error", err)
		}
	}
}

const messageCacheFile = "messagecache.json"
const memberCacheFile = "membercache.json"
const guildCacheFile = "guildcache.json"
const channelCacheFile = "channelcache.json"
const roleCacheFile = "rolecache.json"
const emojiCacheFile = "emojicache.json"
const stickerCacheFile = "stickercache.json"
const voiceStateCacheFile = "voicestatecache.json"
const stageInstanceCacheFile = "stageinstancecache.json"
const scheduledEventCacheFile = "scheduledeventcache.json"
const soundboardSoundCacheFile = "soundboardsoundcache.json"

// initCaches creates the capped caches behind disgo's.
func (b *Bot) initCaches() {
	b.msgCache = newMessageCache(1000000)
	b.memberCache = newCappedGroupedCache[discord.Member](100000)
	b.guildCache = newCappedCache[discord.Guild](100)
	b.channelCache = newCappedCache[discord.GuildChannel](10000)
	b.roleCache = newCappedGroupedCache[discord.Role](5000)
	b.emojiCache = newCappedGroupedCache[discord.Emoji](5000)
	b.stickerCache = newCappedGroupedCache[discord.Sticker](1000)
	b.voiceStateCache = newCappedGroupedCache[discord.VoiceState](10000)
	b.stageInstanceCache = newCappedGroupedCache[discord.StageInstance](1000)
	b.scheduledEventCache = newCappedGroupedCache[discord.GuildScheduledEvent](1000)
	b.soundboardSoundCache = newCappedGroupedCache[discord.SoundboardSound](1000)
}

// cacheConfig points disgo at the caches made by initCaches.
func (b *Bot) cacheConfig() []cache.ConfigOpt {
	guilds := readyGuildCache{cache.NewGuildCache(b.guildCache, cache.NewSet[snowflake.ID](), cache.NewSet[snowflake.ID]())}
	return []cache.ConfigOpt{
		cache.WithCaches(enabledCacheFlags),
		cache.WithGuildCache(guilds),
		cache.WithChannelCache(cache.NewChannelCache(b.channelCache)),
		cache.WithMessageCache(outageSafeMessageCache{
			disgoMessageCache: cache.NewMessageCache(b.msgCache),
			unavailable:       guilds.IsGuildUnavailable,
		}),
		cache.WithMemberCache(guildMemberCache{
			disgoMemberCache: cache.NewMemberCache(b.memberCache),
			unavailable:      guilds.IsGuildUnavailable,
		}),
		cache.WithRoleCache(cache.NewRoleCache(b.roleCache)),
		cache.WithEmojiCache(cache.NewEmojiCache(b.emojiCache)),
		cache.WithStickerCache(cache.NewStickerCache(b.stickerCache)),
		cache.WithVoiceStateCache(cache.NewVoiceStateCache(b.voiceStateCache)),
		cache.WithStageInstanceCache(cache.NewStageInstanceCache(b.stageInstanceCache)),
		cache.WithGuildScheduledEventCache(cache.NewGuildScheduledEventCache(b.scheduledEventCache)),
		cache.WithGuildSoundboardSoundCache(cache.NewGuildSoundboardSoundCache(b.soundboardSoundCache)),
	}
}

// Aliases to embed disgo's caches under: a field named GuildCache,
// MessageCache or MemberCache would hide the interface method of that name.
type (
	disgoGuildCache   = cache.GuildCache
	disgoMessageCache = cache.MessageCache
	disgoMemberCache  = cache.MemberCache
)

// readyGuildCache clears a guild's unavailable flag when it becomes ready.
// disgo clears it only on a GUILD_CREATE for a guild that is not unready, so
// a guild that was unavailable at a READY stays flagged once it is back, and
// outageSafeMessageCache and guildMemberCache would keep its messages and
// members after the bot leaves it.
type readyGuildCache struct {
	disgoGuildCache
}

func (c readyGuildCache) SetGuildUnready(guildID snowflake.ID, unready bool) {
	c.disgoGuildCache.SetGuildUnready(guildID, unready)
	if !unready {
		c.SetGuildUnavailable(guildID, false)
	}
}

// onGuildLeave removes the messages and members of a guild the bot left while
// it was flagged unavailable: outageSafeMessageCache and guildMemberCache kept
// them when disgo tried.
func (b *Bot) onGuildLeave(e *events.GuildLeave) {
	caches := e.Client().Caches
	if !caches.IsGuildUnavailable(e.GuildID) {
		return
	}
	caches.SetGuildUnavailable(e.GuildID, false)
	caches.RemoveMessagesByGuildID(e.GuildID)
	caches.RemoveMembersByGuildID(e.GuildID)
}

// outageSafeMessageCache keeps only messages from served guilds: content from
// a guild outside botutil.AllowedGuildIDs, or from a DM, is not ours to keep.
// It also keeps a guild's messages while it is unavailable. On GUILD_DELETE
// disgo removes them even when the guild is only down in an outage, nothing
// refills them when it comes back, and the next save would take up to 30 days
// of edit and delete context off disk with them.
type outageSafeMessageCache struct {
	disgoMessageCache
	unavailable func(guildID snowflake.ID) bool
}

func (c outageSafeMessageCache) AddMessage(message discord.Message) {
	if message.GuildID != nil && botutil.GuildAllowed(*message.GuildID) {
		c.disgoMessageCache.AddMessage(message)
	}
}

func (c outageSafeMessageCache) RemoveMessagesByGuildID(guildID snowflake.ID) {
	if !c.unavailable(guildID) {
		c.disgoMessageCache.RemoveMessagesByGuildID(guildID)
	}
}

// guildMemberCache files members only under served guilds
// (botutil.AllowedGuildIDs). That also drops disgo's THREAD_MEMBERS_UPDATE
// members, which it adds under the thread ID, where nothing ever removes
// them: they would push real members out at the cap, and save thread
// membership we don't keep. Like outageSafeMessageCache, it also keeps a
// guild's members through an outage: GUILD_CREATE brings back only a few.
type guildMemberCache struct {
	disgoMemberCache
	unavailable func(guildID snowflake.ID) bool
}

func (c guildMemberCache) AddMember(member discord.Member) {
	if botutil.GuildAllowed(member.GuildID) {
		c.disgoMemberCache.AddMember(member)
	}
}

func (c guildMemberCache) RemoveMembersByGuildID(guildID snowflake.ID) {
	if !c.unavailable(guildID) {
		c.disgoMemberCache.RemoveMembersByGuildID(guildID)
	}
}

// cacheKey is a place in a cappedGroupedCache's insertion order. seq tells a
// live place from a stale one: an entry that was removed, or removed and put
// back, no longer has the seq of its old place.
type cacheKey struct {
	GroupID snowflake.ID
	ID      snowflake.ID
	seq     uint64
}

type cacheEntry[T any] struct {
	entity T
	seq    uint64
}

// orderSlack is how far order may grow past twice the live size before it is
// compacted, so small caches don't compact on every removal.
const orderSlack = 1024

var _ cache.GroupedCache[discord.Message] = (*messageCache)(nil)
var _ cache.GroupedCache[discord.Member] = (*cappedGroupedCache[discord.Member])(nil)
var _ cache.Cache[discord.Guild] = (*cappedCache[discord.Guild])(nil)
var _ cache.Cache[discord.GuildChannel] = (*cappedCache[discord.GuildChannel])(nil)

// cappedCache is a cappedGroupedCache with a single group (0), for disgo's
// ungrouped caches.
type cappedCache[T any] struct {
	*cappedGroupedCache[T]
}

func newCappedCache[T any](maxSize int) *cappedCache[T] {
	return &cappedCache[T]{newCappedGroupedCache[T](maxSize)}
}

func (c *cappedCache[T]) Get(id snowflake.ID) (T, bool) {
	return c.cappedGroupedCache.Get(0, id)
}

func (c *cappedCache[T]) Put(id snowflake.ID, entity T) {
	c.cappedGroupedCache.Put(0, id, entity)
}

func (c *cappedCache[T]) Remove(id snowflake.ID) (T, bool) {
	return c.cappedGroupedCache.Remove(0, id)
}

func (c *cappedCache[T]) RemoveIf(filterFunc cache.FilterFunc[T]) {
	c.cappedGroupedCache.RemoveIf(func(_ snowflake.ID, entity T) bool { return filterFunc(entity) })
}

func (c *cappedCache[T]) All() iter.Seq[T] {
	return c.GroupAll(0)
}

// cachedMessage is what the message cache keeps of a message. A
// discord.Message is several times the size, and holds the author twice. It
// keeps the fields every reader of cached messages uses:
//   - the event log, on a deleted or edited message: content and author;
//   - disgo: the IDs it files and clears messages by;
//   - link cleaning (M6 P3, planned): the reference, edit time and
//     components, which mark the bot's link-cleaning replies;
//   - linkeval (M6 P4, planned), which reads the file: the type, webhook ID
//     and the author's bot and system flags.
//
// The JSON names match discord.Message's, so older files load as they are,
// their other fields skipped, and older versions can load these.
type cachedMessage struct {
	ID               snowflake.ID              `json:"id"`
	ChannelID        snowflake.ID              `json:"channel_id"`
	GuildID          snowflake.ID              `json:"guild_id,omitempty"` // 0 outside a guild
	Type             discord.MessageType       `json:"type,omitempty"`
	Content          string                    `json:"content"`
	Author           cachedAuthor              `json:"author"`
	WebhookID        *snowflake.ID             `json:"webhook_id,omitempty"`
	EditedTimestamp  *time.Time                `json:"edited_timestamp,omitempty"`
	MessageReference *discord.MessageReference `json:"message_reference,omitempty"`
	// Components stays JSON: only bot messages have any, and disgo's
	// component types decode only through its unmarshaler.
	Components json.RawMessage `json:"components,omitempty"`
}

// cachedAuthor is the part of a message's author the readers use: the ID,
// name, bot and system flags, and what User.EffectiveAvatarURL needs.
type cachedAuthor struct {
	ID            snowflake.ID `json:"id"`
	Username      string       `json:"username"`
	Discriminator string       `json:"discriminator"`
	Avatar        string       `json:"avatar,omitempty"` // "" for the default avatar
	Bot           bool         `json:"bot"`
	System        bool         `json:"system,omitempty"`
}

func newCachedMessage(m discord.Message) cachedMessage {
	c := cachedMessage{
		ID:        m.ID,
		ChannelID: m.ChannelID,
		Type:      m.Type,
		Content:   m.Content,
		Author: cachedAuthor{
			ID:            m.Author.ID,
			Username:      m.Author.Username,
			Discriminator: m.Author.Discriminator,
			Bot:           m.Author.Bot,
			System:        m.Author.System,
		},
		WebhookID:        m.WebhookID,
		EditedTimestamp:  m.EditedTimestamp,
		MessageReference: m.MessageReference,
	}
	if m.GuildID != nil {
		c.GuildID = *m.GuildID
	}
	if m.Author.Avatar != nil {
		c.Author.Avatar = *m.Author.Avatar
	}
	if len(m.Components) > 0 {
		c.Components, _ = json.Marshal(m.Components) // they were decoded from JSON
	}
	return c
}

// message rebuilds a discord.Message with the fields c keeps.
func (c cachedMessage) message() discord.Message {
	m := discord.Message{
		ID:        c.ID,
		ChannelID: c.ChannelID,
		Type:      c.Type,
		Content:   c.Content,
		Author: discord.User{
			ID:            c.Author.ID,
			Username:      c.Author.Username,
			Discriminator: c.Author.Discriminator,
			Bot:           c.Author.Bot,
			System:        c.Author.System,
		},
		WebhookID:        c.WebhookID,
		EditedTimestamp:  c.EditedTimestamp,
		MessageReference: c.MessageReference,
	}
	if c.GuildID != 0 {
		m.GuildID = &c.GuildID
	}
	if c.Author.Avatar != "" {
		m.Author.Avatar = &c.Author.Avatar
	}
	if len(c.Components) > 0 {
		var components []discord.UnmarshalComponent
		if json.Unmarshal(c.Components, &components) == nil {
			for _, u := range components {
				if lc, ok := u.Component.(discord.LayoutComponent); ok {
					m.Components = append(m.Components, lc)
				}
			}
		}
	}
	return m
}

// messageCache is disgo's message cache. It stores cachedMessage and rebuilds
// a discord.Message on the way out, so cached messages (disgo's OldMessage,
// and the message in a delete event) carry only cachedMessage's fields: a new
// reader of them must check those are enough.
type messageCache struct {
	*cappedGroupedCache[cachedMessage]
}

func newMessageCache(maxSize int) *messageCache {
	c := newCappedGroupedCache[cachedMessage](maxSize)
	c.loadByID = true
	return &messageCache{c}
}

func (c *messageCache) Get(channelID snowflake.ID, id snowflake.ID) (discord.Message, bool) {
	m, ok := c.cappedGroupedCache.Get(channelID, id)
	if !ok {
		return discord.Message{}, false
	}
	return m.message(), true
}

func (c *messageCache) Put(channelID snowflake.ID, id snowflake.ID, msg discord.Message) {
	c.cappedGroupedCache.Put(channelID, id, newCachedMessage(msg))
}

func (c *messageCache) Remove(channelID snowflake.ID, id snowflake.ID) (discord.Message, bool) {
	m, ok := c.cappedGroupedCache.Remove(channelID, id)
	if !ok {
		return discord.Message{}, false
	}
	return m.message(), true
}

func (c *messageCache) RemoveIf(filterFunc cache.GroupedFilterFunc[discord.Message]) {
	c.cappedGroupedCache.RemoveIf(func(channelID snowflake.ID, m cachedMessage) bool {
		return filterFunc(channelID, m.message())
	})
}

func (c *messageCache) GroupRemoveIf(channelID snowflake.ID, filterFunc cache.GroupedFilterFunc[discord.Message]) {
	c.cappedGroupedCache.GroupRemoveIf(channelID, func(channelID snowflake.ID, m cachedMessage) bool {
		return filterFunc(channelID, m.message())
	})
}

func (c *messageCache) All() iter.Seq2[snowflake.ID, discord.Message] {
	return func(yield func(snowflake.ID, discord.Message) bool) {
		for channelID, m := range c.cappedGroupedCache.All() {
			if !yield(channelID, m.message()) {
				return
			}
		}
	}
}

func (c *messageCache) GroupAll(channelID snowflake.ID) iter.Seq[discord.Message] {
	return func(yield func(discord.Message) bool) {
		for m := range c.cappedGroupedCache.GroupAll(channelID) {
			if !yield(m.message()) {
				return
			}
		}
	}
}

// cappedGroupedCache is a disgo GroupedCache that holds at most maxSize
// entries, evicting the oldest (first put) beyond that.
type cappedGroupedCache[T any] struct {
	mu   sync.RWMutex
	data map[snowflake.ID]map[snowflake.ID]cacheEntry[T]
	// order holds the entries' keys in insertion order from head on, mixed
	// with stale keys left by removals; compact drops those.
	order   []cacheKey
	head    int
	seq     uint64 // the last seq handed out
	size    int
	maxSize int
	// loadByID makes load order the entries by ID instead of file order. It's
	// set for messages, whose IDs are their creation times, so a file saved
	// in map order (before snapshot kept insertion order) evicts oldest first.
	loadByID bool
}

func newCappedGroupedCache[T any](maxSize int) *cappedGroupedCache[T] {
	return &cappedGroupedCache[T]{
		data:    make(map[snowflake.ID]map[snowflake.ID]cacheEntry[T]),
		maxSize: maxSize,
	}
}

func (c *cappedGroupedCache[T]) Get(groupID snowflake.ID, id snowflake.ID) (T, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.data[groupID][id]
	return e.entity, ok
}

func (c *cappedGroupedCache[T]) Put(groupID snowflake.ID, id snowflake.ID, entity T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.put(groupID, id, entity)
	c.evict()
}

// put adds or replaces an entry without evicting. A replaced entry keeps its
// place in the order.
func (c *cappedGroupedCache[T]) put(groupID snowflake.ID, id snowflake.ID, entity T) {
	group, ok := c.data[groupID]
	if !ok {
		group = make(map[snowflake.ID]cacheEntry[T])
		c.data[groupID] = group
	}
	if e, ok := group[id]; ok {
		group[id] = cacheEntry[T]{entity: entity, seq: e.seq}
		return
	}
	c.seq++
	group[id] = cacheEntry[T]{entity: entity, seq: c.seq}
	c.order = append(c.order, cacheKey{GroupID: groupID, ID: id, seq: c.seq})
	c.size++
}

// live reports whether key is the current place of an entry.
func (c *cappedGroupedCache[T]) live(key cacheKey) bool {
	e, ok := c.data[key.GroupID][key.ID]
	return ok && e.seq == key.seq
}

// evict removes the oldest entries until the cache is within maxSize.
func (c *cappedGroupedCache[T]) evict() {
	for c.size > c.maxSize && c.head < len(c.order) {
		key := c.order[c.head]
		c.head++
		if c.live(key) {
			c.remove(key.GroupID, key.ID)
		}
	}
	c.compact()
}

// compact rebuilds order from its live keys once evicted and stale keys
// outnumber them, keeping order within about twice the live size.
func (c *cappedGroupedCache[T]) compact() {
	if len(c.order) <= 2*c.size+orderSlack {
		return
	}
	order := make([]cacheKey, 0, c.size)
	for _, key := range c.order[c.head:] {
		if c.live(key) {
			order = append(order, key)
		}
	}
	c.order = order
	c.head = 0
}

// remove deletes an entry, leaving its key in order stale.
func (c *cappedGroupedCache[T]) remove(groupID snowflake.ID, id snowflake.ID) (T, bool) {
	group := c.data[groupID]
	e, ok := group[id]
	if !ok {
		return e.entity, false
	}
	delete(group, id)
	c.size--
	if len(group) == 0 {
		delete(c.data, groupID)
	}
	return e.entity, true
}

func (c *cappedGroupedCache[T]) Remove(groupID snowflake.ID, id snowflake.ID) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entity, ok := c.remove(groupID, id)
	c.compact()
	return entity, ok
}

func (c *cappedGroupedCache[T]) GroupRemove(groupID snowflake.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.size -= len(c.data[groupID])
	delete(c.data, groupID)
	c.compact()
}

func (c *cappedGroupedCache[T]) RemoveIf(filterFunc cache.GroupedFilterFunc[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for groupID := range c.data {
		c.groupRemoveIf(groupID, filterFunc)
	}
	c.compact()
}

func (c *cappedGroupedCache[T]) GroupRemoveIf(groupID snowflake.ID, filterFunc cache.GroupedFilterFunc[T]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.groupRemoveIf(groupID, filterFunc)
	c.compact()
}

func (c *cappedGroupedCache[T]) groupRemoveIf(groupID snowflake.ID, filterFunc cache.GroupedFilterFunc[T]) {
	group := c.data[groupID]
	for id, e := range group {
		if filterFunc(groupID, e.entity) {
			delete(group, id)
			c.size--
		}
	}
	if len(group) == 0 {
		delete(c.data, groupID)
	}
}

func (c *cappedGroupedCache[T]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.size
}

func (c *cappedGroupedCache[T]) GroupLen(groupID snowflake.ID) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.data[groupID])
}

func (c *cappedGroupedCache[T]) All() iter.Seq2[snowflake.ID, T] {
	return func(yield func(snowflake.ID, T) bool) {
		c.mu.RLock()
		defer c.mu.RUnlock()
		for groupID, group := range c.data {
			for _, e := range group {
				if !yield(groupID, e.entity) {
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
		for _, e := range c.data[groupID] {
			if !yield(e.entity) {
				return
			}
		}
	}
}

// snapshot returns the entries oldest first, for saving. It holds the read
// lock throughout, blocking Put (and with it disgo's gateway handling), so it
// only copies.
func (c *cappedGroupedCache[T]) snapshot() []T {
	c.mu.RLock()
	defer c.mu.RUnlock()
	items := make([]T, 0, c.size)
	for _, key := range c.order[c.head:] {
		if e, ok := c.data[key.GroupID][key.ID]; ok && e.seq == key.seq {
			items = append(items, e.entity)
		}
	}
	return items
}

// load replaces the cache's contents with the entries fill passes to add,
// oldest first, then evicts down to maxSize. It returns how many entries it
// kept.
func (c *cappedGroupedCache[T]) load(fill func(add func(groupID, id snowflake.ID, entity T)) error) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = make(map[snowflake.ID]map[snowflake.ID]cacheEntry[T])
	c.order = nil
	c.head = 0
	c.size = 0
	err := fill(c.put)
	if c.loadByID {
		slices.SortFunc(c.order, func(a, b cacheKey) int { return cmp.Compare(a.ID, b.ID) })
	}
	c.evict()
	return c.size, err
}

// messageExpired reports whether a message is past messageRetention at now,
// judged by the creation time in its snowflake.
func messageExpired(id snowflake.ID, now time.Time) bool {
	return id.Time().Before(now.Add(-messageRetention))
}

// pruneMessageCache removes messages past messageRetention from the cache.
// saveCaches runs it before every save, so the file never holds an expired
// message for longer than the save interval.
func (b *Bot) pruneMessageCache() {
	now := time.Now()
	removed := 0
	b.msgCache.cappedGroupedCache.RemoveIf(func(_ snowflake.ID, msg cachedMessage) bool {
		expired := messageExpired(msg.ID, now)
		if expired {
			removed++
		}
		return expired
	})
	if removed > 0 {
		b.Log.Info("Pruned expired messages from cache", "removed", removed, "remaining", b.msgCache.Len())
	}
}

// loadCaches fills the caches from the cache dir, skipping expired messages.
// New calls it before disgo starts, so nothing else uses the caches yet.
func (b *Bot) loadCaches() {
	now := time.Now()
	expired := 0
	loadCache(b, messageCacheFile, b.msgCache.cappedGroupedCache, func(m cachedMessage) (cachedMessage, snowflake.ID, snowflake.ID, bool) {
		if messageExpired(m.ID, now) {
			expired++
			return m, 0, 0, false
		}
		return m, m.ChannelID, m.ID, botutil.GuildAllowed(m.GuildID)
	})
	// Nothing prunes the file while the bot is down. Rewrite it now rather
	// than at the first save, which waits for the gateway.
	if expired > 0 {
		b.Log.Info("Dropped expired messages from the cache file", "removed", expired)
		b.saveMessageCache()
	}
	loadCache(b, guildCacheFile, b.guildCache.cappedGroupedCache, func(g discord.Guild) (discord.Guild, snowflake.ID, snowflake.ID, bool) {
		return g, 0, g.ID, true
	})
	// Members filed under anything but a served guild (see guildMemberCache)
	// are dropped.
	loadCache(b, memberCacheFile, b.memberCache, func(m discord.Member) (discord.Member, snowflake.ID, snowflake.ID, bool) {
		return m, m.GuildID, m.User.ID, botutil.GuildAllowed(m.GuildID)
	})
	// Channels are an interface: UnmarshalChannel decodes the concrete type.
	loadCache(b, channelCacheFile, b.channelCache.cappedGroupedCache, func(w discord.UnmarshalChannel) (discord.GuildChannel, snowflake.ID, snowflake.ID, bool) {
		ch, ok := w.Channel.(discord.GuildChannel)
		if !ok {
			return nil, 0, 0, false
		}
		return ch, 0, ch.ID(), true
	})
	loadCache(b, roleCacheFile, b.roleCache, func(r discord.Role) (discord.Role, snowflake.ID, snowflake.ID, bool) {
		return r, r.GuildID, r.ID, true
	})
	loadCache(b, emojiCacheFile, b.emojiCache, func(e discord.Emoji) (discord.Emoji, snowflake.ID, snowflake.ID, bool) {
		return e, e.GuildID, e.ID, true
	})
	loadCache(b, stickerCacheFile, b.stickerCache, func(s discord.Sticker) (discord.Sticker, snowflake.ID, snowflake.ID, bool) {
		if s.GuildID == nil {
			return s, 0, s.ID, true
		}
		return s, *s.GuildID, s.ID, true
	})
	loadCache(b, voiceStateCacheFile, b.voiceStateCache, func(vs discord.VoiceState) (discord.VoiceState, snowflake.ID, snowflake.ID, bool) {
		return vs, vs.GuildID, vs.UserID, true
	})
	loadCache(b, stageInstanceCacheFile, b.stageInstanceCache, func(s discord.StageInstance) (discord.StageInstance, snowflake.ID, snowflake.ID, bool) {
		return s, s.GuildID, s.ID, true
	})
	loadCache(b, scheduledEventCacheFile, b.scheduledEventCache, func(e discord.GuildScheduledEvent) (discord.GuildScheduledEvent, snowflake.ID, snowflake.ID, bool) {
		return e, e.GuildID, e.ID, true
	})
	loadCache(b, soundboardSoundCacheFile, b.soundboardSoundCache, func(s discord.SoundboardSound) (discord.SoundboardSound, snowflake.ID, snowflake.ID, bool) {
		if s.GuildID == nil {
			return s, 0, s.SoundID, true
		}
		return s, *s.GuildID, s.SoundID, true
	})
}

// loadCache replaces c's contents with the items in file. entry turns each
// decoded item into the entity and the group and ID to file it under, or
// returns ok=false to skip it.
func loadCache[D, T any](b *Bot, file string, c *cappedGroupedCache[T], entry func(D) (entity T, groupID, id snowflake.ID, ok bool)) {
	n, err := c.load(func(add func(groupID, id snowflake.ID, entity T)) error {
		return readCacheFile(b.cacheDir, file, func(item D) {
			if entity, groupID, id, ok := entry(item); ok {
				add(groupID, id, entity)
			}
		})
	})
	if err != nil {
		b.Log.Error("Failed to load cache file", "file", file, "loaded", n, "error", err)
		return
	}
	b.Log.Info("Loaded cache file", "file", file, "count", n)
}

// saveCaches prunes expired messages, then writes every persisted cache to
// the cache dir. The saves run one after another, so only one snapshot is
// held at a time.
func (b *Bot) saveCaches() {
	b.pruneMessageCache()
	b.saveMessageCache()
	saveCache(b, memberCacheFile, b.memberCache.snapshot())
	saveCache(b, guildCacheFile, b.guildCache.snapshot())
	saveCache(b, channelCacheFile, b.channelCache.snapshot())
	saveCache(b, roleCacheFile, b.roleCache.snapshot())
	saveCache(b, emojiCacheFile, b.emojiCache.snapshot())
	saveCache(b, stickerCacheFile, b.stickerCache.snapshot())
	saveCache(b, voiceStateCacheFile, b.voiceStateCache.snapshot())
	saveCache(b, stageInstanceCacheFile, b.stageInstanceCache.snapshot())
	saveCache(b, scheduledEventCacheFile, b.scheduledEventCache.snapshot())
	saveCache(b, soundboardSoundCacheFile, b.soundboardSoundCache.snapshot())
}

func saveCache[T any](b *Bot, file string, items []T) bool {
	if err := writeCacheFile(b.cacheDir, file, items); err != nil {
		b.Log.Error("Failed to save cache file", "file", file, "error", err)
		return false
	}
	return true
}

// saveMessageCache writes the message cache, or deletes the file if that
// fails: a failed write leaves the old file in place, and while writes keep
// failing (a full disk, say) it would age past messageRetention.
func (b *Bot) saveMessageCache() {
	if saveCache(b, messageCacheFile, b.msgCache.snapshot()) {
		return
	}
	err := os.Remove(filepath.Join(b.cacheDir, messageCacheFile))
	switch {
	case err == nil:
		b.Log.Warn("Removed the message cache file after a failed save", "file", messageCacheFile)
	case !errors.Is(err, fs.ErrNotExist):
		b.Log.Error("Failed to remove the message cache file after a failed save", "file", messageCacheFile, "error", err)
	}
}

// readCacheFile decodes the JSON array in dir/name one element at a time,
// passing each to add, so the file is never in memory whole. A missing file
// is not an error.
func readCacheFile[T any](dir, name string, add func(T)) error {
	f, err := os.Open(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil // null: older versions saved an empty cache this way
	}
	if tok != json.Delim('[') {
		return fmt.Errorf("want a JSON array, got %v", tok)
	}
	for dec.More() {
		var item T
		if err := dec.Decode(&item); err != nil {
			return err
		}
		add(item)
	}
	_, err = dec.Token()
	return err
}

// writeCacheFile writes items to dir/name as a JSON array. It writes and
// syncs a temp file, then renames it over the old one, so a crash mid-save
// leaves the previous file intact. The files hold personal data, so they are
// readable by the owner only. Saves of a file must not run concurrently: they
// share the temp file.
func writeCacheFile[T any](dir, name string, items []T) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = writeJSONArray(f, items)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// writeJSONArray encodes items one at a time, so the whole array is never in
// memory as JSON.
func writeJSONArray[T any](f *os.File, items []T) error {
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	w.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			w.WriteByte(',')
		}
		if err := enc.Encode(item); err != nil {
			return err
		}
	}
	w.WriteByte(']')
	return w.Flush()
}
