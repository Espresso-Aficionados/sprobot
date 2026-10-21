package stickybot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
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

// commandEvent builds a slash command interaction from user 9 in channel 500
// of the test guild, carrying data. Its responses go to the returned channel.
func commandEvent(t *testing.T, data string) (*events.ApplicationCommandInteractionCreate, <-chan discord.MessageCreate) {
	t.Helper()
	raw := fmt.Sprintf(`{"id":"1","application_id":"2","type":2,"token":"tok","guild_id":"%d",
		"channel":{"id":"500","type":0,"guild_id":"%d","name":"coffee"},
		"user":{"id":"9","username":"mod"},"data":%s}`, testGuildID, testGuildID, data)
	var i discord.ApplicationCommandInteraction
	if err := json.Unmarshal([]byte(raw), &i); err != nil {
		t.Fatalf("decoding interaction: %v", err)
	}
	responses := make(chan discord.MessageCreate, 4)
	return &events.ApplicationCommandInteractionCreate{
		ApplicationCommandInteraction: i,
		Respond: func(_ discord.InteractionResponseType, d discord.InteractionResponseData, _ ...rest.RequestOpt) error {
			m, _ := d.(discord.MessageCreate)
			responses <- m
			return nil
		},
	}, responses
}

// /sticky remove answers at once and stores the removal before waiting out a
// repost in flight, since a shutdown doesn't wait for the command. Then it
// deletes the message that repost posts, and the sticky's files.
func TestStickyRemoveCommand(t *testing.T) {
	b, s, api, fakeS3, release := startRepostInFlight(t)
	key := testFileKey("a")
	stored := seedFiles(fakeS3, key)
	b.mu.Lock()
	s.FileURLs = []string{key}
	b.mu.Unlock()
	e, responses := commandEvent(t, `{"id":"3","name":"sticky","type":1,"options":[{"name":"remove","type":1}]}`)

	done := make(chan struct{})
	go func() {
		b.onCommand(e)
		close(done)
	}()
	select {
	case m := <-responses:
		if m.Content != "Sticky removed." {
			t.Errorf("response = %q, want %q", m.Content, "Sticky removed.")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no response while a repost was in flight")
	}
	waitFor(t, "the removal to be stored", func() bool {
		saved := storedStickies(t, fakeS3)
		return saved != nil && saved["500"] == nil
	})
	select {
	case <-done:
		t.Fatal("remove returned while a repost was in flight")
	default:
	}

	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("remove did not return after the repost finished")
	}
	// The repost itself deletes 100 before posting 777.
	if got := api.deletedIDs(); !slices.Equal(got, []snowflake.ID{100, 777}) {
		t.Errorf("deleted messages %v, want [100 777]", got)
	}
	if left := stored(); len(left) != 0 {
		t.Errorf("files left %q, want none", left)
	}
	if saved := storedStickies(t, fakeS3); saved["500"] != nil {
		t.Error("removed sticky stored again")
	}
}

// A saved attachment becomes one of the sticky's files, which its first post
// shows by web link.
func TestCreateStickySavesAttachments(t *testing.T) {
	t.Cleanup(s3client.AllowLocalFetchesForTesting())
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("png"))
	}))
	t.Cleanup(files.Close)
	f := &fakeDiscord{}
	b, fakeS3 := newFakeBot(t, f)
	s := &stickyMessage{ChannelID: 500, GuildID: testGuildID, Active: true, MaxIdleMins: 10, MsgThreshold: 1}

	reply := b.createSticky(s, discord.Message{Attachments: []discord.Attachment{{Filename: "photo.png", ProxyURL: files.URL + "/photo.png"}}})

	if !strings.HasPrefix(reply, "Sticky created") || strings.Contains(reply, "Couldn't save") {
		t.Errorf("reply = %q, want a plain confirmation", reply)
	}
	if len(s.FileURLs) != 1 {
		t.Fatalf("sticky's files = %q, want the saved attachment", s.FileURLs)
	}
	key := s.FileURLs[0]
	fakeS3.Mu.Lock()
	_, saved := fakeS3.Objects["/test-bucket/"+key]
	fakeS3.Mu.Unlock()
	if kind, guildID, _, ok := sprobot.ParseFileKey(key); !ok || kind != "sticky" || guildID != testGuildID.String() || !saved {
		t.Errorf("sticky's file %q isn't a saved sticky file of the test guild", key)
	}
	if posts := f.postsMade(); len(posts) != 1 || len(posts[0].Embeds) != 1 || posts[0].Embeds[0].Image.URL != b.S3.FileURL(key) {
		t.Errorf("posts = %+v, want one showing %s", posts, b.S3.FileURL(key))
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
