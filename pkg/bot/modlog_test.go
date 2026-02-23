package bot

import "testing"

func TestGetModLogConfigDev(t *testing.T) {
	config := getModLogCfg("dev")
	if config == nil {
		t.Fatal("dev config is nil")
	}
	if config.ChannelID != 1142519200682876938 {
		t.Errorf("ChannelID = %d, want %d", config.ChannelID, 1142519200682876938)
	}
}

func TestGetModLogConfigProd(t *testing.T) {
	config := getModLogCfg("prod")
	if config == nil {
		t.Fatal("prod config is nil")
	}
	if config.ChannelID != 1141477354129080361 {
		t.Errorf("ChannelID = %d, want %d", config.ChannelID, 1141477354129080361)
	}
}

func TestGetModLogConfigUnknown(t *testing.T) {
	if getModLogCfg("staging") != nil {
		t.Error("expected nil for unknown env")
	}
	if getModLogCfg("") != nil {
		t.Error("expected nil for empty env")
	}
}

func TestMessageLink(t *testing.T) {
	got := messageLink("111", "222", "333")
	want := "https://discord.com/channels/111/222/333"
	if got != want {
		t.Errorf("messageLink = %q, want %q", got, want)
	}
}
