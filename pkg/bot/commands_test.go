package bot

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// commandOption follows path (command name, then option names) through
// registered commands, returning the option found.
func commandOption(t *testing.T, commands []map[string]any, path ...string) map[string]any {
	t.Helper()
	var options []any
	for _, c := range commands {
		if c["name"] == path[0] {
			options, _ = c["options"].([]any)
		}
	}
	var found map[string]any
	for _, name := range path[1:] {
		found = nil
		for _, o := range options {
			if opt := o.(map[string]any); opt["name"] == name {
				found = opt
			}
		}
		if found == nil {
			t.Fatalf("no option %q on the way to %v", name, path)
		}
		options, _ = found["options"].([]any)
	}
	return found
}

func channelTypesOf(opt map[string]any) []discord.ChannelType {
	var types []discord.ChannelType
	raw, _ := opt["channel_types"].([]any)
	for _, v := range raw {
		types = append(types, discord.ChannelType(v.(float64)))
	}
	return types
}

func TestRegisteredCommandOptions(t *testing.T) {
	b, stub := newStubBot(t)
	b.TestGuildIDsOverride = []snowflake.ID{10}
	stub.on("PUT /applications/2/guilds/10/commands", 200, "[]")

	if err := b.registerAllCommands(); err != nil {
		t.Fatal(err)
	}
	bodies := stub.bodies("PUT /applications/2/guilds/10/commands")
	if len(bodies) != 1 {
		t.Fatalf("registered commands %d times, want 1", len(bodies))
	}
	var commands []map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &commands); err != nil {
		t.Fatal(err)
	}

	text := []discord.ChannelType{discord.ChannelTypeGuildText, discord.ChannelTypeGuildNews}
	tests := []struct {
		path []string
		want []discord.ChannelType
	}{
		{[]string{"config", "eventlog", "set", "channel"}, text},
		{[]string{"config", "renamelog", "set", "channel"}, text},
		{[]string{"config", "starboard", "config", "channel"}, text},
		{[]string{"config", "modlog", "set", "channel"}, []discord.ChannelType{discord.ChannelTypeGuildForum}},
		{[]string{"config", "topposters", "filter-add", "channel"}, messageChannelTypes},
		// These match parents and categories too, or monitor any channel.
		{[]string{"config", "market", "filter-add", "channel"}, nil},
		{[]string{"config", "starboard", "filter-add", "channel"}, nil},
		{[]string{"config", "renamelog", "add", "channel"}, nil},
	}
	for _, tt := range tests {
		if got := channelTypesOf(commandOption(t, commands, tt.path...)); !slices.Equal(got, tt.want) {
			t.Errorf("%v channel types = %v, want %v", tt.path, got, tt.want)
		}
	}

	threshold := commandOption(t, commands, "config", "market", "config", "threshold")
	if threshold["min_value"] != 1.0 || threshold["max_value"] != 10000.0 {
		t.Errorf("market threshold bounds = %v..%v, want 1..10000", threshold["min_value"], threshold["max_value"])
	}
}

// configSetData is /config <group> set channel:77, where 77 has channelType.
func configSetData(group string, channelType discord.ChannelType) string {
	return `{"id":"100","name":"config","type":1,"options":[{"name":"` + group + `","type":2,"options":[` +
		`{"name":"set","type":1,"options":[{"name":"channel","type":7,"value":"77"}]}]}],` +
		`"resolved":{"channels":{"77":{"id":"77","name":"somewhere","type":` + jsonNumber(int(channelType)) + `,"permissions":"0"}}}}`
}

func TestLogChannelSetChecksType(t *testing.T) {
	b, _ := newEventLogBot(t)
	tests := []struct {
		group string
		dest  func() snowflake.ID
	}{
		{"eventlog", func() snowflake.ID { return b.eventLog.get(10).ChannelID }},
		{"renamelog", func() snowflake.ID { return b.renameLogs.get(10).Destination }},
	}
	run := func(group string, data string) discord.InteractionResponseData {
		e, replies := commandEvent(t, b.Client, data)
		switch group {
		case "eventlog":
			b.handleEventLogConfig(e)
		case "renamelog":
			b.handleRenameLog(e)
		}
		_, resp := replies.only(t)
		return resp
	}
	for _, tt := range tests {
		before := tt.dest()
		resp := run(tt.group, configSetData(tt.group, discord.ChannelTypeGuildForum))
		if tt.dest() != before || !isEphemeral(resp) {
			t.Errorf("%s set accepted a forum channel (reply %q)", tt.group, contentOf(resp))
		}
		run(tt.group, configSetData(tt.group, discord.ChannelTypeGuildNews))
		if tt.dest() != 77 {
			t.Errorf("%s set refused an announcement channel", tt.group)
		}
	}
}

func TestUnknownComponentIsAnswered(t *testing.T) {
	b, stub := newStubBot(t)

	e, replies := componentEvent(t, b.Client, "del_profile_retired", "", "")
	b.handleComponentInteraction(e)

	_, data := replies.only(t)
	if !isEphemeral(data) || contentOf(data) != noLongerAvailable {
		t.Errorf("reply = %+v, want an ephemeral %q", data, noLongerAvailable)
	}
	if got := stub.routesCalled(); len(got) != 0 {
		t.Errorf("unexpected calls %v", got)
	}
}

func TestUnknownModalIsAnswered(t *testing.T) {
	b, _ := newStubBot(t)

	for _, id := range []string{"retired_modal", "modlog_notanid_123"} {
		e, replies := modalEvent(t, b.Client, id, "")
		b.handleModalSubmit(e)

		_, data := replies.only(t)
		if !isEphemeral(data) || contentOf(data) != noLongerAvailable {
			t.Errorf("%s: reply = %+v, want an ephemeral %q", id, data, noLongerAvailable)
		}
	}
}
