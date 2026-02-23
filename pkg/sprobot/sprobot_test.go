package sprobot

import "testing"

func TestProfileS3Path(t *testing.T) {
	tests := []struct {
		guildID, templateName, userID, want string
	}{
		{"123", "Coffee Setup", "456", "profiles/123/Coffee Setup/456.json"},
		{"999", "Roasting Setup", "111", "profiles/999/Roasting Setup/111.json"},
		{"0", "", "0", "profiles/0//0.json"},
	}
	for _, tt := range tests {
		got := ProfileS3Path(tt.guildID, tt.templateName, tt.userID)
		if got != tt.want {
			t.Errorf("ProfileS3Path(%q, %q, %q) = %q, want %q",
				tt.guildID, tt.templateName, tt.userID, got, tt.want)
		}
	}
}

func TestConstants(t *testing.T) {
	if ImageField != "Gear Picture" {
		t.Errorf("ImageField = %q, want %q", ImageField, "Gear Picture")
	}
	if WebEndpoint != "https://bot.espressoaf.com/" {
		t.Errorf("WebEndpoint = %q, want %q", WebEndpoint, "https://bot.espressoaf.com/")
	}
}
