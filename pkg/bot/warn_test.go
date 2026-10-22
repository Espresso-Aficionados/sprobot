package bot

import (
	"strings"
	"sync"
	"testing"
)

func TestWarnLinksPostedMessageWithoutRefetching(t *testing.T) {
	b, stub := newEventLogBot(t)
	b.modLog = newGuildStateStore(b.S3, b.Log, "modlog",
		func() *modLogState { return &modLogState{} },
		func(st *modLogState) *sync.Mutex { return &st.mu })
	stub.on("POST /interactions/1/tok/callback", 200,
		`{"interaction":{"id":"1","type":2,"response_message_id":"55"},`+
			`"resource":{"type":4,"message":{"id":"55","channel_id":"20","author":{"id":"2","username":"sprobot"}}}}`)

	e, _ := commandEvent(t, b.Client, `{"id":"100","name":"warn","type":1,`+
		`"options":[{"name":"user","type":6,"value":"31"},{"name":"reason","type":3,"value":"spam"}],`+
		`"resolved":{"users":{"31":{"id":"31","username":"bob"}}}}`)
	b.handleWarn(e)

	if got := stub.bodies("POST /interactions/1/tok/callback"); len(got) != 1 || !strings.Contains(got[0], "spam") {
		t.Fatalf("warning responses = %v, want one carrying the reason", got)
	}
	if got := stub.bodies("GET /webhooks/2/tok/messages/@original"); len(got) != 0 {
		t.Error("fetched the response again to learn its ID")
	}
	logs := stub.bodies("POST /channels/15/messages")
	if len(logs) != 1 || !strings.Contains(logs[0], "https://discord.com/channels/10/20/55") {
		t.Errorf("event log posts = %v, want one linking the warning message", logs)
	}
}
