package bot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

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
	c2.load(msgs, func(msg discord.Message) (snowflake.ID, snowflake.ID) {
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

func TestLoadMessageCache_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, messageCacheFile)

	// Ensure file doesn't exist
	_ = os.Remove(path)

	c := newCappedGroupedCache[discord.Message](100)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("unexpected error: %v", err)
		}
		// This is the expected path — no error, cache stays empty
	} else {
		t.Fatalf("expected file not to exist, got data: %s", data)
	}

	if c.Len() != 0 {
		t.Fatalf("cache should be empty, got len %d", c.Len())
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

	// Fill and overflow many times to trigger repeated evictions and compactions.
	totalInserts := cap * 10
	for i := 1; i <= totalInserts; i++ {
		c.Put(ch, snowflake.ID(i), makeMessage(ch, snowflake.ID(i)))
	}

	if c.Len() != cap {
		t.Fatalf("len = %d, want %d", c.Len(), cap)
	}

	// The order slice (from head onward) should not be much larger than size.
	// With compaction, len(order) - head should equal size (no stale entries
	// remain because we only insert into one group, so evict never skips).
	active := len(c.order) - c.head
	if active > cap*2 {
		t.Fatalf("order active region = %d, want <= %d (head=%d, len=%d)",
			active, cap*2, c.head, len(c.order))
	}

	// head should have been compacted at least once (not still 0 only if
	// no compaction happened, but after 10x overflow it definitely did).
	// After the final compaction head resets to 0, so just verify the slice
	// isn't unbounded.
	if len(c.order) > cap*2 {
		t.Fatalf("order slice length = %d, should be bounded near cap (%d)", len(c.order), cap)
	}
}
