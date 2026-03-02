package main

import (
	"html/template"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
	}

	pageTemplates = make(map[string]*template.Template)
	for _, name := range []string{"index.html", "profile.html", "404.html"} {
		t, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/base.html", "templates/"+name)
		if err != nil {
			log.Fatalf("Failed to parse template %s: %v", name, err)
		}
		pageTemplates[name] = t
	}
	for _, name := range []string{"login.html", "admin_dashboard.html", "admin_guild.html", "admin_profiles.html", "admin_selfroles.html", "admin_tickets.html"} {
		t, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/base.html", "templates/"+name)
		if err != nil {
			log.Fatalf("Failed to parse admin template %s: %v", name, err)
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

func TestSecurityHeadersAdminCSP(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := securityHeaders(inner)
	req := httptest.NewRequest(http.MethodGet, "/admin/123/profiles", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'unsafe-inline'") {
		t.Errorf("admin CSP %q should contain script-src 'unsafe-inline'", csp)
	}
	if !strings.Contains(csp, "form-action 'self'") {
		t.Errorf("admin CSP %q should contain form-action 'self'", csp)
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

// --- Session store tests ---

func TestSessionStore(t *testing.T) {
	store := newSessionStore()

	sess := &session{
		User:      discordUser{ID: "123", Username: "testuser"},
		ExpiresAt: time.Now().Add(time.Hour),
	}

	store.Set("abc", sess)

	got, ok := store.Get("abc")
	if !ok {
		t.Fatal("session not found")
	}
	if got.User.ID != "123" {
		t.Errorf("user ID = %q, want %q", got.User.ID, "123")
	}

	// Non-existent session
	_, ok = store.Get("xyz")
	if ok {
		t.Error("expected no session for key xyz")
	}

	// Delete
	store.Delete("abc")
	_, ok = store.Get("abc")
	if ok {
		t.Error("session should be deleted")
	}
}

func TestSessionStoreExpired(t *testing.T) {
	store := newSessionStore()

	sess := &session{
		User:      discordUser{ID: "456"},
		ExpiresAt: time.Now().Add(-time.Hour), // already expired
	}

	store.Set("expired", sess)

	_, ok := store.Get("expired")
	if ok {
		t.Error("expired session should not be returned")
	}
}

func TestIsGuildAdmin(t *testing.T) {
	tests := []struct {
		name  string
		perms int64
		want  bool
	}{
		{"no permissions", 0, false},
		{"administrator", 0x8, true},
		{"manage guild", 0x20, true},
		{"both", 0x28, true},
		{"only send messages", 0x800, false},
		{"admin with other perms", 0x8 | 0x400, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isGuildAdmin(tt.perms)
			if got != tt.want {
				t.Errorf("isGuildAdmin(0x%x) = %v, want %v", tt.perms, got, tt.want)
			}
		})
	}
}

func TestAdminAuthRedirectsWithoutCookie(t *testing.T) {
	store := newSessionStore()
	handler := adminAuth(store, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	handler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/admin/login" {
		t.Errorf("Location = %q, want /admin/login", loc)
	}
}

func TestAdminAuthRedirectsWithInvalidSession(t *testing.T) {
	store := newSessionStore()
	handler := adminAuth(store, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "invalid"})
	handler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}
}

func TestAdminAuthPassesWithValidSession(t *testing.T) {
	store := newSessionStore()
	store.Set("valid", &session{
		User:      discordUser{ID: "123"},
		ExpiresAt: time.Now().Add(time.Hour),
	})

	called := false
	handler := adminAuth(store, func(w http.ResponseWriter, r *http.Request) {
		called = true
		sess := getSession(r)
		if sess == nil {
			t.Error("session not in context")
		}
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "valid"})
	handler(rec, req)

	if !called {
		t.Error("handler not called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestLoginTemplateRenders(t *testing.T) {
	rec := httptest.NewRecorder()
	renderPage(rec, "login.html", struct{ LoginURL string }{"https://discord.com/auth"})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Login with Discord") {
		t.Error("login page missing 'Login with Discord'")
	}
	if !strings.Contains(body, "https://discord.com/auth") {
		t.Error("login page missing login URL")
	}
}

func TestAdminDashboardTemplateRenders(t *testing.T) {
	type guildInfo struct {
		ID   string
		Name string
	}
	rec := httptest.NewRecorder()
	renderPage(rec, "admin_dashboard.html", struct {
		Guilds []guildInfo
	}{
		Guilds: []guildInfo{
			{ID: "123", Name: "Test Server"},
		},
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Test Server") {
		t.Error("dashboard missing guild name")
	}
	if !strings.Contains(body, "/admin/123/") {
		t.Error("dashboard missing guild link")
	}
}

func TestShortNameRegex(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"profile", true},
		{"roaster", true},
		{"my-template", true},
		{"a", true},
		{"a1", true},
		{"Profile", false},  // uppercase
		{"1profile", false}, // starts with number
		{"-profile", false}, // starts with hyphen
		{"", false},         // empty
		{"a b", false},      // space
		{"profile!", false}, // special char
		{"a234567890123456789012345678901x", true}, // 32 chars (max)
	}
	for _, tt := range tests {
		got := shortNameRegex.MatchString(tt.input)
		if got != tt.valid {
			t.Errorf("shortNameRegex.MatchString(%q) = %v, want %v", tt.input, got, tt.valid)
		}
	}
}

func TestRandomSessionID(t *testing.T) {
	id1 := randomSessionID()
	id2 := randomSessionID()

	if len(id1) != 64 { // 32 bytes → 64 hex chars
		t.Errorf("session ID length = %d, want 64", len(id1))
	}
	if id1 == id2 {
		t.Error("two random session IDs should not be equal")
	}
}

func TestHandleLogout(t *testing.T) {
	store := newSessionStore()
	store.Set("mysess", &session{
		User:      discordUser{ID: "123"},
		ExpiresAt: time.Now().Add(time.Hour),
	})

	handler := handleLogout(&oauthConfig{SecureCookie: true}, store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/logout", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "mysess"})
	handler(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", rec.Code)
	}

	// Session should be deleted
	_, ok := store.Get("mysess")
	if ok {
		t.Error("session should be deleted after logout")
	}

	// Check cookie is cleared
	cookies := rec.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "session" && c.MaxAge < 0 {
			found = true
		}
	}
	if !found {
		t.Error("session cookie should be cleared")
	}
}

func TestGetOAuthConfigMissing(t *testing.T) {
	t.Setenv("DISCORD_CLIENT_ID", "")
	t.Setenv("DISCORD_CLIENT_SECRET", "")
	t.Setenv("DISCORD_REDIRECT_URI", "")

	cfg := getOAuthConfig()
	if cfg != nil {
		t.Error("expected nil when env vars are missing")
	}
}

func TestGetOAuthConfigPresent(t *testing.T) {
	t.Setenv("DISCORD_CLIENT_ID", "123")
	t.Setenv("DISCORD_CLIENT_SECRET", "secret")
	t.Setenv("DISCORD_REDIRECT_URI", "https://example.com/callback")
	t.Setenv("SPROBOT_ENV", "prod")

	cfg := getOAuthConfig()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.ClientID != "123" {
		t.Errorf("ClientID = %q", cfg.ClientID)
	}
	if cfg.ClientSecret != "secret" {
		t.Errorf("ClientSecret = %q", cfg.ClientSecret)
	}
	if cfg.RedirectURI != "https://example.com/callback" {
		t.Errorf("RedirectURI = %q", cfg.RedirectURI)
	}
	if !cfg.SecureCookie {
		t.Error("SecureCookie should be true for prod")
	}
}

func TestGetOAuthConfigDevInsecureCookie(t *testing.T) {
	t.Setenv("DISCORD_CLIENT_ID", "123")
	t.Setenv("DISCORD_CLIENT_SECRET", "secret")
	t.Setenv("DISCORD_REDIRECT_URI", "http://localhost:8080/auth/callback")
	t.Setenv("SPROBOT_ENV", "dev")

	cfg := getOAuthConfig()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.SecureCookie {
		t.Error("SecureCookie should be false for dev")
	}
}

func TestGuildHubTemplateRenders(t *testing.T) {
	rec := httptest.NewRecorder()
	renderAdminPage(rec, "admin_guild.html", struct {
		GuildID string
	}{"123456"})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	checks := []string{
		"Server Settings",
		"/admin/123456/profiles",
		"/admin/123456/selfroles",
		"/admin/123456/tickets",
		"Profile Templates",
		"Self-Assign Roles",
		"Tickets",
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("guild hub page missing %q", want)
		}
	}
}

func TestSelfroleTemplateRenders(t *testing.T) {
	rec := httptest.NewRecorder()
	renderAdminPage(rec, "admin_selfroles.html", struct {
		GuildID string
		Panels  []selfrolePanel
		Success bool
		Error   string
	}{
		GuildID: "123",
		Panels: []selfrolePanel{
			{
				ChannelID: 999,
				Message:   "Test message",
				Buttons: []selfroleButton{
					{Label: "TestBtn", Emoji: "X", RoleID: 111},
				},
			},
		},
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	checks := []string{
		"Self-Assign Roles",
		"Test message",
		"TestBtn",
		"panel_0_channel_id",
		"panel_0_btn_0_label",
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("selfrole page missing %q", want)
		}
	}
}

func TestSelfroleTemplateEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	renderAdminPage(rec, "admin_selfroles.html", struct {
		GuildID string
		Panels  []selfrolePanel
		Success bool
		Error   string
	}{GuildID: "123"})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestTicketTemplateRendersWithConfig(t *testing.T) {
	rec := httptest.NewRecorder()
	renderAdminPage(rec, "admin_tickets.html", struct {
		GuildID   string
		Config    ticketWebConfig
		HasConfig bool
		Success   bool
		Error     string
	}{
		GuildID: "123",
		Config: ticketWebConfig{
			ChannelID:        999,
			StaffRoleID:      888,
			CounterOffset:    100,
			PanelButtonLabel: "Open Ticket",
			PanelMessage:     "Click below!",
			TicketIntro:      "Hello %s",
			CloseButtonLabel: "Close Ticket",
		},
		HasConfig: true,
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	checks := []string{
		"Ticket System",
		"Click below!",
		"Open Ticket",
		"Close Ticket",
		"channel_id",
		"staff_role_id",
		"Remove Configuration",
	}
	for _, want := range checks {
		if !strings.Contains(body, want) {
			t.Errorf("ticket page missing %q", want)
		}
	}
}

func TestTicketTemplateRendersWithoutConfig(t *testing.T) {
	rec := httptest.NewRecorder()
	renderAdminPage(rec, "admin_tickets.html", struct {
		GuildID   string
		Config    ticketWebConfig
		HasConfig bool
		Success   bool
		Error     string
	}{GuildID: "123", HasConfig: false})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No ticket system configured") {
		t.Error("ticket page should show empty state when no config")
	}
	if !strings.Contains(body, "Set Up Tickets") {
		t.Error("ticket page should show setup form when no config")
	}
}

func TestGetHardcodedSelfroles(t *testing.T) {
	dev := getHardcodedSelfroles("dev")
	if dev == nil {
		t.Fatal("dev selfroles nil")
	}
	panels, ok := dev["1013566342345019512"]
	if !ok || len(panels) != 1 {
		t.Errorf("expected 1 dev panel, got %d", len(panels))
	}

	prod := getHardcodedSelfroles("prod")
	if prod == nil {
		t.Fatal("prod selfroles nil")
	}
	panels, ok = prod["726985544038612993"]
	if !ok || len(panels) != 2 {
		t.Errorf("expected 2 prod panels, got %d", len(panels))
	}

	if getHardcodedSelfroles("staging") != nil {
		t.Error("expected nil for unknown env")
	}
}

func TestGetHardcodedTickets(t *testing.T) {
	dev := getHardcodedTickets("dev")
	if dev == nil {
		t.Fatal("dev tickets nil")
	}
	cfg, ok := dev["1013566342345019512"]
	if !ok {
		t.Fatal("missing dev guild")
	}
	if cfg.ChannelID == 0 {
		t.Error("dev ChannelID should be set")
	}
	if cfg.PanelMessage == "" {
		t.Error("dev PanelMessage should be set")
	}

	prod := getHardcodedTickets("prod")
	if prod == nil {
		t.Fatal("prod tickets nil")
	}

	if getHardcodedTickets("staging") != nil {
		t.Error("expected nil for unknown env")
	}
}
