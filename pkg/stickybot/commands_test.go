package stickybot

import (
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestPendingStickyStashAndGet(t *testing.T) {
	b := &Bot{pending: make(map[snowflake.ID]pendingSticky)}
	msg := discord.Message{ID: snowflake.ID(123), Content: "hello"}

	b.stashPendingSticky(msg)

	got, ok := b.getPendingSticky(msg.ID)
	if !ok {
		t.Fatal("expected stashed message, got miss")
	}
	if got.Content != "hello" {
		t.Errorf("got content %q, want %q", got.Content, "hello")
	}

	// Reads are non-destructive: a concurrent modal for the same message or a
	// duplicate submit must still resolve.
	if _, ok := b.getPendingSticky(msg.ID); !ok {
		t.Error("expected second get to hit, reads should be non-destructive")
	}
}

func TestPendingStickyGetMiss(t *testing.T) {
	b := &Bot{pending: make(map[snowflake.ID]pendingSticky)}
	if _, ok := b.getPendingSticky(snowflake.ID(999)); ok {
		t.Error("expected miss for unknown message ID")
	}
}

func TestPendingStickyGetExpired(t *testing.T) {
	b := &Bot{pending: make(map[snowflake.ID]pendingSticky)}
	msg := discord.Message{ID: snowflake.ID(1), Content: "old"}
	b.pending[msg.ID] = pendingSticky{msg: msg, storedAt: time.Now().Add(-pendingStickyTTL - time.Minute)}

	if _, ok := b.getPendingSticky(msg.ID); ok {
		t.Error("expected expired entry to miss")
	}
	if len(b.pending) != 0 {
		t.Error("expected expired entry to be pruned by get")
	}
}

func TestPendingStickyPrunesStaleEntries(t *testing.T) {
	b := &Bot{pending: make(map[snowflake.ID]pendingSticky)}
	stale := discord.Message{ID: snowflake.ID(1), Content: "old"}
	b.pending[stale.ID] = pendingSticky{msg: stale, storedAt: time.Now().Add(-pendingStickyTTL - time.Minute)}

	b.stashPendingSticky(discord.Message{ID: snowflake.ID(2), Content: "new"})

	if _, ok := b.pending[stale.ID]; ok {
		t.Error("expected stale entry to be pruned on stash")
	}
	if _, ok := b.pending[snowflake.ID(2)]; !ok {
		t.Error("expected fresh entry to be present")
	}
}

func TestValidateStickyParams(t *testing.T) {
	tests := []struct {
		name                                               string
		minIdle, maxIdle, threshold, timeThreshold, buffer int
		wantOK                                             bool
	}{
		{"valid defaults", 15, 30, 30, 10, 5, true},
		{"valid no buffer", 0, 10, 5, 0, 0, true},
		{"valid time only", 0, 10, 0, 5, 0, true},
		{"max_idle equals min_idle", 15, 15, 10, 0, 0, false},
		{"max_idle less than min_idle", 30, 15, 10, 0, 0, false},
		{"threshold equals buffer", 0, 10, 5, 0, 5, false},
		{"threshold less than buffer", 0, 10, 3, 0, 5, false},
		{"both thresholds zero", 0, 10, 0, 0, 0, false},
		{"threshold zero buffer nonzero ok", 0, 10, 0, 5, 3, true},
		{"min_idle zero valid", 0, 1, 1, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errMsg := validateStickyParams(tt.minIdle, tt.maxIdle, tt.threshold, tt.timeThreshold, tt.buffer)
			if tt.wantOK && errMsg != "" {
				t.Errorf("expected OK, got error: %q", errMsg)
			}
			if !tt.wantOK && errMsg == "" {
				t.Error("expected error, got OK")
			}
		})
	}
}
