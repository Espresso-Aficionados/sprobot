package bot

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/bot/handlers"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/botutil"
)

// --- Message retention ---

// retentionTestMessages returns an expired and a fresh message in each of
// two channels.
func retentionTestMessages() (expired, fresh []discord.Message) {
	for _, ch := range []snowflake.ID{1, 2} {
		expiredID := snowflake.New(time.Now().Add(-messageRetention - time.Hour))
		freshID := snowflake.New(time.Now().Add(-messageRetention + time.Hour))
		expired = append(expired, makeMessage(ch, expiredID+ch))
		fresh = append(fresh, makeMessage(ch, freshID+ch))
	}
	return expired, fresh
}

func TestPruneMessageCache(t *testing.T) {
	b := newCacheTestBot(t)
	expired, fresh := retentionTestMessages()
	for _, m := range slices.Concat(expired, fresh) {
		b.msgCache.Put(m.ChannelID, m.ID, m)
	}

	b.pruneMessageCache()

	for _, m := range expired {
		if _, ok := b.msgCache.Get(m.ChannelID, m.ID); ok {
			t.Errorf("expired message %d in channel %d was kept", m.ID, m.ChannelID)
		}
	}
	for _, m := range fresh {
		if _, ok := b.msgCache.Get(m.ChannelID, m.ID); !ok {
			t.Errorf("fresh message %d in channel %d was pruned", m.ID, m.ChannelID)
		}
	}
}

func TestLoadCaches_DropsExpiredMessages(t *testing.T) {
	b := newCacheTestBot(t)
	expired, fresh := retentionTestMessages()
	if err := writeCacheFile(b.cacheDir, messageCacheFile, slices.Concat(expired, fresh)); err != nil {
		t.Fatal(err)
	}

	b.loadCaches()

	if b.msgCache.Len() != len(fresh) {
		t.Errorf("loaded %d messages, want the %d fresh ones", b.msgCache.Len(), len(fresh))
	}
	for _, m := range fresh {
		if _, ok := b.msgCache.Get(m.ChannelID, m.ID); !ok {
			t.Errorf("fresh message %d in channel %d not loaded", m.ID, m.ChannelID)
		}
	}
}

func TestSaveCaches_ExcludesExpiredMessages(t *testing.T) {
	b := newCacheTestBot(t)
	expired, fresh := retentionTestMessages()
	for _, m := range slices.Concat(expired, fresh) {
		b.msgCache.Put(m.ChannelID, m.ID, m)
	}

	b.saveCaches()

	var saved []snowflake.ID
	err := readCacheFile(b.cacheDir, messageCacheFile, func(m discord.Message) { saved = append(saved, m.ID) })
	if err != nil {
		t.Fatal(err)
	}
	var want []snowflake.ID
	for _, m := range fresh {
		want = append(want, m.ID)
	}
	if !slices.Equal(saved, want) {
		t.Errorf("saved messages %v, want only the fresh ones %v", saved, want)
	}
}

// TestMessageExpired_Boundary: a message exactly messageRetention old is
// kept; a millisecond (snowflake resolution) older is not.
func TestMessageExpired_Boundary(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	atLimit := snowflake.New(now.Add(-messageRetention))
	if messageExpired(atLimit, now) {
		t.Error("message exactly at the retention limit counted as expired")
	}
	if !messageExpired(atLimit-1<<22, now) { // timestamp is the bits above 22
		t.Error("message 1ms past the retention limit was kept")
	}
	if messageExpired(snowflake.New(now), now) {
		t.Error("new message counted as expired")
	}
}

func makeMessage(channelID, messageID snowflake.ID) discord.Message {
	return discord.Message{
		ChannelID: channelID,
		ID:        messageID,
		Content:   "test",
	}
}

func TestCappedCache_PutGet(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)

	ch := snowflake.ID(1)
	msg := makeMessage(ch, 10)
	c.Put(ch, 10, msg)

	got, ok := c.Get(ch, 10)
	if !ok {
		t.Fatal("expected message to be found")
	}
	if got.ID != 10 {
		t.Fatalf("got ID %d, want 10", got.ID)
	}

	_, ok = c.Get(ch, 99)
	if ok {
		t.Fatal("expected message not found")
	}
}

func TestCappedCache_Eviction(t *testing.T) {
	const cap = 100
	c := newCappedGroupedCache[discord.Message](cap)
	ch := snowflake.ID(1)

	for i := 1; i <= cap+1; i++ {
		c.Put(ch, snowflake.ID(i), makeMessage(ch, snowflake.ID(i)))
	}

	if c.Len() != cap {
		t.Fatalf("len = %d, want %d", c.Len(), cap)
	}

	// Oldest (ID=1) should be evicted
	if _, ok := c.Get(ch, 1); ok {
		t.Fatal("expected oldest message to be evicted")
	}

	// Newest should still be present
	if _, ok := c.Get(ch, snowflake.ID(cap+1)); !ok {
		t.Fatal("expected newest message to be present")
	}
}

func TestCappedCache_RemoveThenEvict(t *testing.T) {
	const cap = 10
	c := newCappedGroupedCache[discord.Message](cap)
	ch := snowflake.ID(1)

	// Fill to capacity
	for i := 1; i <= cap; i++ {
		c.Put(ch, snowflake.ID(i), makeMessage(ch, snowflake.ID(i)))
	}

	// Remove a few from the middle
	c.Remove(ch, 3)
	c.Remove(ch, 5)
	if c.Len() != cap-2 {
		t.Fatalf("len after remove = %d, want %d", c.Len(), cap-2)
	}

	// Add enough to trigger eviction past the removed stale entries
	for i := cap + 1; i <= cap+5; i++ {
		c.Put(ch, snowflake.ID(i), makeMessage(ch, snowflake.ID(i)))
	}

	if c.Len() > cap {
		t.Fatalf("len after refill = %d, want <= %d", c.Len(), cap)
	}
}

func TestCappedCache_SnapshotLoad(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)
	ch1 := snowflake.ID(1)
	ch2 := snowflake.ID(2)

	c.Put(ch1, 10, makeMessage(ch1, 10))
	c.Put(ch1, 11, makeMessage(ch1, 11))
	c.Put(ch2, 20, makeMessage(ch2, 20))

	msgs := c.snapshot()
	if len(msgs) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(msgs))
	}

	c2 := newCappedGroupedCache[discord.Message](100)
	loadItems(c2, msgs, func(msg discord.Message) (snowflake.ID, snowflake.ID) {
		return msg.ChannelID, msg.ID
	})

	if c2.Len() != 3 {
		t.Fatalf("loaded len = %d, want 3", c2.Len())
	}
	if _, ok := c2.Get(ch1, 10); !ok {
		t.Fatal("expected ch1/10 after load")
	}
	if _, ok := c2.Get(ch2, 20); !ok {
		t.Fatal("expected ch2/20 after load")
	}
}

func TestCappedCache_PutOverwrite(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)
	ch := snowflake.ID(1)

	c.Put(ch, 10, discord.Message{ChannelID: ch, ID: 10, Content: "first"})
	c.Put(ch, 10, discord.Message{ChannelID: ch, ID: 10, Content: "second"})

	if c.Len() != 1 {
		t.Fatalf("len = %d, want 1 after overwrite", c.Len())
	}
	got, _ := c.Get(ch, 10)
	if got.Content != "second" {
		t.Fatalf("content = %q, want %q", got.Content, "second")
	}
}

func TestCappedCache_GroupRemove(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)
	ch1 := snowflake.ID(1)
	ch2 := snowflake.ID(2)

	c.Put(ch1, 10, makeMessage(ch1, 10))
	c.Put(ch1, 11, makeMessage(ch1, 11))
	c.Put(ch2, 20, makeMessage(ch2, 20))

	c.GroupRemove(ch1)
	if c.Len() != 1 {
		t.Fatalf("len after GroupRemove = %d, want 1", c.Len())
	}
	if _, ok := c.Get(ch1, 10); ok {
		t.Fatal("expected ch1 messages removed")
	}
	if _, ok := c.Get(ch2, 20); !ok {
		t.Fatal("expected ch2 messages still present")
	}
}

func TestCappedCache_RemoveIf(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)
	ch := snowflake.ID(1)

	c.Put(ch, 10, discord.Message{ChannelID: ch, ID: 10, Content: "keep"})
	c.Put(ch, 11, discord.Message{ChannelID: ch, ID: 11, Content: "remove"})
	c.Put(ch, 12, discord.Message{ChannelID: ch, ID: 12, Content: "keep"})

	c.RemoveIf(func(_ snowflake.ID, msg discord.Message) bool {
		return msg.Content == "remove"
	})

	if c.Len() != 2 {
		t.Fatalf("len after RemoveIf = %d, want 2", c.Len())
	}
	if _, ok := c.Get(ch, 11); ok {
		t.Fatal("expected message 11 to be removed")
	}
}

func TestCappedCache_SizeTracking(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)
	ch1 := snowflake.ID(1)
	ch2 := snowflake.ID(2)

	// Put several entries
	c.Put(ch1, 10, makeMessage(ch1, 10))
	c.Put(ch1, 11, makeMessage(ch1, 11))
	c.Put(ch2, 20, makeMessage(ch2, 20))
	if c.Len() != 3 {
		t.Fatalf("len after puts = %d, want 3", c.Len())
	}

	// Overwrite should not change size
	c.Put(ch1, 10, discord.Message{ChannelID: ch1, ID: 10, Content: "updated"})
	if c.Len() != 3 {
		t.Fatalf("len after overwrite = %d, want 3", c.Len())
	}

	// Remove one
	c.Remove(ch1, 11)
	if c.Len() != 2 {
		t.Fatalf("len after Remove = %d, want 2", c.Len())
	}

	// Remove non-existent should not change size
	c.Remove(ch1, 999)
	if c.Len() != 2 {
		t.Fatalf("len after Remove non-existent = %d, want 2", c.Len())
	}

	// GroupRemove
	c.Put(ch1, 12, makeMessage(ch1, 12))
	c.Put(ch1, 13, makeMessage(ch1, 13))
	// ch1 now has 10, 12, 13; ch2 has 20 → total 4
	if c.Len() != 4 {
		t.Fatalf("len before GroupRemove = %d, want 4", c.Len())
	}
	c.GroupRemove(ch1)
	if c.Len() != 1 {
		t.Fatalf("len after GroupRemove = %d, want 1", c.Len())
	}

	// GroupRemove on non-existent group
	c.GroupRemove(snowflake.ID(999))
	if c.Len() != 1 {
		t.Fatalf("len after GroupRemove non-existent = %d, want 1", c.Len())
	}

	// RemoveIf
	c.Put(ch2, 21, discord.Message{ChannelID: ch2, ID: 21, Content: "drop"})
	c.Put(ch2, 22, discord.Message{ChannelID: ch2, ID: 22, Content: "keep"})
	// ch2 has 20 (test), 21 (drop), 22 (keep) → total 3
	if c.Len() != 3 {
		t.Fatalf("len before RemoveIf = %d, want 3", c.Len())
	}
	c.RemoveIf(func(_ snowflake.ID, msg discord.Message) bool {
		return msg.Content == "drop"
	})
	if c.Len() != 2 {
		t.Fatalf("len after RemoveIf = %d, want 2", c.Len())
	}

	// GroupRemoveIf
	c.GroupRemoveIf(ch2, func(_ snowflake.ID, msg discord.Message) bool {
		return msg.Content == "keep"
	})
	if c.Len() != 1 {
		t.Fatalf("len after GroupRemoveIf = %d, want 1", c.Len())
	}
}

func TestCappedCache_GroupLen(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)
	ch1 := snowflake.ID(1)
	ch2 := snowflake.ID(2)

	if c.GroupLen(ch1) != 0 {
		t.Fatalf("GroupLen of empty group = %d, want 0", c.GroupLen(ch1))
	}

	c.Put(ch1, 10, makeMessage(ch1, 10))
	c.Put(ch1, 11, makeMessage(ch1, 11))
	c.Put(ch2, 20, makeMessage(ch2, 20))

	if c.GroupLen(ch1) != 2 {
		t.Fatalf("GroupLen(ch1) = %d, want 2", c.GroupLen(ch1))
	}
	if c.GroupLen(ch2) != 1 {
		t.Fatalf("GroupLen(ch2) = %d, want 1", c.GroupLen(ch2))
	}
	if c.GroupLen(snowflake.ID(999)) != 0 {
		t.Fatalf("GroupLen(nonexistent) = %d, want 0", c.GroupLen(snowflake.ID(999)))
	}
}

func TestCappedCache_All(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)
	ch1 := snowflake.ID(1)
	ch2 := snowflake.ID(2)

	c.Put(ch1, 10, makeMessage(ch1, 10))
	c.Put(ch1, 11, makeMessage(ch1, 11))
	c.Put(ch2, 20, makeMessage(ch2, 20))

	count := 0
	for range c.All() {
		count++
	}
	if count != 3 {
		t.Fatalf("All() yielded %d items, want 3", count)
	}
}

func TestCappedCache_AllEarlyBreak(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)
	ch := snowflake.ID(1)
	c.Put(ch, 10, makeMessage(ch, 10))
	c.Put(ch, 11, makeMessage(ch, 11))
	c.Put(ch, 12, makeMessage(ch, 12))

	count := 0
	for range c.All() {
		count++
		if count >= 2 {
			break
		}
	}
	if count != 2 {
		t.Fatalf("All() with early break yielded %d items, want 2", count)
	}
}

func TestCappedCache_GroupAll(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)
	ch1 := snowflake.ID(1)
	ch2 := snowflake.ID(2)

	c.Put(ch1, 10, makeMessage(ch1, 10))
	c.Put(ch1, 11, makeMessage(ch1, 11))
	c.Put(ch2, 20, makeMessage(ch2, 20))

	count := 0
	for range c.GroupAll(ch1) {
		count++
	}
	if count != 2 {
		t.Fatalf("GroupAll(ch1) yielded %d items, want 2", count)
	}

	count = 0
	for range c.GroupAll(snowflake.ID(999)) {
		count++
	}
	if count != 0 {
		t.Fatalf("GroupAll(nonexistent) yielded %d items, want 0", count)
	}
}

func TestCappedCache_GroupAllEarlyBreak(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)
	ch := snowflake.ID(1)
	c.Put(ch, 10, makeMessage(ch, 10))
	c.Put(ch, 11, makeMessage(ch, 11))
	c.Put(ch, 12, makeMessage(ch, 12))

	count := 0
	for range c.GroupAll(ch) {
		count++
		if count >= 1 {
			break
		}
	}
	if count != 1 {
		t.Fatalf("GroupAll with early break yielded %d items, want 1", count)
	}
}

func TestCappedCache_AllEmpty(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](100)
	count := 0
	for range c.All() {
		count++
	}
	if count != 0 {
		t.Fatalf("All() on empty cache yielded %d items, want 0", count)
	}
}

func TestCappedCache_EvictionCompaction(t *testing.T) {
	const cap = 50
	c := newCappedGroupedCache[discord.Message](cap)
	ch := snowflake.ID(1)

	// Overflow many times to trigger repeated evictions and compactions.
	for i := 1; i <= 10*orderSlack; i++ {
		c.Put(ch, snowflake.ID(i), makeMessage(ch, snowflake.ID(i)))
	}

	if c.Len() != cap {
		t.Fatalf("len = %d, want %d", c.Len(), cap)
	}
	if len(c.order) > 2*cap+orderSlack {
		t.Fatalf("order slice length = %d, want <= %d", len(c.order), 2*cap+orderSlack)
	}
}

// TestCappedCache_OrderBoundedBelowCap: removals never reach evict, so they
// must compact order themselves.
func TestCappedCache_OrderBoundedBelowCap(t *testing.T) {
	c := newCappedGroupedCache[discord.Member](1000000)
	guild := snowflake.ID(1)
	for i := 1; i <= 10*orderSlack; i++ {
		c.Put(guild, snowflake.ID(i), discord.Member{})
		switch i % 3 {
		case 0:
			c.Remove(guild, snowflake.ID(i))
		case 1:
			c.RemoveIf(func(_ snowflake.ID, _ discord.Member) bool { return false }) // no-op
		case 2:
			c.GroupRemoveIf(guild, func(_ snowflake.ID, _ discord.Member) bool { return true })
		}
	}
	if len(c.order) > 2*c.Len()+orderSlack {
		t.Fatalf("order slice length = %d with %d live entries", len(c.order), c.Len())
	}
}

// TestCappedCache_ReaddedEntryIsNewest: an entry removed and put back must
// not be evicted from its old place in the order.
func TestCappedCache_ReaddedEntryIsNewest(t *testing.T) {
	c := newCappedGroupedCache[discord.Member](2)
	guild := snowflake.ID(1)
	c.Put(guild, 1, discord.Member{})
	c.Put(guild, 2, discord.Member{})
	c.Remove(guild, 1)
	c.Put(guild, 1, discord.Member{}) // now newer than 2
	c.Put(guild, 3, discord.Member{}) // over the cap: evicts 2

	if _, ok := c.Get(guild, 2); ok {
		t.Error("2 is the oldest entry but was kept")
	}
	for _, id := range []snowflake.ID{1, 3} {
		if _, ok := c.Get(guild, id); !ok {
			t.Errorf("%d was evicted", id)
		}
	}
}

func TestCappedCache_EvictsDownToMaxSize(t *testing.T) {
	c := newCappedGroupedCache[discord.Member](10)
	for i := 1; i <= 10; i++ {
		c.Put(1, snowflake.ID(i), discord.Member{})
	}
	c.maxSize = 5
	c.Put(1, 11, discord.Member{})
	if c.Len() != 5 {
		t.Fatalf("len = %d, want 5", c.Len())
	}
	for i := 7; i <= 11; i++ {
		if _, ok := c.Get(1, snowflake.ID(i)); !ok {
			t.Errorf("newest entry %d was evicted", i)
		}
	}
}

func TestCappedCache_SnapshotInInsertionOrder(t *testing.T) {
	c := newCappedGroupedCache[discord.Role](1000)
	var want []snowflake.ID
	for i := 0; i < 200; i++ {
		id := snowflake.ID(1000 - i) // descending, so neither ID nor map order
		c.Put(snowflake.ID(i%7), id, discord.Role{ID: id})
		want = append(want, id)
	}
	moved := want[3]
	c.Remove(3, moved)
	c.Put(3, moved, discord.Role{ID: moved}) // now the newest
	want = append(slices.Delete(want, 3, 4), moved)

	var got []snowflake.ID
	for _, r := range c.snapshot() {
		got = append(got, r.ID)
	}
	if !slices.Equal(got, want) {
		t.Errorf("snapshot order = %v,\nwant %v", got, want)
	}
}

// TestCappedCache_LoadTrimsOldest: entries load oldest first and the cap
// applies, evicting from the start of the file.
func TestCappedCache_LoadTrimsOldest(t *testing.T) {
	c := newCappedGroupedCache[discord.Role](3)
	var roles []discord.Role
	for _, id := range []snowflake.ID{50, 40, 30, 20, 10} {
		roles = append(roles, discord.Role{ID: id, GuildID: 1})
	}
	loadItems(c, roles, func(r discord.Role) (snowflake.ID, snowflake.ID) { return r.GuildID, r.ID })

	if c.Len() != 3 {
		t.Fatalf("len = %d, want 3", c.Len())
	}
	c.Put(1, 60, discord.Role{ID: 60})
	var got []snowflake.ID
	for _, r := range c.snapshot() {
		got = append(got, r.ID)
	}
	if want := []snowflake.ID{20, 10, 60}; !slices.Equal(got, want) {
		t.Errorf("after load and put: %v, want %v", got, want)
	}
}

// TestCappedCache_LoadByID: the message cache orders loaded entries by ID,
// so a file saved in map order still evicts the oldest messages first.
func TestCappedCache_LoadByID(t *testing.T) {
	c := newCappedGroupedCache[discord.Message](3)
	c.loadByID = true
	var msgs []discord.Message
	for _, id := range []snowflake.ID{30, 10, 50, 20, 40} {
		msgs = append(msgs, makeMessage(id/10%2, id))
	}
	loadItems(c, msgs, func(m discord.Message) (snowflake.ID, snowflake.ID) { return m.ChannelID, m.ID })

	c.Put(0, 60, makeMessage(0, 60))
	var got []snowflake.ID
	for _, m := range c.snapshot() {
		got = append(got, m.ID)
	}
	if want := []snowflake.ID{40, 50, 60}; !slices.Equal(got, want) {
		t.Errorf("after load and put: %v, want %v", got, want)
	}
}

// --- Ungrouped cappedCache[T] tests ---

func TestUngroupedCache_PutGet(t *testing.T) {
	c := newCappedCache[discord.Guild](100)
	g := discord.Guild{ID: 10, Name: "test"}
	c.Put(10, g)

	got, ok := c.Get(10)
	if !ok {
		t.Fatal("expected guild to be found")
	}
	if got.Name != "test" {
		t.Fatalf("got name %q, want %q", got.Name, "test")
	}

	_, ok = c.Get(99)
	if ok {
		t.Fatal("expected guild not found")
	}
}

func TestUngroupedCache_Eviction(t *testing.T) {
	const cap = 50
	c := newCappedCache[discord.Guild](cap)

	for i := 1; i <= cap+1; i++ {
		c.Put(snowflake.ID(i), discord.Guild{ID: snowflake.ID(i)})
	}

	if c.Len() != cap {
		t.Fatalf("len = %d, want %d", c.Len(), cap)
	}

	if _, ok := c.Get(1); ok {
		t.Fatal("expected oldest to be evicted")
	}
	if _, ok := c.Get(snowflake.ID(cap + 1)); !ok {
		t.Fatal("expected newest to be present")
	}
}

func TestUngroupedCache_PutOverwrite(t *testing.T) {
	c := newCappedCache[discord.Guild](100)
	c.Put(10, discord.Guild{ID: 10, Name: "first"})
	c.Put(10, discord.Guild{ID: 10, Name: "second"})

	if c.Len() != 1 {
		t.Fatalf("len = %d, want 1", c.Len())
	}
	got, _ := c.Get(10)
	if got.Name != "second" {
		t.Fatalf("name = %q, want %q", got.Name, "second")
	}
}

func TestUngroupedCache_Remove(t *testing.T) {
	c := newCappedCache[discord.Guild](100)
	c.Put(10, discord.Guild{ID: 10})
	c.Put(11, discord.Guild{ID: 11})

	removed, ok := c.Remove(10)
	if !ok {
		t.Fatal("expected remove to succeed")
	}
	if removed.ID != 10 {
		t.Fatalf("removed ID = %d, want 10", removed.ID)
	}
	if c.Len() != 1 {
		t.Fatalf("len = %d, want 1", c.Len())
	}

	_, ok = c.Remove(999)
	if ok {
		t.Fatal("expected remove non-existent to return false")
	}
}

func TestUngroupedCache_RemoveIf(t *testing.T) {
	c := newCappedCache[discord.Guild](100)
	c.Put(10, discord.Guild{ID: 10, Name: "keep"})
	c.Put(11, discord.Guild{ID: 11, Name: "drop"})
	c.Put(12, discord.Guild{ID: 12, Name: "keep"})

	c.RemoveIf(func(g discord.Guild) bool { return g.Name == "drop" })

	if c.Len() != 2 {
		t.Fatalf("len = %d, want 2", c.Len())
	}
	if _, ok := c.Get(11); ok {
		t.Fatal("expected 11 to be removed")
	}
}

func TestUngroupedCache_SnapshotLoad(t *testing.T) {
	c := newCappedCache[discord.Guild](100)
	c.Put(10, discord.Guild{ID: 10, Name: "a"})
	c.Put(11, discord.Guild{ID: 11, Name: "b"})

	items := c.snapshot()
	if len(items) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(items))
	}

	c2 := newCappedCache[discord.Guild](100)
	loadItems(c2.cappedGroupedCache, items, func(g discord.Guild) (snowflake.ID, snowflake.ID) { return 0, g.ID })

	if c2.Len() != 2 {
		t.Fatalf("loaded len = %d, want 2", c2.Len())
	}
	if _, ok := c2.Get(10); !ok {
		t.Fatal("expected 10 after load")
	}
	if _, ok := c2.Get(11); !ok {
		t.Fatal("expected 11 after load")
	}
}

func TestUngroupedCache_All(t *testing.T) {
	c := newCappedCache[discord.Guild](100)
	c.Put(10, discord.Guild{ID: 10})
	c.Put(11, discord.Guild{ID: 11})
	c.Put(12, discord.Guild{ID: 12})

	count := 0
	for range c.All() {
		count++
	}
	if count != 3 {
		t.Fatalf("All() yielded %d, want 3", count)
	}
}

func TestUngroupedCache_EvictionCompaction(t *testing.T) {
	const cap = 50
	c := newCappedCache[discord.Guild](cap)

	for i := 1; i <= 10*orderSlack; i++ {
		c.Put(snowflake.ID(i), discord.Guild{ID: snowflake.ID(i)})
	}

	if c.Len() != cap {
		t.Fatalf("len = %d, want %d", c.Len(), cap)
	}
	if len(c.order) > 2*cap+orderSlack {
		t.Fatalf("order slice length = %d, want <= %d", len(c.order), 2*cap+orderSlack)
	}
}

// --- Completeness guard ---

// persistedCacheFiles maps each enabled cache flag to its disk file.
var persistedCacheFiles = map[cache.Flags]string{
	cache.FlagGuilds:                guildCacheFile,
	cache.FlagChannels:              channelCacheFile,
	cache.FlagMessages:              messageCacheFile,
	cache.FlagMembers:               memberCacheFile,
	cache.FlagRoles:                 roleCacheFile,
	cache.FlagEmojis:                emojiCacheFile,
	cache.FlagStickers:              stickerCacheFile,
	cache.FlagVoiceStates:           voiceStateCacheFile,
	cache.FlagStageInstances:        stageInstanceCacheFile,
	cache.FlagGuildScheduledEvents:  scheduledEventCacheFile,
	cache.FlagGuildSoundboardSounds: soundboardSoundCacheFile,
}

// TestCacheFlagsClassified fails when a disgo upgrade adds a cache flag that
// is neither persisted nor deliberately disabled, forcing a decision about
// storing the new data.
func TestCacheFlagsClassified(t *testing.T) {
	for bit := cache.Flags(1); bit <= cache.FlagsAll; bit <<= 1 {
		if !cache.FlagsAll.Has(bit) {
			continue
		}
		_, persisted := persistedCacheFiles[bit]
		_, disabled := disabledCacheFlags[bit]
		switch {
		case persisted && disabled:
			t.Errorf("cache flag %d is both persisted and disabled", bit)
		case !persisted && !disabled:
			t.Errorf("cache flag %d is unclassified: persist it or add it to disabledCacheFlags with a reason", bit)
		case persisted != enabledCacheFlags.Has(bit):
			t.Errorf("cache flag %d: persisted=%v but enabled=%v", bit, persisted, enabledCacheFlags.Has(bit))
		}
	}
	for flag, file := range persistedCacheFiles {
		if file == "" {
			t.Errorf("cache flag %d has no cache file", flag)
		}
	}
	for flag, reason := range disabledCacheFlags {
		if reason == "" {
			t.Errorf("cache flag %d is disabled without a reason", flag)
		}
	}
}

// TestDisabledCachesStoreNothing checks the flags actually switch the
// disabled caches off: disgo only honours flags for the default caches it
// builds, so this fails if a custom cache is passed for a disabled type.
func TestDisabledCachesStoreNothing(t *testing.T) {
	c := cache.New(newCacheTestBot(t).cacheConfig()...)
	guildID, threadID, userID := snowflake.ID(1), snowflake.ID(2), snowflake.ID(3)

	c.AddPresence(discord.Presence{GuildID: guildID, PresenceUser: discord.PresenceUser{ID: userID}})
	if _, ok := c.Presence(guildID, userID); ok {
		t.Error("presence was cached with presences disabled")
	}
	c.AddThreadMember(discord.ThreadMember{ThreadID: threadID, UserID: userID})
	if _, ok := c.ThreadMember(threadID, userID); ok {
		t.Error("thread member was cached with thread members disabled")
	}
}

// --- Fixes for disgo's cache handling ---

// gatewayTestClient returns a disgo client over b's caches, to feed gateway
// events through disgo's own handlers.
func gatewayTestClient(b *Bot) *bot.Client {
	client := &bot.Client{Caches: cache.New(b.cacheConfig()...), Logger: slog.New(slog.DiscardHandler)}
	client.EventManager = bot.NewEventManager(client)
	return client
}

func handleGatewayEvent(client *bot.Client, eventType gateway.EventType, event gateway.EventData) {
	handlers.GetGatewayHandlers()[eventType].HandleGatewayEvent(client, 0, 0, event)
}

// TestMemberCache_SkipsThreadMembers: disgo's THREAD_MEMBERS_UPDATE handler
// adds the joining member to the member cache under the thread ID.
func TestMemberCache_SkipsThreadMembers(t *testing.T) {
	b := newCacheTestBot(t)
	client := gatewayTestClient(b)
	guildID, threadID, userID := botutil.AllowedGuildIDs[0], snowflake.ID(500), snowflake.ID(3)

	handleGatewayEvent(client, gateway.EventTypeThreadMembersUpdate, gateway.EventThreadMembersUpdate{
		ID:      threadID,
		GuildID: guildID,
		AddedMembers: []gateway.AddedThreadMember{{
			ThreadMember: discord.ThreadMember{ThreadID: threadID, UserID: userID},
			Member:       discord.Member{User: discord.User{ID: userID}},
		}},
	})
	if n := b.memberCache.Len(); n != 0 {
		t.Errorf("member cache holds %d entries after a thread join, want 0", n)
	}

	// Members of an allowed guild, or of one in the guild cache, are kept.
	otherGuildID := snowflake.ID(600)
	b.guildCache.Put(otherGuildID, discord.Guild{ID: otherGuildID})
	for _, id := range []snowflake.ID{guildID, otherGuildID} {
		handleGatewayEvent(client, gateway.EventTypeGuildMemberAdd, gateway.EventGuildMemberAdd{
			Member: discord.Member{GuildID: id, User: discord.User{ID: userID}},
		})
		if _, ok := b.memberCache.Get(id, userID); !ok {
			t.Errorf("member of guild %d not cached", id)
		}
	}
}

func TestLoadCaches_DropsMembersOutsideGuilds(t *testing.T) {
	b := newCacheTestBot(t)
	allowedID, cachedGuildID, threadID := botutil.AllowedGuildIDs[0], snowflake.ID(600), snowflake.ID(500)
	if err := writeCacheFile(b.cacheDir, guildCacheFile, []discord.Guild{{ID: cachedGuildID}}); err != nil {
		t.Fatal(err)
	}
	members := []discord.Member{
		{GuildID: allowedID, User: discord.User{ID: 1}},
		{GuildID: cachedGuildID, User: discord.User{ID: 2}},
		{GuildID: threadID, User: discord.User{ID: 3}},
	}
	if err := writeCacheFile(b.cacheDir, memberCacheFile, members); err != nil {
		t.Fatal(err)
	}

	b.loadCaches()

	if b.memberCache.Len() != 2 {
		t.Errorf("loaded %d members, want 2", b.memberCache.Len())
	}
	if _, ok := b.memberCache.Get(threadID, 3); ok {
		t.Error("member filed under a thread ID was loaded")
	}
	for _, m := range members[:2] {
		if _, ok := b.memberCache.Get(m.GuildID, m.User.ID); !ok {
			t.Errorf("member of guild %d not loaded", m.GuildID)
		}
	}
}

// TestGuildOutage_KeepsMessagesAndMembers runs disgo's GUILD_DELETE and
// GUILD_CREATE handlers through an outage, then a real departure.
func TestGuildOutage_KeepsMessagesAndMembers(t *testing.T) {
	b := newCacheTestBot(t)
	client := gatewayTestClient(b)
	guildID, channelID, userID := botutil.AllowedGuildIDs[0], snowflake.ID(2), snowflake.ID(3)
	msgID := snowflake.New(time.Now())
	msg := makeMessage(channelID, msgID)
	msg.GuildID = &guildID
	b.msgCache.Put(channelID, msgID, msg)
	b.memberCache.Put(guildID, userID, discord.Member{GuildID: guildID, User: discord.User{ID: userID}})
	check := func(when string, want bool) {
		t.Helper()
		if _, ok := b.msgCache.Get(channelID, msgID); ok != want {
			t.Errorf("%s: message cached = %v, want %v", when, ok, want)
		}
		if _, ok := b.memberCache.Get(guildID, userID); ok != want {
			t.Errorf("%s: member cached = %v, want %v", when, ok, want)
		}
	}

	handleGatewayEvent(client, gateway.EventTypeGuildDelete, gateway.EventGuildDelete{
		UnavailableGuild: discord.UnavailableGuild{ID: guildID, Unavailable: true},
	})
	check("guild unavailable", true)

	handleGatewayEvent(client, gateway.EventTypeGuildCreate, gateway.EventGuildCreate{
		GatewayGuild: discord.GatewayGuild{RestGuild: discord.RestGuild{Guild: discord.Guild{ID: guildID}}},
	})
	check("guild back", true)

	handleGatewayEvent(client, gateway.EventTypeGuildDelete, gateway.EventGuildDelete{
		UnavailableGuild: discord.UnavailableGuild{ID: guildID},
	})
	check("left guild", false)
}

// --- Persistence ---

// newCacheTestBot returns a bot with empty caches persisted to a temp dir.
func newCacheTestBot(t *testing.T) *Bot {
	t.Helper()
	b := &Bot{
		BaseBot:  &botutil.BaseBot{Log: slog.New(slog.DiscardHandler)},
		cacheDir: t.TempDir(),
	}
	b.initCaches()
	return b
}

// loadItems loads items into c in order, as loadCaches does from a file.
func loadItems[T any](c *cappedGroupedCache[T], items []T, key func(T) (groupID, id snowflake.ID)) {
	c.load(func(add func(snowflake.ID, snowflake.ID, T)) error {
		for _, item := range items {
			groupID, id := key(item)
			add(groupID, id, item)
		}
		return nil
	})
}

func unmarshalChannel(t *testing.T, data string) discord.GuildChannel {
	t.Helper()
	var w discord.UnmarshalChannel
	if err := json.Unmarshal([]byte(data), &w); err != nil {
		t.Fatal(err)
	}
	return w.Channel.(discord.GuildChannel)
}

func TestCachesRoundTrip(t *testing.T) {
	b := newCacheTestBot(t)
	guildID, channelID, userID := snowflake.ID(1), snowflake.ID(2), snowflake.ID(3)
	msgID := snowflake.New(time.Now().Add(-time.Hour))
	b.msgCache.Put(channelID, msgID, discord.Message{
		ID: msgID, ChannelID: channelID, GuildID: &guildID, Content: "hello",
		Author: discord.User{ID: userID, Username: "someone"},
	})
	b.memberCache.Put(guildID, userID, discord.Member{GuildID: guildID, User: discord.User{ID: userID}})
	b.guildCache.Put(guildID, discord.Guild{ID: guildID, Name: "guild"})
	b.channelCache.Put(channelID, unmarshalChannel(t, `{"id":"2","type":0,"guild_id":"1","name":"general"}`))
	b.roleCache.Put(guildID, 4, discord.Role{ID: 4, GuildID: guildID})
	b.emojiCache.Put(guildID, 5, discord.Emoji{ID: 5, GuildID: guildID})
	b.stickerCache.Put(guildID, 6, discord.Sticker{ID: 6, GuildID: &guildID})
	b.voiceStateCache.Put(guildID, userID, discord.VoiceState{GuildID: guildID, UserID: userID})
	b.stageInstanceCache.Put(guildID, 7, discord.StageInstance{ID: 7, GuildID: guildID})
	b.scheduledEventCache.Put(guildID, 8, discord.GuildScheduledEvent{ID: 8, GuildID: guildID})
	b.soundboardSoundCache.Put(guildID, 9, discord.SoundboardSound{SoundID: 9, GuildID: &guildID})

	b.saveCaches()

	for flag, file := range persistedCacheFiles {
		info, err := os.Stat(filepath.Join(b.cacheDir, file))
		if err != nil {
			t.Errorf("cache flag %d: %v", flag, err)
			continue
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s has mode %o, want 600", file, perm)
		}
	}
	if entries, _ := os.ReadDir(b.cacheDir); len(entries) != len(persistedCacheFiles) {
		t.Errorf("cache dir has %d files, want %d (temp files left behind?)", len(entries), len(persistedCacheFiles))
	}

	got := newCacheTestBot(t)
	got.cacheDir = b.cacheDir
	got.loadCaches()

	if m, ok := got.msgCache.Get(channelID, msgID); !ok || m.Content != "hello" || m.Author.ID != userID || *m.GuildID != guildID {
		t.Errorf("message = %+v, %v", m, ok)
	}
	if _, ok := got.memberCache.Get(guildID, userID); !ok {
		t.Error("member not loaded")
	}
	if g, ok := got.guildCache.Get(guildID); !ok || g.Name != "guild" {
		t.Errorf("guild = %+v, %v", g, ok)
	}
	if ch, ok := got.channelCache.Get(channelID); !ok || ch.Name() != "general" || ch.GuildID() != guildID {
		t.Errorf("channel = %+v, %v", ch, ok)
	}
	for name, ok := range map[string]bool{
		"role":            has(got.roleCache.Get(guildID, 4)),
		"emoji":           has(got.emojiCache.Get(guildID, 5)),
		"sticker":         has(got.stickerCache.Get(guildID, 6)),
		"voice state":     has(got.voiceStateCache.Get(guildID, userID)),
		"stage instance":  has(got.stageInstanceCache.Get(guildID, 7)),
		"scheduled event": has(got.scheduledEventCache.Get(guildID, 8)),
		"soundboard":      has(got.soundboardSoundCache.Get(guildID, 9)),
	} {
		if !ok {
			t.Errorf("%s not loaded", name)
		}
	}
}

func has[T any](_ T, ok bool) bool { return ok }

// TestCachesRoundTrip_KeepsEvictionOrder: after a restart the oldest entries
// are still the first evicted.
func TestCachesRoundTrip_KeepsEvictionOrder(t *testing.T) {
	b := newCacheTestBot(t)
	guildID := botutil.AllowedGuildIDs[0]
	for _, userID := range []snowflake.ID{50, 30, 10, 40, 20} {
		b.memberCache.Put(guildID, userID, discord.Member{GuildID: guildID, User: discord.User{ID: userID}})
	}
	b.saveCaches()

	got := newCacheTestBot(t)
	got.cacheDir = b.cacheDir
	got.memberCache.maxSize = 3
	got.loadCaches()
	got.memberCache.Put(guildID, 60, discord.Member{GuildID: guildID, User: discord.User{ID: 60}})

	var ids []snowflake.ID
	for _, m := range got.memberCache.snapshot() {
		ids = append(ids, m.User.ID)
	}
	if want := []snowflake.ID{40, 20, 60}; !slices.Equal(ids, want) {
		t.Errorf("members after restart and put = %v, want %v", ids, want)
	}
}

// TestLoadCaches_LegacyFiles loads files as the old per-cache savers wrote
// them: compact json.Marshal output, and null for an empty grouped cache.
func TestLoadCaches_LegacyFiles(t *testing.T) {
	b := newCacheTestBot(t)
	msgID := snowflake.New(time.Now().Add(-time.Hour))
	guildID := snowflake.ID(1)
	old, err := json.Marshal([]discord.Message{{
		ID: msgID, ChannelID: 2, GuildID: &guildID, Content: "old format",
		Author: discord.User{ID: 3, Username: "someone"},
		Member: &discord.Member{User: discord.User{ID: 3}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for file, data := range map[string][]byte{
		messageCacheFile: old,
		roleCacheFile:    []byte("null"),
	} {
		if err := os.WriteFile(filepath.Join(b.cacheDir, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	b.loadCaches()

	if m, ok := b.msgCache.Get(2, msgID); !ok || m.Content != "old format" || m.Author.Username != "someone" {
		t.Errorf("message = %+v, %v", m, ok)
	}
	if b.roleCache.Len() != 0 {
		t.Errorf("role cache len = %d, want 0", b.roleCache.Len())
	}
}

func TestReadCacheFile(t *testing.T) {
	tests := []struct {
		name    string
		data    string // "-": no file
		want    []int
		wantErr bool
	}{
		{name: "missing file", data: "-"},
		{name: "null", data: "null"},
		{name: "empty array", data: "[]"},
		{name: "compact", data: "[1,2,3]", want: []int{1, 2, 3}},
		{name: "one per line", data: "[1\n,2\n]\n", want: []int{1, 2}},
		// Entries before the damage are kept: dropping them helps nobody.
		{name: "truncated", data: "[1,2,", want: []int{1, 2}, wantErr: true},
		{name: "not an array", data: `{"a":1}`, wantErr: true},
		{name: "empty file", data: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.data != "-" {
				if err := os.WriteFile(filepath.Join(dir, "c.json"), []byte(tt.data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var got []int
			err := readCacheFile(dir, "c.json", func(i int) { got = append(got, i) })
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWriteCacheFile_FailureKeepsOldFile checks a failed save leaves the
// previous file whole, with no temp file behind.
func TestWriteCacheFile_FailureKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeCacheFile(dir, "c.json", []any{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := writeCacheFile(dir, "c.json", []any{3, func() {}}); err == nil {
		t.Fatal("expected an error encoding a func")
	}

	var got []int
	if err := readCacheFile(dir, "c.json", func(i int) { got = append(got, i) }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int{1, 2}) {
		t.Errorf("got %v, want [1 2]", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.json.tmp")); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

// TestWriteCacheFile_TightensMode checks a file the old savers wrote with
// mode 0o644 is replaced by an owner-only one.
func TestWriteCacheFile_TightensMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeCacheFile(dir, "c.json", []int{1}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}
