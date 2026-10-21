package botutil

import (
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestMessageStashPutAndGet(t *testing.T) {
	var s MessageStash
	msg := discord.Message{ID: snowflake.ID(123), Content: "hello"}

	s.Put(msg)

	got, ok := s.Get(msg.ID)
	if !ok {
		t.Fatal("expected stashed message, got miss")
	}
	if got.Content != "hello" {
		t.Errorf("got content %q, want %q", got.Content, "hello")
	}

	// Reads are non-destructive: a concurrent modal for the same message or a
	// duplicate submit must still resolve.
	if _, ok := s.Get(msg.ID); !ok {
		t.Error("expected second get to hit, reads should be non-destructive")
	}
}

func TestMessageStashGetMiss(t *testing.T) {
	var s MessageStash
	if _, ok := s.Get(snowflake.ID(999)); ok {
		t.Error("expected miss for unknown message ID")
	}
}

func TestMessageStashGetExpired(t *testing.T) {
	var s MessageStash
	msg := discord.Message{ID: snowflake.ID(1), Content: "old"}
	s.entries = map[snowflake.ID]stashedMessage{
		msg.ID: {msg: msg, storedAt: time.Now().Add(-MessageStashTTL - time.Minute)},
	}

	if _, ok := s.Get(msg.ID); ok {
		t.Error("expected expired entry to miss")
	}
	if len(s.entries) != 0 {
		t.Error("expected expired entry to be pruned by get")
	}
}

func TestMessageStashPrunesStaleEntries(t *testing.T) {
	var s MessageStash
	stale := discord.Message{ID: snowflake.ID(1), Content: "old"}
	s.entries = map[snowflake.ID]stashedMessage{
		stale.ID: {msg: stale, storedAt: time.Now().Add(-MessageStashTTL - time.Minute)},
	}

	s.Put(discord.Message{ID: snowflake.ID(2), Content: "new"})

	if _, ok := s.entries[stale.ID]; ok {
		t.Error("expected stale entry to be pruned on put")
	}
	if _, ok := s.entries[snowflake.ID(2)]; !ok {
		t.Error("expected fresh entry to be present")
	}
}
