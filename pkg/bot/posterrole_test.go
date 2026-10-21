package bot

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/sadbox/sprobot/pkg/testutil"
)

func TestPosterRoleSettingsJSONRoundTrip(t *testing.T) {
	s := posterRoleSettings{
		RoleID:       1234567890,
		Threshold:    50,
		SkipChannels: []snowflake.ID{111, 222, 333},
	}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	var loaded posterRoleSettings
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.RoleID != s.RoleID {
		t.Errorf("RoleID = %d, want %d", loaded.RoleID, s.RoleID)
	}
	if loaded.Threshold != s.Threshold {
		t.Errorf("Threshold = %d, want %d", loaded.Threshold, s.Threshold)
	}
	if len(loaded.SkipChannels) != len(s.SkipChannels) {
		t.Fatalf("SkipChannels len = %d, want %d", len(loaded.SkipChannels), len(s.SkipChannels))
	}
	for i, id := range loaded.SkipChannels {
		if id != s.SkipChannels[i] {
			t.Errorf("SkipChannels[%d] = %d, want %d", i, id, s.SkipChannels[i])
		}
	}
}

func TestIsSkippedSingle(t *testing.T) {
	s := posterRoleSettings{
		SkipChannels: []snowflake.ID{100, 200, 300},
	}

	if !s.isSkipped(200) {
		t.Error("expected 200 to be skipped")
	}
	if s.isSkipped(400) {
		t.Error("expected 400 to not be skipped")
	}
}

func TestIsSkippedVariadic(t *testing.T) {
	s := posterRoleSettings{
		SkipChannels: []snowflake.ID{100, 200},
	}

	if !s.isSkipped(999, 200) {
		t.Error("expected match when second arg is in skip list")
	}
	if s.isSkipped(999, 888) {
		t.Error("expected no match when neither arg is in skip list")
	}
}

func TestIsSkippedEmpty(t *testing.T) {
	s := posterRoleSettings{}

	if s.isSkipped(100) {
		t.Error("expected no match with nil skip list")
	}

	s.SkipChannels = []snowflake.ID{}
	if s.isSkipped(100) {
		t.Error("expected no match with empty skip list")
	}
}

func TestThresholdProgress(t *testing.T) {
	tests := []struct {
		name          string
		count         int
		threshold     int
		wantPct       int
		wantRemaining int
	}{
		{"zero progress", 0, 100, 0, 100},
		{"half way", 50, 100, 50, 50},
		{"at threshold", 100, 100, 100, 0},
		{"over threshold", 110, 100, 110, 0},
		{"low count", 30, 100, 30, 70},
		{"no threshold", 30, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pct, remaining := thresholdProgress(tt.count, tt.threshold)
			if pct != tt.wantPct {
				t.Errorf("pct = %d, want %d", pct, tt.wantPct)
			}
			if remaining != tt.wantRemaining {
				t.Errorf("remaining = %d, want %d", remaining, tt.wantRemaining)
			}
		})
	}
}

func TestPosterRoleStateJSONRoundTrip(t *testing.T) {
	st := &posterRoleState{
		Settings: posterRoleSettings{
			RoleID:       9999,
			Threshold:    100,
			SkipChannels: []snowflake.ID{111},
		},
		Counts:  map[string]int{"111": 55, "222": 10},
		Fetched: map[string]bool{"111": true},
	}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	var loaded posterRoleState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}

	if loaded.Settings.RoleID != 9999 {
		t.Errorf("Settings.RoleID = %d, want 9999", loaded.Settings.RoleID)
	}
	if loaded.Settings.Threshold != 100 {
		t.Errorf("Settings.Threshold = %d, want 100", loaded.Settings.Threshold)
	}
	if loaded.Counts["111"] != 55 {
		t.Errorf("Counts[111] = %d, want 55", loaded.Counts["111"])
	}
	if !loaded.Fetched["111"] {
		t.Error("Fetched[111] should be true")
	}
	if loaded.Fetched["222"] {
		t.Error("Fetched[222] should be false")
	}
}

func TestDiscordSearchResponseJSON(t *testing.T) {
	data := []byte(`{"total_results": 42, "extra_field": true}`)
	var resp discordSearchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TotalResults != 42 {
		t.Errorf("TotalResults = %d, want 42", resp.TotalResults)
	}
}

func TestDiscordSearchResponseWithMessages(t *testing.T) {
	data := []byte(`{
		"total_results": 3,
		"messages": [
			[{"channel_id": "100"}],
			[{"channel_id": "200"}],
			[{"channel_id": "100"}]
		]
	}`)
	var resp discordSearchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.TotalResults != 3 {
		t.Errorf("TotalResults = %d, want 3", resp.TotalResults)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("Messages len = %d, want 3", len(resp.Messages))
	}
	if resp.Messages[0][0].ChannelID != 100 {
		t.Errorf("Messages[0][0].ChannelID = %d, want 100", resp.Messages[0][0].ChannelID)
	}
	if resp.Messages[1][0].ChannelID != 200 {
		t.Errorf("Messages[1][0].ChannelID = %d, want 200", resp.Messages[1][0].ChannelID)
	}
}

func TestFilteredSearchCount(t *testing.T) {
	skipSet := func(ids ...snowflake.ID) func(snowflake.ID) bool {
		m := make(map[snowflake.ID]bool, len(ids))
		for _, id := range ids {
			m[id] = true
		}
		return func(id snowflake.ID) bool { return m[id] }
	}
	noSkip := func(snowflake.ID) bool { return false }

	t.Run("no blacklist returns TotalResults", func(t *testing.T) {
		resp := &discordSearchResponse{
			TotalResults: 100,
			Messages: [][]searchHitMessage{
				{{ChannelID: 1}},
				{{ChannelID: 2}},
				{{ChannelID: 3}},
			},
		}
		got := filteredSearchCount(resp, noSkip)
		if got != 100 {
			t.Errorf("got %d, want 100", got)
		}
	})

	t.Run("all blacklisted returns 0", func(t *testing.T) {
		resp := &discordSearchResponse{
			TotalResults: 50,
			Messages: [][]searchHitMessage{
				{{ChannelID: 10}},
				{{ChannelID: 20}},
			},
		}
		got := filteredSearchCount(resp, skipSet(10, 20))
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})

	t.Run("partial blacklist with extrapolation", func(t *testing.T) {
		resp := &discordSearchResponse{
			TotalResults: 100,
			Messages: [][]searchHitMessage{
				{{ChannelID: 1}},
				{{ChannelID: 2}},
				{{ChannelID: 3}},
				{{ChannelID: 4}},
			},
		}
		// 1 of 4 blacklisted → 3/4 eligible → 3*100/4 = 75
		got := filteredSearchCount(resp, skipSet(2))
		if got != 75 {
			t.Errorf("got %d, want 75", got)
		}
	})

	t.Run("partial blacklist no extrapolation", func(t *testing.T) {
		resp := &discordSearchResponse{
			TotalResults: 4,
			Messages: [][]searchHitMessage{
				{{ChannelID: 1}},
				{{ChannelID: 2}},
				{{ChannelID: 3}},
				{{ChannelID: 4}},
			},
		}
		// TotalResults == len(Messages), so no extrapolation — just count eligible
		got := filteredSearchCount(resp, skipSet(2))
		if got != 3 {
			t.Errorf("got %d, want 3", got)
		}
	})

	t.Run("empty messages returns 0", func(t *testing.T) {
		resp := &discordSearchResponse{TotalResults: 50}
		got := filteredSearchCount(resp, noSkip)
		if got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})
}

func TestFilterMessages(t *testing.T) {
	skip := func(id snowflake.ID) bool { return id == 20 }

	resp := &discordSearchResponse{
		Messages: [][]searchHitMessage{
			{{ChannelID: 10}},
			{{ChannelID: 20}},
			{{ChannelID: 30}},
		},
	}
	got := filterMessages(resp, skip)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0][0].ChannelID != 10 {
		t.Errorf("got[0] channel = %d, want 10", got[0][0].ChannelID)
	}
	if got[1][0].ChannelID != 30 {
		t.Errorf("got[1] channel = %d, want 30", got[1][0].ChannelID)
	}
}

func TestTopChannels(t *testing.T) {
	t.Run("multiple channels", func(t *testing.T) {
		resp := &discordSearchResponse{
			Messages: [][]searchHitMessage{
				{{ChannelID: 100}},
				{{ChannelID: 200}},
				{{ChannelID: 100}},
				{{ChannelID: 300}},
				{{ChannelID: 100}},
				{{ChannelID: 200}},
			},
		}
		top := topChannels(resp, 5)
		if len(top) != 3 {
			t.Fatalf("len = %d, want 3", len(top))
		}
		if top[0].ChannelID != 100 || top[0].Count != 3 {
			t.Errorf("top[0] = {%d, %d}, want {100, 3}", top[0].ChannelID, top[0].Count)
		}
		if top[1].ChannelID != 200 || top[1].Count != 2 {
			t.Errorf("top[1] = {%d, %d}, want {200, 2}", top[1].ChannelID, top[1].Count)
		}
		if top[2].ChannelID != 300 || top[2].Count != 1 {
			t.Errorf("top[2] = {%d, %d}, want {300, 1}", top[2].ChannelID, top[2].Count)
		}
	})

	t.Run("capped at n", func(t *testing.T) {
		resp := &discordSearchResponse{
			Messages: [][]searchHitMessage{
				{{ChannelID: 1}},
				{{ChannelID: 2}},
				{{ChannelID: 3}},
				{{ChannelID: 4}},
			},
		}
		top := topChannels(resp, 2)
		if len(top) != 2 {
			t.Fatalf("len = %d, want 2", len(top))
		}
	})

	t.Run("empty", func(t *testing.T) {
		resp := &discordSearchResponse{}
		top := topChannels(resp, 5)
		if len(top) != 0 {
			t.Errorf("len = %d, want 0", len(top))
		}
	})

	t.Run("tie breaking by channel ID", func(t *testing.T) {
		resp := &discordSearchResponse{
			Messages: [][]searchHitMessage{
				{{ChannelID: 300}},
				{{ChannelID: 100}},
			},
		}
		top := topChannels(resp, 5)
		if len(top) != 2 {
			t.Fatalf("len = %d, want 2", len(top))
		}
		// Same count (1 each), should sort by channel ID ascending
		if top[0].ChannelID != 100 {
			t.Errorf("top[0].ChannelID = %d, want 100", top[0].ChannelID)
		}
		if top[1].ChannelID != 300 {
			t.Errorf("top[1].ChannelID = %d, want 300", top[1].ChannelID)
		}
	})
}

const posterTestRole snowflake.ID = 300

// newPosterTestBot returns a test bot whose poster role needs threshold posts.
// The role is a plain one in the role cache.
func newPosterTestBot(t *testing.T, threshold int) (*Bot, *roleAPI, *testutil.FakeS3, *posterRoleState) {
	t.Helper()
	b, api, fakeS3 := newRoleTestBot(t)
	addTestRole(b, discord.Role{ID: posterTestRole, Position: 5, Permissions: discord.PermissionSendMessages})
	st := &posterRoleState{
		Settings: posterRoleSettings{RoleID: posterTestRole, Threshold: threshold},
		Counts:   make(map[string]int),
		Fetched:  make(map[string]bool),
	}
	// Messages are posted in channel 500 and searches find them in channel 5.
	// Both are cached with no parent, so resolving their parents makes no
	// REST call.
	for _, id := range []snowflake.ID{500, 5} {
		b.Client.Caches.AddChannel(testChannel(t, fmt.Sprintf(`{"id":"%d","type":0,"guild_id":"%d","name":"chat"}`, id, roleTestGuild)))
	}
	b.posterRole.set(roleTestGuild, st)
	return b, api, fakeS3, st
}

func searchCall(call string) bool { return strings.HasSuffix(call, "/messages/search") }

// searchHits answers a search with total results, all in channel 5.
func searchHits(total int) (int, string) {
	return http.StatusOK, fmt.Sprintf(`{"total_results":%d,"messages":[[{"channel_id":"5"}]]}`, total)
}

func posterCount(st *posterRoleState, userID snowflake.ID) (int, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	n, ok := st.Counts[userID.String()]
	return n, ok
}

func TestCountMessageClaimsOneSearchPerUser(t *testing.T) {
	st := &posterRoleState{Counts: map[string]int{}, Fetched: map[string]bool{}}
	now := time.Now()
	s := st.countMessage(20, now)
	if s == nil {
		t.Fatal("first message from an untracked user didn't start a search")
	}
	if st.countMessage(20, now) != nil || st.countMessage(20, now) != nil {
		t.Error("a second search started while one was in flight")
	}
	if st.countMessage(21, now) == nil {
		t.Error("another user's search was held off")
	}

	// The two messages counted during the search are kept on top of it.
	if !st.finishSearch(20, s, 10) {
		t.Fatal("search result dropped")
	}
	if got := st.Counts["20"]; got != 12 {
		t.Errorf("count = %d, want 10 searched + 2 counted meanwhile", got)
	}
	if st.countMessage(20, now) != nil {
		t.Error("searched again after the history was fetched")
	}
	if got := st.Counts["20"]; got != 13 {
		t.Errorf("count = %d, want 13", got)
	}
}

func TestFinishSearchDropsResultAfterClearOrBan(t *testing.T) {
	now := time.Now()

	st := &posterRoleState{Counts: map[string]int{}, Fetched: map[string]bool{}}
	old := st.countMessage(20, now)
	st.clearTracking(20)
	// A message after the clear starts a fresh search.
	fresh := st.countMessage(20, now)
	if fresh == nil {
		t.Fatal("no search after tracking was cleared")
	}
	if st.finishSearch(20, old, 500) {
		t.Error("a search from before the clear was recorded")
	}
	if st.Counts["20"] != 1 || st.Fetched["20"] {
		t.Errorf("stale search changed tracking: count %d, fetched %v", st.Counts["20"], st.Fetched["20"])
	}
	if !st.finishSearch(20, fresh, 7) || st.Counts["20"] != 7 {
		t.Errorf("fresh search not recorded: count %d", st.Counts["20"])
	}

	st = &posterRoleState{Counts: map[string]int{}, Fetched: map[string]bool{}}
	s := st.countMessage(30, now)
	st.Settings.BlacklistedUsers = []snowflake.ID{30}
	if st.finishSearch(30, s, 500) || st.Fetched["30"] {
		t.Error("search recorded for a user banned while it ran")
	}
}

func TestFailSearchHoldsOffTheNextOne(t *testing.T) {
	st := &posterRoleState{Counts: map[string]int{}, Fetched: map[string]bool{}}
	now := time.Now()
	st.failSearch(20, st.countMessage(20, now), now)
	if st.countMessage(20, now.Add(time.Minute)) != nil {
		t.Error("searched again right after a failure")
	}
	if st.countMessage(20, now.Add(posterSearchRetry)) == nil {
		t.Error("no search once the retry wait ran out")
	}
}

func TestGrantDue(t *testing.T) {
	now := time.Now()
	st := &posterRoleState{
		Settings: posterRoleSettings{RoleID: posterTestRole, Threshold: 10},
		Counts:   map[string]int{"20": 10, "21": 9, "22": 50, "23": 50},
		Fetched:  map[string]bool{"20": true, "21": true, "23": true},
	}
	st.Settings.BlacklistedUsers = []snowflake.ID{23}
	for _, tt := range []struct {
		user snowflake.ID
		want bool
	}{
		{20, true},  // at the threshold
		{21, false}, // below it
		{22, false}, // history not fetched yet
		{23, false}, // banned
	} {
		if _, _, due := st.grantDue(tt.user, now); due != tt.want {
			t.Errorf("user %d: due = %v, want %v", tt.user, due, tt.want)
		}
	}
	st.grantRetry.fail(20, now, roleGrantRetry)
	if _, _, due := st.grantDue(20, now); due {
		t.Error("due while a failed grant is holding off")
	}
}

// Two quick messages from an untracked user used to run two searches and add
// both results.
func TestCheckPosterRoleRunsOneSearchPerUser(t *testing.T) {
	b, api, _, st := newPosterTestBot(t, 100)
	release := make(chan struct{})
	var searches atomic.Int32
	api.setRespond(func(call string) (int, string) {
		if searchCall(call) {
			if searches.Add(1) == 1 {
				<-release
			}
			return searchHits(10)
		}
		return http.StatusNoContent, ""
	})

	done := make(chan struct{})
	go func() {
		b.checkPosterRole(roleTestGuild, 500, messageFrom(20))
		close(done)
	}()
	waitFor(t, "the first search", func() bool { return searches.Load() == 1 })
	b.checkPosterRole(roleTestGuild, 500, messageFrom(20))
	b.checkPosterRole(roleTestGuild, 500, messageFrom(20))
	close(release)
	<-done

	if n := searches.Load(); n != 1 {
		t.Errorf("%d searches, want 1", n)
	}
	if n, _ := posterCount(st, 20); n != 12 {
		t.Errorf("count = %d, want 10 searched + 2 counted meanwhile", n)
	}
}

// marketBan is /config market ban <user>.
func marketBan(userID snowflake.ID) string {
	return fmt.Sprintf(`[{"name":"market","type":2,"options":[{"name":"ban","type":1,"options":[{"name":"user","type":6,"value":"%d"},{"name":"reason","type":3,"value":"scam"}]}]}]`, userID)
}

func banEvent(t *testing.T, userID snowflake.ID) (*events.ApplicationCommandInteractionCreate, <-chan discord.MessageCreate) {
	resolved := discord.ResolvedData{Users: map[snowflake.ID]discord.User{userID: {ID: userID, Username: "seller"}}}
	return roleCommand(t, roleTestOwner, nil, marketBan(userID), resolved)
}

// A market ban that lands while the history search runs must keep the user
// from getting the role when the search comes back over the threshold.
func TestCheckPosterRoleDropsSearchAfterBan(t *testing.T) {
	b, api, _, st := newPosterTestBot(t, 100)
	release := make(chan struct{})
	var searches atomic.Int32
	api.setRespond(func(call string) (int, string) {
		if searchCall(call) {
			searches.Add(1)
			<-release
			return searchHits(500)
		}
		return http.StatusNoContent, ""
	})

	done := make(chan struct{})
	go func() {
		b.checkPosterRole(roleTestGuild, 500, messageFrom(20))
		close(done)
	}()
	waitFor(t, "the search", func() bool { return searches.Load() == 1 })
	e, responses := banEvent(t, 20)
	b.handleMarketConfig(e)
	response(t, responses)
	close(release)
	<-done

	if n := api.count(roleCall("PUT", 20, posterTestRole)); n != 0 {
		t.Errorf("poster role granted %d times to a user banned during the search", n)
	}
	if n, ok := posterCount(st, 20); ok {
		t.Errorf("tracking rewritten after the ban: count %d", n)
	}
}

// A ban that lands while AddMemberRole is in flight gets the role revoked.
func TestPosterRoleRevokedWhenBanLandsDuringGrant(t *testing.T) {
	b, api, _, st := newPosterTestBot(t, 100)
	st.Counts["20"] = 150
	st.Fetched["20"] = true
	api.setRespond(func(call string) (int, string) {
		if call == roleCall("PUT", 20, posterTestRole) {
			st.mu.Lock()
			st.Settings.BlacklistedUsers = append(st.Settings.BlacklistedUsers, 20)
			st.mu.Unlock()
		}
		return http.StatusNoContent, ""
	})

	b.checkPosterRole(roleTestGuild, 500, messageFrom(20))

	if n := api.count(roleCall("PUT", 20, posterTestRole)); n != 1 {
		t.Fatalf("%d grants, want 1", n)
	}
	if n := api.count(roleCall("DELETE", 20, posterTestRole)); n != 1 {
		t.Errorf("%d revokes after a ban landed during the grant, want 1", n)
	}
}

func TestPosterRoleGrantBacksOffAfterFailure(t *testing.T) {
	b, api, _, st := newPosterTestBot(t, 100)
	st.Counts["20"] = 150
	st.Fetched["20"] = true
	api.setRespond(func(string) (int, string) {
		return restError(http.StatusForbidden, 50013, "Missing Permissions")
	})

	b.checkPosterRole(roleTestGuild, 500, messageFrom(20))
	b.checkPosterRole(roleTestGuild, 500, messageFrom(20))
	if n := api.count(roleCall("PUT", 20, posterTestRole)); n != 1 {
		t.Errorf("%d grant attempts, want 1", n)
	}
}

func TestPosterRoleSearchBacksOffWhileIndexing(t *testing.T) {
	b, api, _, st := newPosterTestBot(t, 100)
	api.setRespond(func(call string) (int, string) {
		if searchCall(call) {
			return http.StatusAccepted, `{"retry_after":5}`
		}
		return http.StatusNoContent, ""
	})

	b.checkPosterRole(roleTestGuild, 500, messageFrom(20))
	b.checkPosterRole(roleTestGuild, 500, messageFrom(20))
	if n := api.countSuffix("/messages/search"); n != 1 {
		t.Errorf("%d searches while the index builds, want 1", n)
	}
	if n, _ := posterCount(st, 20); n != 2 {
		t.Errorf("count = %d, want both messages counted", n)
	}
}

// The poster role is checked again at grant time, in case it was edited into
// a moderator role after it was configured.
func TestPosterRoleRefusesUngrantableRole(t *testing.T) {
	b, api, _, st := newPosterTestBot(t, 100)
	addTestRole(b, discord.Role{ID: posterTestRole, Position: 5, Permissions: discord.PermissionManageRoles})
	st.Counts["20"] = 150
	st.Fetched["20"] = true

	b.checkPosterRole(roleTestGuild, 500, messageFrom(20))
	if n := api.count(roleCall("PUT", 20, posterTestRole)); n != 0 {
		t.Errorf("moderator role granted %d times", n)
	}
}

// A failed save used to leave the ban in memory, so the retry answered
// "already banned" without removing the role or posting to the mod log.
func TestMarketBanRollsBackWhenSaveFails(t *testing.T) {
	b, api, fakeS3, st := newPosterTestBot(t, 100)
	key := fmt.Sprintf("/test-bucket/posterroles/%d.json", roleTestGuild)
	fakeS3.Mu.Lock()
	fakeS3.Fail[key] = http.StatusForbidden
	fakeS3.Mu.Unlock()

	e, responses := banEvent(t, 20)
	b.handleMarketConfig(e)
	if got := response(t, responses).Content; got != "Failed to save ban." {
		t.Errorf("response = %q", got)
	}
	if st.Settings.isUserBlacklisted(20) {
		t.Error("ban kept in memory after the save failed")
	}

	fakeS3.Mu.Lock()
	delete(fakeS3.Fail, key)
	fakeS3.Mu.Unlock()
	e, responses = banEvent(t, 20)
	b.handleMarketConfig(e)
	if got := response(t, responses).Content; !strings.Contains(got, "has been banned") {
		t.Errorf("retry response = %q", got)
	}
	if !st.Settings.isUserBlacklisted(20) {
		t.Error("retry didn't ban")
	}
	if n := api.count(roleCall("DELETE", 20, posterTestRole)); n != 1 {
		t.Errorf("%d role removals, want 1", n)
	}
}

func TestMarketBanOfBannedUserStillClearsTrackingAndRole(t *testing.T) {
	b, api, _, st := newPosterTestBot(t, 100)
	st.Settings.BlacklistedUsers = []snowflake.ID{20}
	st.Counts["20"] = 5

	e, responses := banEvent(t, 20)
	b.handleMarketConfig(e)
	if got := response(t, responses).Content; !strings.Contains(got, "already banned") {
		t.Errorf("response = %q", got)
	}
	if _, ok := posterCount(st, 20); ok {
		t.Error("tracking kept for an already banned user")
	}
	if n := api.count(roleCall("DELETE", 20, posterTestRole)); n != 1 {
		t.Errorf("%d role removals, want 1", n)
	}
}

func TestMarketConfigRejectsUngrantableRole(t *testing.T) {
	b, _, _, st := newPosterTestBot(t, 100)
	target := discord.Role{ID: 301, GuildID: roleTestGuild, Position: 5, Permissions: discord.PermissionMentionEveryone}
	opts := `[{"name":"market","type":2,"options":[{"name":"config","type":1,"options":[{"name":"role","type":8,"value":"301"},{"name":"threshold","type":4,"value":5}]}]}]`
	e, responses := roleCommand(t, roleTestOwner, nil, opts, resolvedRole(target))
	b.handleMarketConfig(e)
	if got := response(t, responses).Content; !strings.Contains(got, "moderator permissions") {
		t.Errorf("response = %q", got)
	}
	if st.Settings.RoleID != posterTestRole || st.Settings.Threshold != 100 {
		t.Errorf("settings changed to %+v", st.Settings)
	}
}

func TestMarketProgressEmbed(t *testing.T) {
	fieldValue := func(e discord.Embed, name string) (string, bool) {
		for _, f := range e.Fields {
			if f.Name == name {
				return f.Value, true
			}
		}
		return "", false
	}

	// A failed search must not read as zero posts.
	failed := marketProgress{Username: "seller", Threshold: 100, MemberKnown: true}.embed()
	if v, _ := fieldValue(failed, "Total Posts"); !strings.Contains(v, "Couldn't fetch post history") {
		t.Errorf("Total Posts = %q after a failed search", v)
	}
	if v, ok := fieldValue(failed, "Progress"); ok {
		t.Errorf("Progress shown after a failed search: %q", v)
	}

	ok := marketProgress{Username: "seller", Threshold: 100, Searched: true, Count: 30,
		Top: []channelCount{{ChannelID: 5, Count: 3}}}.embed()
	if v, _ := fieldValue(ok, "Total Posts"); v != "30" {
		t.Errorf("Total Posts = %q, want 30", v)
	}
	if v, _ := fieldValue(ok, "Progress"); v != "30 / 100 (30%) — 70 more needed" {
		t.Errorf("Progress = %q", v)
	}
	if v, _ := fieldValue(ok, "Recent Activity (last 25 posts)"); v != "<#5>: 3" {
		t.Errorf("Recent Activity = %q", v)
	}

	has := marketProgress{Username: "seller", Threshold: 100, Searched: true, Count: 300, MemberKnown: true, HasRole: true}.embed()
	if v, ok := fieldValue(has, "Progress"); ok {
		t.Errorf("Progress shown for a user with the role: %q", v)
	}
}
