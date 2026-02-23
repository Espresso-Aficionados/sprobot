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
	c := newCappedGroupedCache(100)

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
	c := newCappedGroupedCache(cap)
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
	c := newCappedGroupedCache(cap)
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
	c := newCappedGroupedCache(100)
	ch1 := snowflake.ID(1)
	ch2 := snowflake.ID(2)

	c.Put(ch1, 10, makeMessage(ch1, 10))
	c.Put(ch1, 11, makeMessage(ch1, 11))
	c.Put(ch2, 20, makeMessage(ch2, 20))

	msgs := c.snapshot()
	if len(msgs) != 3 {
		t.Fatalf("snapshot len = %d, want 3", len(msgs))
	}

	c2 := newCappedGroupedCache(100)
	c2.load(msgs)

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
	c := newCappedGroupedCache(100)
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

	c := newCappedGroupedCache(100)
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
	c := newCappedGroupedCache(100)
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
	c := newCappedGroupedCache(100)
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
