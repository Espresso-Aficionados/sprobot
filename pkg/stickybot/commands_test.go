package stickybot

import (
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
)

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

// An attachment that can't be saved is left out of the sticky, and the
// moderator is told which one. Loopback URLs are refused before any fetch.
func TestCreateStickyReportsUnsavedAttachments(t *testing.T) {
	unsaved := discord.Attachment{Filename: "my_photo.png", ProxyURL: "http://127.0.0.1/my_photo.png"}

	t.Run("sticky without the attachment", func(t *testing.T) {
		f := &fakeDiscord{}
		b, _ := newFakeBot(t, f)
		s := &stickyMessage{ChannelID: 500, GuildID: testGuildID, Active: true, MaxIdleMins: 10, MsgThreshold: 1}

		reply := b.createSticky(s, discord.Message{Content: "hello", Attachments: []discord.Attachment{unsaved}})

		if !strings.HasPrefix(reply, "Sticky created") || !strings.Contains(reply, "`my_photo.png`") {
			t.Errorf("reply = %q, want a confirmation naming `my_photo.png`", reply)
		}
		if posts := f.postsMade(); len(posts) != 1 || posts[0].Content != "hello" || len(posts[0].Embeds) != 0 {
			t.Errorf("posts = %+v, want one with just the text", posts)
		}
	})
	t.Run("nothing left to sticky", func(t *testing.T) {
		f := &fakeDiscord{}
		b, _ := newFakeBot(t, f)
		s := &stickyMessage{ChannelID: 500, GuildID: testGuildID, Active: true, MaxIdleMins: 10, MsgThreshold: 1}

		reply := b.createSticky(s, discord.Message{Attachments: []discord.Attachment{unsaved}})

		if strings.HasPrefix(reply, "Sticky created") {
			t.Errorf("reply = %q, want a failure", reply)
		}
		if posts := f.postsMade(); len(posts) != 0 {
			t.Errorf("posted %d empty stickies", len(posts))
		}
	})
}
