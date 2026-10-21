package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestDefaultModLogConfig(t *testing.T) {
	config := defaultModLogConfig()
	if config == nil {
		t.Fatal("config is nil")
	}
	if len(config) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(config))
	}
	if config[1013566342345019512] != 1142519200682876938 {
		t.Errorf("dev ChannelID = %d, want %d", config[1013566342345019512], 1142519200682876938)
	}
	if config[726985544038612993] != 1141477354129080361 {
		t.Errorf("prod ChannelID = %d, want %d", config[726985544038612993], 1141477354129080361)
	}
}

func TestMessageLink(t *testing.T) {
	got := messageLink(111, 222, 333)
	want := "https://discord.com/channels/111/222/333"
	if got != want {
		t.Errorf("messageLink = %q, want %q", got, want)
	}
}

func TestParseThreadUserID(t *testing.T) {
	tests := []struct {
		name string
		want snowflake.ID
	}{
		{"cooluser - 123456789012345678", 123456789012345678},
		{"user with spaces - 999", 999},
		{"someone - 0", 0},
		{"no dash at all", 0},
		{"trailing text - abc", 0},
		{"", 0},
		{"- 12345", 12345},
		{"name - 111 - 222", 222},
		{"overflow - 99999999999999999999999", 0},
		{"renamed by a mod - 123 (banned)", 123},
		{"renamed by a mod - 123 [alt]", 123},
		{"renamed by a mod - 123 ✅", 123},
		{"digits run on - 123abc", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseThreadUserID(tc.name)
			if got != tc.want {
				t.Errorf("parseThreadUserID(%q) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// testThread builds a thread in forum 600; GuildThread's fields are only
// settable through JSON.
func testThread(t *testing.T, id snowflake.ID, name string) discord.GuildThread {
	t.Helper()
	var thread discord.GuildThread
	data := fmt.Sprintf(`{"id":"%d","type":11,"guild_id":"1","parent_id":"600","name":%q}`, id, name)
	if err := json.Unmarshal([]byte(data), &thread); err != nil {
		t.Fatal(err)
	}
	return thread
}

func TestModLogForumFindMatchesUserIDExactly(t *testing.T) {
	f := newModLogForum([]discord.GuildThread{
		testThread(t, 1, "alice - 123"),
		testThread(t, 2, "bob - 9123"),
		testThread(t, 3, "carol - 1234"),
		testThread(t, 4, "dave"),          // legacy: no user ID in the name
		testThread(t, 5, "erin (warned)"), // legacy
		testThread(t, 6, "frank - 77"),
		testThread(t, 7, "frank - 77"), // duplicate: the first one wins
		testThread(t, 8, "gina - 88 (banned)"),
	})

	tests := []struct {
		name   string
		user   discord.User
		want   snowflake.ID
		wantOK bool
	}{
		{"by ID", discord.User{ID: 123, Username: "alice"}, 1, true},
		{"renamed user still matches by ID", discord.User{ID: 123, Username: "alice2"}, 1, true},
		{"username prefix of another thread", discord.User{ID: 555, Username: "al"}, 0, false},
		{"numeric username inside another user's ID", discord.User{ID: 556, Username: "12"}, 0, false},
		{"ID that is a prefix of another ID", discord.User{ID: 12, Username: "zed"}, 0, false},
		{"legacy thread by exact username", discord.User{ID: 900, Username: "dave"}, 4, true},
		{"legacy thread needs an exact name", discord.User{ID: 901, Username: "erin"}, 0, false},
		{"thread with an ID never matches by name", discord.User{ID: 902, Username: "alice - 123"}, 0, false},
		{"first of duplicate threads", discord.User{ID: 77, Username: "frank"}, 6, true},
		{"text after the ID", discord.User{ID: 88, Username: "gina"}, 8, true},
		{"ID that is a prefix of an annotated ID", discord.User{ID: 8, Username: "gin"}, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := f.find(tc.user)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("find(%d %q) = %d, %v; want %d, %v", tc.user.ID, tc.user.Username, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestModLogForumRemove(t *testing.T) {
	f := newModLogForum([]discord.GuildThread{testThread(t, 1, "alice - 123"), testThread(t, 2, "dave")})
	if !f.remove(1) || !f.remove(2) {
		t.Fatal("indexed threads should be removed")
	}
	if f.remove(3) {
		t.Error("unknown thread reported as removed")
	}
	if _, ok := f.find(discord.User{ID: 123, Username: "dave"}); ok {
		t.Error("removed threads still found")
	}
}

func TestSplitText(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		size  int
		wantN int
	}{
		{"empty", "", embedSplitSize, 0},
		{"short", "hello", embedSplitSize, 1},
		{"exactly at limit", strings.Repeat("x", embedSplitSize), embedSplitSize, 1},
		{"one over", strings.Repeat("a", embedSplitSize+1), embedSplitSize, 2},
		// 3-byte runes: a byte-offset split would cut one in two.
		{"CJK", strings.Repeat("漢", embedSplitSize+10), embedSplitSize, 2},
		// Emoji count as two (UTF-16), so 1024 of them need two fields.
		{"emoji", strings.Repeat("😀", embedSplitSize), embedSplitSize, 2},
		{"mixed", strings.Repeat("a😀", 700), embedSplitSize, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chunks := splitText(tc.s, tc.size)
			if len(chunks) != tc.wantN {
				t.Fatalf("got %d chunks, want %d", len(chunks), tc.wantN)
			}
			if strings.Join(chunks, "") != tc.s {
				t.Error("chunks don't reassemble the input")
			}
			for i, c := range chunks {
				if !utf8.ValidString(c) {
					t.Errorf("chunk %d cuts a character in two", i)
				}
				if textLen(c) > tc.size {
					t.Errorf("chunk %d is %d long, over %d", i, textLen(c), tc.size)
				}
			}
		})
	}
}

func TestTruncateText(t *testing.T) {
	if got := truncateText("short", 10); got != "short" {
		t.Errorf("text under the limit changed: %q", got)
	}
	got := truncateText(strings.Repeat("😀", 100), 50)
	if !strings.HasSuffix(got, truncatedMarker) || !utf8.ValidString(got) {
		t.Errorf("truncated text = %q", got)
	}
	if textLen(got) > 50 {
		t.Errorf("truncated to %d, over 50", textLen(got))
	}
	if got := truncateText("too long for this", 3); got != "" {
		t.Errorf("no room for the marker should give \"\", got %q", got)
	}
}

func TestBuildModLogEmbedFitsDiscordLimits(t *testing.T) {
	link := "https://bot.espressoaf.com/files/mod/726985544038612993/abcdefghijABCDEFGHIJ0123456789.png"
	var attachments []string
	for range 10 {
		attachments = append(attachments, link)
	}
	edited := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entry := modLogEntry{
		msg: discord.Message{
			// Nitro allows 4000 characters; emoji count double.
			Content:         strings.Repeat("😀", 4000),
			Author:          discord.User{ID: 1, Username: strings.Repeat("u", 32)},
			CreatedAt:       edited.Add(-time.Hour),
			EditedTimestamp: &edited,
		},
		channelName: strings.Repeat("c", 100),
		requester:   discord.User{ID: 2, Username: strings.Repeat("m", 32)},
		attachments: attachments,
		link:        messageLink(1, 2, 3),
		note:        strings.Repeat("n", 1024),
		archivedAt:  edited.Add(time.Hour),
	}
	embed := buildModLogEmbed(entry)

	if n := embedTextLen(embed); n > embedTotalLimit {
		t.Errorf("embed text is %d, over %d", n, embedTotalLimit)
	}
	if len(embed.Fields) > 25 {
		t.Errorf("%d fields, over 25", len(embed.Fields))
	}
	var content strings.Builder
	for i, f := range embed.Fields {
		if textLen(f.Value) > embedSplitSize || !utf8.ValidString(f.Value) {
			t.Errorf("field %d (%q) is %d long or not valid UTF-8", i, f.Name, textLen(f.Value))
		}
		if f.Name == "" {
			content.WriteString(f.Value)
		}
	}
	if !strings.HasSuffix(content.String(), truncatedMarker) {
		t.Error("cut content should end with the truncation marker")
	}
	last := embed.Fields[len(embed.Fields)-1]
	if last.Name != "Mod Note" || last.Value != entry.note {
		t.Errorf("mod note not kept whole: last field %q", last.Name)
	}

	// Content that fits is kept whole, ahead of the other fields.
	entry.msg.Content = "hello 漢字"
	embed = buildModLogEmbed(entry)
	if embed.Fields[0].Value != "hello 漢字" || embed.Fields[1].Name != "Attachments" {
		t.Errorf("short content: fields start %q, %q", embed.Fields[0].Value, embed.Fields[1].Name)
	}
	if embed.Title != "Message from @"+entry.msg.Author.Username+" to #"+entry.channelName {
		t.Errorf("title = %q", embed.Title)
	}
}

func TestModLogSavedNotice(t *testing.T) {
	n := modLogSavedNotice("alice", "mod-log", "alice - 123", "https://x", 0, 2)
	if n.Title != "Saved message from alice in #mod-log/alice - 123" {
		t.Errorf("title = %q", n.Title)
	}
	if n.Description != "" {
		t.Errorf("no failures should mean no description, got %q", n.Description)
	}
	if n := modLogSavedNotice("alice", "f", "t", "u", 1, 3); !strings.Contains(n.Description, "1 of 3") {
		t.Errorf("description = %q", n.Description)
	}
}

func TestModLogAuditReport(t *testing.T) {
	threads := []discord.GuildThread{
		testThread(t, 1, "alice - 123"),
		testThread(t, 2, "alice2 - 123"),
		testThread(t, 3, "bob - 456"),
	}
	clean := modLogAuditReport(threads[2:], false)
	if !strings.Contains(clean, "No issues found") {
		t.Errorf("clean report:\n%s", clean)
	}

	dup := modLogAuditReport(threads, false)
	if !strings.Contains(dup, "**User ID 123** — 2 threads") || strings.Contains(dup, "No issues found") {
		t.Errorf("duplicate report:\n%s", dup)
	}

	partial := modLogAuditReport(threads[2:], true)
	if strings.Contains(partial, "No issues found") || !strings.Contains(partial, "may be incomplete") {
		t.Errorf("a failed listing must not report a clean bill of health:\n%s", partial)
	}

	var many []discord.GuildThread
	for i := range 200 {
		many = append(many, testThread(t, snowflake.ID(1000+i), fmt.Sprintf("legacy name number %d", i)))
	}
	if long := modLogAuditReport(many, false); textLen(long) > messageContentLimit {
		t.Errorf("report is %d long, over the message limit", textLen(long))
	}
}

func TestSaveModFilesBoundedAndOrdered(t *testing.T) {
	var atts []discord.Attachment
	for i := range 7 {
		atts = append(atts, discord.Attachment{Filename: fmt.Sprintf("f%d", i)})
	}
	var mu sync.Mutex
	running, maxRunning := 0, 0
	var calls atomic.Int32
	got := saveModFiles(context.Background(), atts, 2, func(_ context.Context, att discord.Attachment) savedModFile {
		calls.Add(1)
		mu.Lock()
		running++
		maxRunning = max(maxRunning, running)
		mu.Unlock()
		// Earlier files take longer, so they finish out of order.
		var i int
		fmt.Sscanf(att.Filename, "f%d", &i)
		time.Sleep(time.Duration(7-i) * 3 * time.Millisecond)
		mu.Lock()
		running--
		mu.Unlock()
		return savedModFile{line: att.Filename, key: "k" + att.Filename}
	})

	if calls.Load() != 7 {
		t.Fatalf("save called %d times, want 7", calls.Load())
	}
	if maxRunning != 2 {
		t.Errorf("%d saves ran at once, want 2", maxRunning)
	}
	for i, s := range got {
		if want := fmt.Sprintf("f%d", i); s.line != want || s.key != "k"+want {
			t.Errorf("result %d = %+v, want %s", i, s, want)
		}
	}
}
