package threadbot

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

// newThreadListBot returns a bot whose Discord and S3 are fakes.
func newThreadListBot(t *testing.T, api *fakeThreadAPI) *Bot {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	s3srv := httptest.NewServer(testutil.NewFakeS3())
	t.Cleanup(s3srv.Close)
	b := newTestBot(t, testutil.NewTestS3Client(t, s3srv))
	b.Client = &bot.Client{ApplicationID: 2, Rest: rest.New(rest.NewClient("test", rest.WithURL(srv.URL)))}
	return b
}

func listLines(desc string) []string {
	var lines []string
	for line := range strings.SplitSeq(desc, "\n") {
		if strings.HasPrefix(line, "- [") {
			lines = append(lines, line)
		}
	}
	return lines
}

// A channel list shows that channel's 10 busiest threads; channel 0 shows
// the server's 25 busiest, each with its parent channel.
func TestThreadsEmbedChannelAndServerLists(t *testing.T) {
	api := &fakeThreadAPI{members: map[string]int{}, fetches: map[string]int{}}
	for i := range 30 {
		id := fmt.Sprint(1000 + i)
		api.addThread(id, 5, 5)
		api.setField(id, "message_count", i)
		if i%2 == 1 {
			api.setField(id, "parent_id", "600")
		}
	}
	b := newThreadListBot(t, api)

	ch, err := b.buildThreadsEmbed(devGuild, 500)
	if err != nil || ch == nil {
		t.Fatalf("channel list = %v, %v", ch, err)
	}
	lines := listLines(ch.Description)
	if len(lines) != 10 {
		t.Errorf("channel list shows %d threads, want 10", len(lines))
	}
	if !strings.Contains(lines[0], "thread-1028") || strings.Contains(ch.Description, "thread-1029") {
		t.Errorf("channel list not the channel's busiest first:\n%s", ch.Description)
	}
	if strings.Contains(ch.Description, " in <#") {
		t.Errorf("channel list names parent channels:\n%s", ch.Description)
	}
	if !strings.Contains(ch.Description, "...and 5 more threads") || ch.Title != "Active Threads" || ch.Footer.Text != "15 active thread(s)" {
		t.Errorf("channel list summary wrong: title %q, footer %q\n%s", ch.Title, ch.Footer.Text, ch.Description)
	}

	all, err := b.buildThreadsEmbed(devGuild, 0)
	if err != nil || all == nil {
		t.Fatalf("server list = %v, %v", all, err)
	}
	lines = listLines(all.Description)
	if len(lines) != 25 {
		t.Errorf("server list shows %d threads, want 25", len(lines))
	}
	if !strings.Contains(lines[0], "thread-1029") || !strings.Contains(lines[0], " in <#600>") || !strings.Contains(lines[1], " in <#500>") {
		t.Errorf("server list not busiest first with parents:\n%s", all.Description)
	}
	if !strings.Contains(all.Description, "...and 5 more threads") || all.Title != "All Active Threads" || all.Footer.Text != "30 active thread(s) server-wide" {
		t.Errorf("server list summary wrong: title %q, footer %q\n%s", all.Title, all.Footer.Text, all.Description)
	}
}

const (
	threadsData  = `{"id":"3","name":"threads","type":1}`
	fetchAllData = `{"id":"3","name":"threadbot","type":1,"options":[{"name":"fetchall","type":1}]}`
)

// A failed thread fetch is reported as a failure, not as an empty channel.
func TestThreadListReportsFetchFailure(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		failActive bool
		want       string
	}{
		{"threads, fetch fails", threadsData, true, "Couldn't fetch the active threads"},
		{"fetchall, fetch fails", fetchAllData, true, "Couldn't fetch the active threads"},
		{"threads, none", threadsData, false, "No active threads in this channel."},
		{"fetchall, none", fetchAllData, false, "No active threads in this server."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeThreadAPI{members: map[string]int{}, fetches: map[string]int{}, failActive: tt.failActive}
			api.addThread("101", 5, 5)
			api.setField("101", "parent_id", "999") // never in channel 500
			if !tt.failActive {
				api.setField("101", "thread_metadata", map[string]any{"archived": true})
			}
			b := newThreadListBot(t, api)

			e, _ := commandEvent(t, devGuild, tt.data)
			b.onCommand(e)
			sent := api.sentFollowups()
			if len(sent) != 1 || !strings.Contains(sent[0].Content, tt.want) {
				t.Errorf("follow-ups = %+v, want one saying %q", sent, tt.want)
			}
		})
	}
}

// Threads cut by the description length limit are counted in "...and N
// more", not just those past the shown limit.
func TestThreadsEmbedCountsThreadsCutForLength(t *testing.T) {
	var threads []threadInfo
	for i := range 25 {
		threads = append(threads, threadInfo{
			Name: strings.Repeat("x", 100), ID: snowflake.ID(1000 + i), ParentID: 500,
			MessageCount: 10, MemberCount: 5, CreatedAt: time.Now(),
		})
	}
	e := threadsEmbed(devGuild, 0, threads, 40, time.Now())
	shown := len(listLines(e.Description))
	if shown == 0 || shown == 25 {
		t.Fatalf("shown %d threads, want the description limit to cut some", shown)
	}
	if want := fmt.Sprintf("...and %d more threads", 40-shown); !strings.Contains(e.Description, want) {
		t.Errorf("description missing %q:\n%s", want, e.Description)
	}
	if len(e.Description) > 4096 {
		t.Errorf("description is %d bytes, over Discord's 4096", len(e.Description))
	}
}
