package bot

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

func TestWithoutID(t *testing.T) {
	list := []snowflake.ID{1, 2, 3}
	reader := list // a copy of the slice taken under the lock, read after it
	got, ok := withoutID(list, 1)
	if !ok || !slices.Equal(got, []snowflake.ID{2, 3}) {
		t.Errorf("withoutID = %v, %v; want [2 3], true", got, ok)
	}
	if !slices.Equal(reader, []snowflake.ID{1, 2, 3}) {
		t.Errorf("removal rewrote a reader's copy: %v", reader)
	}
	if got, ok := withoutID(list, 9); ok || !slices.Equal(got, list) {
		t.Errorf("missing ID: withoutID = %v, %v", got, ok)
	}
}

// checkPosterRole and the starboard read a copy of the settings without the
// lock, so removing a channel must not shift the shared array under them.
func TestBlacklistRemoveLeavesReadersCopyIntact(t *testing.T) {
	b, _, _ := newRoleTestBot(t)
	st := &posterRoleState{Settings: posterRoleSettings{SkipChannels: []snowflake.ID{100, 200, 300}}}
	st.mu.Lock()
	cfg := st.Settings
	st.mu.Unlock()

	opts := `[{"name":"market","type":2,"options":[{"name":"filter-remove","type":1,"options":[{"name":"channel","type":7,"value":"100"}]}]}]`
	resolved := discord.ResolvedData{Channels: map[snowflake.ID]discord.ResolvedChannel{100: {ID: 100, Name: "sales"}}}
	e, responses := roleCommand(t, roleTestOwner, nil, opts, resolved)
	b.handleBlacklistRemove(e, roleTestGuild, st, func() error { return nil }, "Market")
	response(t, responses)

	if !slices.Equal(st.Settings.SkipChannels, []snowflake.ID{200, 300}) {
		t.Errorf("SkipChannels = %v, want [200 300]", st.Settings.SkipChannels)
	}
	if !slices.Equal(cfg.SkipChannels, []snowflake.ID{100, 200, 300}) {
		t.Errorf("reader's copy changed to %v", cfg.SkipChannels)
	}
}

// The starboard's filter-add and filter-remove, through the shared handlers.
func TestStarboardBlacklistAddRemove(t *testing.T) {
	b, _, _ := newRoleTestBot(t)
	st := &starboardState{}
	filter := func(handle func(*events.ApplicationCommandInteractionCreate, snowflake.ID, blacklistable, func() error, string), sub string, channelID snowflake.ID) string {
		t.Helper()
		opts := fmt.Sprintf(`[{"name":"starboard","type":2,"options":[{"name":%q,"type":1,"options":[{"name":"channel","type":7,"value":"%d"}]}]}]`, sub, channelID)
		resolved := discord.ResolvedData{Channels: map[snowflake.ID]discord.ResolvedChannel{channelID: {ID: channelID, Name: "art"}}}
		e, responses := roleCommand(t, roleTestOwner, nil, opts, resolved)
		handle(e, roleTestGuild, st, func() error { return nil }, "Starboard")
		return response(t, responses).Content
	}

	filter(b.handleBlacklistAdd, "filter-add", 100)
	filter(b.handleBlacklistAdd, "filter-add", 200)
	if !st.Settings.isBlacklisted(100) || !st.Settings.isBlacklisted(200) {
		t.Fatalf("Blacklist = %v, want [100 200]", st.Settings.Blacklist)
	}

	reader := st.Settings.Blacklist
	if got := filter(b.handleBlacklistRemove, "filter-remove", 100); !strings.Contains(got, "removed") {
		t.Errorf("response = %q", got)
	}
	if st.Settings.isBlacklisted(100) || !st.Settings.isBlacklisted(200) {
		t.Errorf("Blacklist = %v, want [200]", st.Settings.Blacklist)
	}
	if !slices.Equal(reader, []snowflake.ID{100, 200}) {
		t.Errorf("reader's copy changed to %v", reader)
	}
	if got := filter(b.handleBlacklistRemove, "filter-remove", 100); !strings.Contains(got, "not blacklisted") {
		t.Errorf("removing it again: response = %q", got)
	}
}

func TestContainsAny(t *testing.T) {
	tests := []struct {
		name string
		list []snowflake.ID
		ids  []snowflake.ID
		want bool
	}{
		{"empty list", nil, []snowflake.ID{1}, false},
		{"empty ids", []snowflake.ID{1, 2, 3}, nil, false},
		{"match", []snowflake.ID{1, 2, 3}, []snowflake.ID{2}, true},
		{"no match", []snowflake.ID{1, 2, 3}, []snowflake.ID{4, 5}, false},
		{"variadic partial match", []snowflake.ID{1, 2, 3}, []snowflake.ID{4, 3}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsAny(tt.list, tt.ids...)
			if got != tt.want {
				t.Errorf("containsAny(%v, %v) = %v, want %v", tt.list, tt.ids, got, tt.want)
			}
		})
	}
}
