package stickybot

import (
	"errors"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
)

// Both a sticky's first post and its reposts show saved files by their web
// links, since Discord rejects an embed image that is a bare S3 key, and ping
// no one, since a stickied mention would otherwise ping on every repost.
func TestStickyPostsLinkFilesAndPingNoOne(t *testing.T) {
	posts := map[string]func(*Bot, *stickyMessage) error{
		"initial post": func(b *Bot, s *stickyMessage) error { return b.installSticky(s) },
		"repost": func(b *Bot, s *stickyMessage) error {
			if !b.repostSticky(s) {
				return errors.New("repost failed")
			}
			return nil
		},
	}
	for name, post := range posts {
		t.Run(name, func(t *testing.T) {
			f := &fakeDiscord{}
			b, _ := newFakeBot(t, f)
			key := testFileKey("a")
			s := &stickyMessage{
				ChannelID: 500, GuildID: testGuildID, Content: "hello <@123> @everyone", Active: true,
				Embeds:   []discord.Embed{{Title: "own embed"}, {Image: &discord.EmbedResource{URL: key}}},
				FileURLs: []string{key}, MaxIdleMins: 10, MsgThreshold: 1,
			}
			if err := post(b, s); err != nil {
				t.Fatal(err)
			}

			got := f.postsMade()
			if len(got) != 1 {
				t.Fatalf("got %d posts, want 1", len(got))
			}
			want := "https://bot.espressoaf.com/files/sticky/1013566342345019512/" + strings.Repeat("a", 30) + ".png"
			if e := got[0].Embeds; len(e) != 2 || e[0].Title != "own embed" || e[1].Image == nil || e[1].Image.URL != want {
				t.Errorf("posted embeds = %+v, want the own embed and the image at %s", e, want)
			}
			if am := got[0].AllowedMentions; am == nil || len(am.Parse) != 0 || len(am.Users) != 0 || len(am.Roles) != 0 || am.RepliedUser {
				t.Errorf("allowed mentions = %+v, want none", am)
			}
			if s.Embeds[1].Image.URL != key {
				t.Errorf("stored image = %q, want the key %q kept", s.Embeds[1].Image.URL, key)
			}
		})
	}
}
