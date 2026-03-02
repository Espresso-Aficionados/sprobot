package bot

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/disgoorg/disgo/discord"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

func newTestS3(t *testing.T) *s3client.Client {
	t.Helper()
	server := httptest.NewServer(nil)
	t.Cleanup(server.Close)
	endpoint := server.URL
	cache, _ := lru.New[string, map[string]string](10)
	client := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: &endpoint,
		Credentials:  credentials.NewStaticCredentialsProvider("key", "secret", ""),
		UsePathStyle: true,
	})
	return s3client.NewDirect(client, "test-bucket", endpoint, cache, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestBuildProfileEmbedBasic(t *testing.T) {
	profile := map[string]string{
		"Machine":      "Decent DE1",
		"Grinder":      "Niche Zero",
		"Gear Picture": "https://example.com/image.png",
	}

	embed := buildProfileEmbed(sprobot.ProfileTemplate, "testuser", profile, "123", "456", "prod", newTestS3(t))

	if embed.Title != "Coffee Setup for testuser" {
		t.Errorf("Title = %q, want %q", embed.Title, "Coffee Setup for testuser")
	}
	if embed.Color != rgbToInt(103, 71, 54) {
		t.Errorf("Color = %d, want %d", embed.Color, rgbToInt(103, 71, 54))
	}
	if embed.Footer == nil {
		t.Fatal("Footer is nil")
	}
	if embed.Footer.Text != "sprobot" {
		t.Errorf("Footer.Text = %q, want %q", embed.Footer.Text, "sprobot")
	}
	if embed.Footer.IconURL != footerIconURL {
		t.Errorf("Footer.IconURL = %q, want %q", embed.Footer.IconURL, footerIconURL)
	}
}

func TestBuildProfileEmbedFields(t *testing.T) {
	profile := map[string]string{
		"Machine":        "La Marzocco",
		"Grinder":        "EK43",
		"Favorite Beans": "Ethiopian Yirgacheffe",
	}

	embed := buildProfileEmbed(sprobot.ProfileTemplate, "user", profile, "123", "456", "prod", newTestS3(t))

	fieldNames := make(map[string]string)
	for _, f := range embed.Fields {
		fieldNames[f.Name] = f.Value
	}

	if fieldNames["Machine"] != "La Marzocco" {
		t.Errorf("Machine field = %q, want %q", fieldNames["Machine"], "La Marzocco")
	}
	if fieldNames["Grinder"] != "EK43" {
		t.Errorf("Grinder field = %q, want %q", fieldNames["Grinder"], "EK43")
	}
	if fieldNames["Favorite Beans"] != "Ethiopian Yirgacheffe" {
		t.Errorf("Favorite Beans field = %q, want %q", fieldNames["Favorite Beans"], "Ethiopian Yirgacheffe")
	}
}

func TestBuildProfileEmbedSkipsEmptyFields(t *testing.T) {
	profile := map[string]string{
		"Machine": "Breville",
		"Grinder": "",
	}

	embed := buildProfileEmbed(sprobot.ProfileTemplate, "user", profile, "123", "456", "prod", newTestS3(t))

	for _, f := range embed.Fields {
		if f.Name == "Grinder" {
			t.Error("empty Grinder field should be skipped")
		}
	}
}

func TestBuildProfileEmbedWithImage(t *testing.T) {
	profile := map[string]string{
		"Gear Picture": "https://example.com/photo.jpg",
	}

	embed := buildProfileEmbed(sprobot.ProfileTemplate, "user", profile, "123", "456", "prod", newTestS3(t))

	if embed.Image == nil {
		t.Fatal("Image is nil when profile has image")
	}
	if embed.Image.URL != "https://example.com/photo.jpg" {
		t.Errorf("Image URL = %q, want external URL unchanged", embed.Image.URL)
	}
}

func TestBuildProfileEmbedWithoutImage(t *testing.T) {
	profile := map[string]string{
		"Machine": "Rocket",
	}

	embed := buildProfileEmbed(sprobot.ProfileTemplate, "user", profile, "123", "456", "prod", newTestS3(t))

	if embed.Image != nil {
		t.Error("Image should be nil when no image in profile")
	}

	// Should have a "Want to add a profile image?" field
	found := false
	for _, f := range embed.Fields {
		if f.Name == "Want to add a profile image?" {
			found = true
			if !strings.Contains(f.Value, "sprobot.html") {
				t.Errorf("image help field value %q missing guide URL", f.Value)
			}
		}
	}
	if !found {
		t.Error("missing 'Want to add a profile image?' field when no image")
	}
}

func TestBuildProfileEmbedURL(t *testing.T) {
	profile := map[string]string{}
	embed := buildProfileEmbed(sprobot.ProfileTemplate, "user", profile, "123", "456", "prod", newTestS3(t))

	want := sprobot.WebEndpoint + "profiles/123/Coffee%20Setup/456"
	if embed.URL != want {
		t.Errorf("embed URL = %q, want %q", embed.URL, want)
	}
}

func TestRgbToInt(t *testing.T) {
	tests := []struct {
		r, g, b int
		want    int
	}{
		{103, 71, 54, 0x674736},
		{0, 0, 0, 0x000000},
		{255, 255, 255, 0xFFFFFF},
		{255, 0, 0, 0xFF0000},
		{0, 255, 0, 0x00FF00},
		{0, 0, 255, 0x0000FF},
	}
	for _, tt := range tests {
		got := rgbToInt(tt.r, tt.g, tt.b)
		if got != tt.want {
			t.Errorf("rgbToInt(%d, %d, %d) = 0x%06X, want 0x%06X", tt.r, tt.g, tt.b, got, tt.want)
		}
	}
}

func TestGetNickOrName(t *testing.T) {
	nick := "CoolNick"
	tests := []struct {
		nick     *string
		username string
		want     string
	}{
		{&nick, "user123", "CoolNick"},
		{nil, "user123", "user123"},
	}
	for _, tt := range tests {
		member := &discord.ResolvedMember{
			Member: discord.Member{
				Nick: tt.nick,
				User: discord.User{Username: tt.username},
			},
		}
		got := getNickOrName(member)
		if got != tt.want {
			t.Errorf("getNickOrName(nick=%v, user=%q) = %q, want %q", tt.nick, tt.username, got, tt.want)
		}
	}
}
