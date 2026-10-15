package main

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sadbox/sprobot/pkg/sprobot"
	"github.com/sadbox/sprobot/pkg/testutil"
)

func TestMain(m *testing.M) {
	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"toJSON": func(v any) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
	}

	pageTemplates = make(map[string]*template.Template)
	for _, name := range []string{"index.html", "profile.html", "404.html"} {
		t, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/base.html", "templates/"+name)
		if err != nil {
			log.Fatalf("Failed to parse template %s: %v", name, err)
		}
		pageTemplates[name] = t
	}
	os.Exit(m.Run())
}

func TestIndexTemplate(t *testing.T) {
	tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/index.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	rec := httptest.NewRecorder()
	if err := tmpl.ExecuteTemplate(rec, "base.html", nil); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	body := rec.Body.String()

	checks := []string{
		"espressoaf.com",
		"sprobot",
		"<!DOCTYPE html>",
		"<html",
		"Profile bot for",
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("index page missing %q", want)
		}
	}
}

func TestProfileTemplate(t *testing.T) {
	tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/profile.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	data := profileData{
		TemplateName: "Coffee Setup",
		Fields: []profileField{
			{Name: "Machine", Value: "Decent DE1"},
			{Name: "Grinder", Value: "Niche Zero"},
			{Name: "Favorite Beans", Value: "Ethiopian Yirgacheffe"},
		},
		ImageURL: "https://example.com/image.jpg",
		Preview:  linkPreview{Title: "Coffee Setup"},
	}

	rec := httptest.NewRecorder()
	if err := tmpl.ExecuteTemplate(rec, "base.html", data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	body := rec.Body.String()

	checks := []string{
		"Coffee Setup",
		"Decent DE1",
		"Niche Zero",
		"Ethiopian Yirgacheffe",
		"https://example.com/image.jpg",
		"Coffee Setup - sprobot", // title
		"card-title",
		"field-name",
		"field-value",
		"profile-image",
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("profile page missing %q", want)
		}
	}
}

func TestProfileTemplateNoImage(t *testing.T) {
	tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/profile.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	data := profileData{
		TemplateName: "Coffee Setup",
		Fields: []profileField{
			{Name: "Machine", Value: "Breville"},
		},
		ImageURL: "",
	}

	rec := httptest.NewRecorder()
	if err := tmpl.ExecuteTemplate(rec, "base.html", data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, `<div class="profile-image">`) {
		t.Error("profile page should not contain profile-image div when no image URL")
	}
}

func TestProfileTemplateEmptyFields(t *testing.T) {
	tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/profile.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	data := profileData{
		TemplateName: "Roasting Setup",
		Fields:       nil,
		ImageURL:     "",
	}

	rec := httptest.NewRecorder()
	if err := tmpl.ExecuteTemplate(rec, "base.html", data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Roasting Setup") {
		t.Error("template name should still appear")
	}
}

func Test404Template(t *testing.T) {
	tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/404.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	rec := httptest.NewRecorder()
	if err := tmpl.ExecuteTemplate(rec, "base.html", nil); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Profile not found") {
		t.Error("404 page missing 'Profile not found'")
	}
	if !strings.Contains(body, "Profile Not Found - sprobot") {
		t.Error("404 page missing correct title")
	}
}

func TestBaseTemplateStructure(t *testing.T) {
	tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/index.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	rec := httptest.NewRecorder()
	if err := tmpl.ExecuteTemplate(rec, "base.html", nil); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	body := rec.Body.String()

	// Verify base template structure
	checks := []string{
		`<meta charset="UTF-8">`,
		`<meta name="viewport"`,
		`class="header"`,
		`class="container"`,
		"76916743.gif",              // sprobot icon
		`<a href="/">`,              // home link
		`background-color: #2b2d31`, // Discord dark theme
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("base template missing %q", want)
		}
	}
}

func TestHandleIndexReturns200(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handleIndex(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestHandleIndexNonRootReturns404(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/something", nil)
	handleIndex(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRender404SetsStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	render404(rec)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Profile not found") {
		t.Error("404 response missing expected content")
	}
}

func TestRenderPageInvalidName(t *testing.T) {
	rec := httptest.NewRecorder()
	renderPage(rec, "nonexistent.html", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestStatusRecorder(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: 200}

	sr.WriteHeader(http.StatusNotFound)
	if sr.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", sr.status)
	}
}

func TestAccessLogMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	wrapped := accessLog(handler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "1.2.3.4:5678"

	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAccessLogUsesXForwardedFor(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := accessLog(handler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.RemoteAddr = "1.2.3.4:5678"

	// Just verify it doesn't panic
	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAccessLogRecords404(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	wrapped := accessLog(handler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)

	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestProfileFieldStruct(t *testing.T) {
	f := profileField{Name: "Machine", Value: "Decent"}
	if f.Name != "Machine" || f.Value != "Decent" {
		t.Errorf("profileField = %+v, unexpected", f)
	}
}

func TestProfileDataStruct(t *testing.T) {
	d := profileData{
		TemplateName: "Coffee Setup",
		Fields: []profileField{
			{Name: "Machine", Value: "Linea"},
		},
		ImageURL: "https://example.com/img.png",
	}
	if d.TemplateName != "Coffee Setup" {
		t.Errorf("TemplateName = %q", d.TemplateName)
	}
	if len(d.Fields) != 1 {
		t.Errorf("len(Fields) = %d", len(d.Fields))
	}
	if d.ImageURL != "https://example.com/img.png" {
		t.Errorf("ImageURL = %q", d.ImageURL)
	}
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := securityHeaders(inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	expected := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for header, want := range expected {
		got := rec.Header().Get(header)
		if got != want {
			t.Errorf("header %q = %q, want %q", header, got, want)
		}
	}

	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("Content-Security-Policy header should be set")
	}
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP %q should contain default-src 'none'", csp)
	}
}

func TestSanitizeLog(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"clean string", "hello world", "hello world"},
		{"empty string", "", ""},
		{"embedded newline", "line1\nline2", "line1line2"},
		{"embedded carriage return", "line1\rline2", "line1line2"},
		{"both CR and LF", "a\r\nb", "ab"},
		{"multiple newlines", "a\n\n\nb", "ab"},
		{"only newlines", "\n\r\n", ""},
		{"IP with injection", "1.2.3.4\nINJECTED: evil", "1.2.3.4INJECTED: evil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeLog(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeLog(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestProfileTemplateHTMLEscaping(t *testing.T) {
	tmpl, err := template.ParseFS(templateFS, "templates/base.html", "templates/profile.html")
	if err != nil {
		t.Fatalf("Failed to parse templates: %v", err)
	}

	data := profileData{
		TemplateName: "Coffee Setup",
		Fields: []profileField{
			{Name: "Machine", Value: "<script>alert('xss')</script>"},
		},
	}

	rec := httptest.NewRecorder()
	if err := tmpl.ExecuteTemplate(rec, "base.html", data); err != nil {
		t.Fatalf("Failed to execute template: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, "<script>") {
		t.Error("template did not escape HTML — XSS vulnerability")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected escaped script tag")
	}
}

// --- Link previews and the Discord-only image route ---

const discordUA = "Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)"

func newTestServer(t *testing.T) (*testutil.FakeS3, *http.ServeMux) {
	t.Helper()
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	s3 := testutil.NewTestS3Client(t, server)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /profiles/{guildID}/{templateName}/{userID}", handleProfile(s3))
	mux.HandleFunc("GET /profiles/{guildID}/{templateName}/{userID}/{file}", handleProfileImage(s3))
	return fake, mux
}

func putObject(fake *testutil.FakeS3, key string, data []byte) {
	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	fake.Objects["/test-bucket/"+key] = data
}

func putProfile(t *testing.T, fake *testutil.FakeS3, profile map[string]string) {
	t.Helper()
	b, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	putObject(fake, "profiles/1/Coffee Setup/2.json", b)
}

func get(mux *http.ServeMux, target, userAgent string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("User-Agent", userAgent)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestProfilePageLinkPreview(t *testing.T) {
	fake, mux := newTestServer(t)
	putProfile(t, fake, map[string]string{
		"Machine":               "Decent DE1",
		"Grinder":               "Niche Zero",
		"Gear Picture":          "images/1/Coffee Setup/2.png",
		sprobot.DisplayNameKey:  "alice",
		sprobot.ImageVersionKey: "abc",
	})

	rec := get(mux, "/profiles/1/Coffee%20Setup/2", "Mozilla/5.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	checks := []string{
		`<meta property="og:site_name" content="sprobot">`,
		`<meta property="og:title" content="Coffee Setup for alice">`,
		`<meta property="og:description" content="Machine: Decent DE1` + "\n" + `Grinder: Niche Zero">`,
		`<meta name="theme-color" content="#674736">`,
		`<meta property="og:image" content="https://bot.espressoaf.com/profiles/1/Coffee%20Setup/2/image.png?v=abc">`,
		`<meta property="og:image:alt" content="Gear Picture">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<title>Coffee Setup for alice - sprobot</title>`,
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("profile page missing %q", want)
		}
	}
	// The page's own <img> must stay on a presigned (expiring) URL.
	if !strings.Contains(body, "X-Amz-Signature") {
		t.Error("profile page <img> should use a presigned URL")
	}
	if strings.Contains(body, sprobot.DisplayNameKey) || strings.Contains(body, sprobot.ImageVersionKey) {
		t.Error("reserved keys must not render as fields")
	}
	if strings.Index(body, ">Machine<") > strings.Index(body, ">Grinder<") {
		t.Error("fields should render in template order (Machine before Grinder)")
	}
}

func TestProfilePageNoImageNoName(t *testing.T) {
	fake, mux := newTestServer(t)
	putProfile(t, fake, map[string]string{"Machine": "Rocket"})

	body := get(mux, "/profiles/1/Coffee%20Setup/2", "Mozilla/5.0").Body.String()
	if !strings.Contains(body, `<meta property="og:title" content="Coffee Setup">`) {
		t.Error("og:title should fall back to the template name when no display name is stored")
	}
	for _, unwanted := range []string{"og:image", "twitter:card"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("profile without image should not emit %s", unwanted)
		}
	}
}

func TestProfileImageRedirectsNonDiscord(t *testing.T) {
	_, mux := newTestServer(t)
	for _, ua := range []string{"", "Mozilla/5.0 (Macintosh) Chrome/130", "curl/8.0", "Mozilla/5.0 discord/1.0.9163 Electron/32"} {
		rec := get(mux, "/profiles/1/Coffee%20Setup/2/image.png?v=abc", ua)
		if rec.Code != http.StatusFound {
			t.Errorf("UA %q: status = %d, want 302", ua, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/profiles/1/Coffee%20Setup/2" {
			t.Errorf("UA %q: Location = %q, want profile page", ua, loc)
		}
		if rec.Header().Get("Vary") != "User-Agent" {
			t.Errorf("UA %q: missing Vary: User-Agent", ua)
		}
	}
}

func TestProfileImageServesDiscord(t *testing.T) {
	fake, mux := newTestServer(t)
	putProfile(t, fake, map[string]string{"Gear Picture": "images/1/Coffee Setup/2.png"})
	png := []byte("\x89PNG\r\n\x1a\nfake")
	putObject(fake, "images/1/Coffee Setup/2.png", png)

	rec := get(mux, "/profiles/1/Coffee%20Setup/2/image.png?v=abc", discordUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if rec.Body.String() != string(png) {
		t.Error("body should be the stored image bytes")
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "max-age") {
		t.Error("image response should be cacheable")
	}
}

func TestProfileImageNotFound(t *testing.T) {
	fake, mux := newTestServer(t)

	if rec := get(mux, "/profiles/1/Coffee%20Setup/2/image.png", discordUA); rec.Code != http.StatusNotFound {
		t.Errorf("missing profile: status = %d, want 404", rec.Code)
	}

	putProfile(t, fake, map[string]string{"Machine": "Rocket"})
	if rec := get(mux, "/profiles/1/Coffee%20Setup/2/image.png", discordUA); rec.Code != http.StatusNotFound {
		t.Errorf("profile without image: status = %d, want 404", rec.Code)
	}

	putProfile(t, fake, map[string]string{"Gear Picture": "https://example.com/x.png"})
	if rec := get(mux, "/profiles/1/Coffee%20Setup/2/image.png", discordUA); rec.Code != http.StatusNotFound {
		t.Errorf("external image: status = %d, want 404 (third-party hosts are not proxied)", rec.Code)
	}

	if rec := get(mux, "/profiles/1/Coffee%20Setup/2/other", discordUA); rec.Code != http.StatusNotFound {
		t.Errorf("non-image path: status = %d, want 404", rec.Code)
	}
}

func TestIsDiscordFetcher(t *testing.T) {
	tests := []struct {
		ua   string
		want bool
	}{
		{discordUA, true},
		{"DiscordBot", true},
		{"something discordapp.com", true},
		{"Mozilla/5.0 discord/1.0.9163 Electron/32", false},
		{"Mozilla/5.0 (Macintosh) Chrome/130", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isDiscordFetcher(tt.ua); got != tt.want {
			t.Errorf("isDiscordFetcher(%q) = %v, want %v", tt.ua, got, tt.want)
		}
	}
}

func TestTruncateBytes(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"hello world", 10, "hello w…"},
		{"hello    world", 10, "hello…"},
		{"ééééé", 7, "éé…"}, // never splits a multi-byte rune
	}
	for _, tt := range tests {
		got := truncateBytes(tt.in, tt.n)
		if got != tt.want {
			t.Errorf("truncateBytes(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
		if len(got) > tt.n {
			t.Errorf("truncateBytes(%q, %d) = %d bytes, over limit", tt.in, tt.n, len(got))
		}
	}
}

func TestLookupTemplateUnknownFallsBack(t *testing.T) {
	tmpl := lookupTemplate("Retired Template", map[string]string{
		"Zed":                  "z",
		"Alpha":                "a",
		sprobot.ImageField:     "images/x.png",
		sprobot.DisplayNameKey: "alice",
	})
	if len(tmpl.Fields) != 2 || tmpl.Fields[0].Name != "Alpha" || tmpl.Fields[1].Name != "Zed" {
		t.Errorf("fallback fields = %+v, want [Alpha Zed]", tmpl.Fields)
	}
	if tmpl.Image.Name != sprobot.ImageField {
		t.Errorf("fallback image field = %q", tmpl.Image.Name)
	}
}
