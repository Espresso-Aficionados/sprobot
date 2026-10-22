package stickybot

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

func TestFileKeys(t *testing.T) {
	const bucketURL = "https://s3.example/bucket/"
	key := testFileKey("a")
	legacyKey := testFileKey("b")
	s := &stickyMessage{GuildID: testGuildID, FileURLs: []string{
		key,
		bucketURL + url.PathEscape(legacyKey), // stored before files were stored as keys
		"sticky_files/726985544038612993/cccccccccccccccccccccccccccccc.png", // another guild's
		"mod_files/1013566342345019512/dddddddddddddddddddddddddddddd.png",   // not a sticky file
		"https://cdn.example/sticky_files/1013566342345019512/eeeeeeeeeeeeeeeeeeeeeeeeeeeeee.png",
	}}
	got := s.fileKeys(bucketURL)
	if want := []string{key, legacyKey}; !slices.Equal(got, want) {
		t.Errorf("fileKeys = %q, want %q", got, want)
	}
}

// seedFiles stores an object under each key and returns a func reporting
// which of them are still stored.
func seedFiles(fake *testutil.FakeS3, keys ...string) func() []string {
	fake.Mu.Lock()
	for _, k := range keys {
		fake.Objects["/test-bucket/"+k] = []byte("file")
	}
	fake.Mu.Unlock()
	return func() []string {
		fake.Mu.Lock()
		defer fake.Mu.Unlock()
		var left []string
		for _, k := range keys {
			if _, ok := fake.Objects["/test-bucket/"+k]; ok {
				left = append(left, k)
			}
		}
		return left
	}
}

// PRIVACY.md promises a sticky's saved files go when the sticky does.
func TestRetireStickyDeletesMessageAndFiles(t *testing.T) {
	f := &fakeDiscord{}
	b, fakeS3 := newFakeBot(t, f)
	key, legacyKey, otherKey := testFileKey("a"), testFileKey("b"), testFileKey("c")
	stored := seedFiles(fakeS3, key, legacyKey, otherKey)
	s := &stickyMessage{
		ChannelID: 500, GuildID: testGuildID, LastMessageID: 100,
		FileURLs: []string{key, b.S3.Origin() + "/test-bucket/" + url.PathEscape(legacyKey)},
	}

	b.retireSticky(s)

	if got := f.deletedIDs(); !slices.Equal(got, []snowflake.ID{100}) {
		t.Errorf("deleted messages %v, want [100]", got)
	}
	if left := stored(); !slices.Equal(left, []string{otherKey}) {
		t.Errorf("files left %q, want only the unrelated %q", left, otherKey)
	}
}

func TestInstallStickyRetiresReplacedSticky(t *testing.T) {
	f := &fakeDiscord{}
	b, fakeS3 := newFakeBot(t, f)
	oldKey, newKey := testFileKey("a"), testFileKey("b")
	stored := seedFiles(fakeS3, oldKey, newKey)
	old := &stickyMessage{ChannelID: 500, GuildID: testGuildID, Content: "old", LastMessageID: 100, FileURLs: []string{oldKey}}
	b.stickies[testGuildID] = map[snowflake.ID]*stickyMessage{500: old}

	s := &stickyMessage{ChannelID: 500, GuildID: testGuildID, Content: "new", Active: true, FileURLs: []string{newKey}, MaxIdleMins: 10, MsgThreshold: 1}
	if err := b.installSticky(s); err != nil {
		t.Fatal(err)
	}

	if got := b.stickies[testGuildID][500]; got != s {
		t.Errorf("channel's sticky = %+v, want the new one", got)
	}
	if got := f.deletedIDs(); !slices.Equal(got, []snowflake.ID{100}) {
		t.Errorf("deleted messages %v, want the replaced sticky's [100]", got)
	}
	if left := stored(); !slices.Equal(left, []string{newKey}) {
		t.Errorf("files left %q, want only the new sticky's %q", left, newKey)
	}
}

// Stickying a sticky's own post, the way to change its settings, replaces
// the sticky, which deletes its files. The new sticky shows its own copies.
func TestRestickyingOwnPostCopiesFiles(t *testing.T) {
	f := &fakeDiscord{}
	b, fakeS3 := newFakeBot(t, f)
	key := testFileKey("a")
	stored := seedFiles(fakeS3, key)
	old := &stickyMessage{
		ChannelID: 500, GuildID: testGuildID, Content: "old", LastMessageID: 100,
		Embeds: []discord.Embed{{Image: &discord.EmbedResource{URL: key}}}, FileURLs: []string{key},
	}
	b.stickies[testGuildID] = map[snowflake.ID]*stickyMessage{500: old}
	post := old.messageCreate(b.S3.FileURL)

	s := &stickyMessage{ChannelID: 500, GuildID: testGuildID, Active: true, MaxIdleMins: 10, MsgThreshold: 1}
	reply := b.createSticky(s, discord.Message{ID: 100, ChannelID: 500, Content: post.Content, Embeds: post.Embeds})

	if !strings.HasPrefix(reply, "Sticky created") || strings.Contains(reply, "Couldn't save") {
		t.Errorf("reply = %q, want a plain confirmation", reply)
	}
	if left := stored(); len(left) != 0 {
		t.Errorf("replaced sticky's files left %q, want none", left)
	}
	if len(s.FileURLs) != 1 || s.FileURLs[0] == key {
		t.Fatalf("new sticky's files = %q, want one copy of %q", s.FileURLs, key)
	}
	copied := s.FileURLs[0]
	fakeS3.Mu.Lock()
	data := fakeS3.Objects["/test-bucket/"+copied]
	fakeS3.Mu.Unlock()
	if string(data) != "file" {
		t.Errorf("copy %q holds %q, want the original's contents", copied, data)
	}
	if len(s.Embeds) != 1 || s.Embeds[0].Image == nil || s.Embeds[0].Image.URL != copied {
		t.Errorf("stored embeds = %+v, want one image at the copy's key %q", s.Embeds, copied)
	}
	if posts := f.postsMade(); len(posts) != 1 || len(posts[0].Embeds) != 1 || posts[0].Embeds[0].Image.URL != b.S3.FileURL(copied) {
		t.Errorf("posts = %+v, want one showing %s", posts, b.S3.FileURL(copied))
	}
}

// Once its sticky is gone, nothing else records a file, so a deletion that
// fails is retried.
func TestFailedFileDeletesAreRetried(t *testing.T) {
	f := &fakeDiscord{}
	b, fakeS3 := newFakeBot(t, f)
	key := testFileKey("a")
	stored := seedFiles(fakeS3, key)
	fakeS3.Mu.Lock()
	fakeS3.Fail["/test-bucket/"+key] = http.StatusForbidden
	fakeS3.Mu.Unlock()

	b.retireSticky(&stickyMessage{ChannelID: 500, GuildID: testGuildID, FileURLs: []string{key}})
	if left := stored(); len(left) != 1 {
		t.Fatalf("files left %q, want the one whose delete failed", left)
	}
	fakeS3.Mu.Lock()
	delete(fakeS3.Fail, "/test-bucket/"+key)
	fakeS3.Mu.Unlock()
	b.retryFailed()

	if left := stored(); len(left) != 0 {
		t.Errorf("files left %q after the retry, want none", left)
	}
	if len(b.undeleted) != 0 {
		t.Errorf("still to delete: %q", b.undeleted)
	}
}

// A sticky that fails to post is never stored, so nothing would ever delete
// the files saved for it.
func TestInstallStickyPostFailureDeletesFiles(t *testing.T) {
	f := &fakeDiscord{failPosts: true}
	b, fakeS3 := newFakeBot(t, f)
	key := testFileKey("a")
	stored := seedFiles(fakeS3, key)
	s := &stickyMessage{ChannelID: 500, GuildID: testGuildID, Content: "new", Active: true, FileURLs: []string{key}, MaxIdleMins: 10, MsgThreshold: 1}

	if err := b.installSticky(s); err == nil {
		t.Fatal("installSticky succeeded, want the post's error")
	}
	if left := stored(); len(left) != 0 {
		t.Errorf("files left %q, want none", left)
	}
	if b.stickies[testGuildID][500] != nil {
		t.Error("sticky installed despite the failed post")
	}
}

// Removing a sticky mid-repost must delete the message the repost posts, not
// the one it replaces.
func TestRetireStickyDeletesInFlightRepostMessage(t *testing.T) {
	b, _, api, _, release := startRepostInFlight(t)
	s := b.takeSticky(testGuildID, 500)
	if s == nil {
		t.Fatal("takeSticky found no sticky")
	}

	done := make(chan struct{})
	go func() {
		b.retireSticky(s)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("retireSticky returned while a repost was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retireSticky did not return after the repost finished")
	}

	// The repost itself deletes 100 before posting 777.
	if got := api.deletedIDs(); !slices.Equal(got, []snowflake.ID{100, 777}) {
		t.Errorf("deleted messages %v, want [100 777]", got)
	}
}
