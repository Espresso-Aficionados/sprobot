package main

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
