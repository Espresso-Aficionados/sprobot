package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sadbox/sprobot/pkg/s3client"
	"github.com/sadbox/sprobot/pkg/sprobot"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type profileField struct {
	Name  string
	Value string
}

type profileData struct {
	TemplateName string
	Fields       []profileField
	ImageURL     string // presigned, for the page's own <img>
	Preview      linkPreview
}

// linkPreview feeds the Open Graph tags Discord reads when unfurling a link.
// See https://docs.discord.com/developers/link-previews/overview.
type linkPreview struct {
	Title       string
	Description string
	ImageURL    string // non-expiring, Discord-only image route
	ImageAlt    string
}

// Discord trims longer values; truncating ourselves gives a clean ellipsis.
const (
	maxPreviewTitleBytes       = 70
	maxPreviewDescriptionBytes = 350
)

// fileData is the saved-file viewer page.
type fileData struct {
	Name        string
	Size        string
	Media       string // "image", "video", or "" for download only
	ViewURL     string // presigned, served inline as its own type
	DownloadURL string // presigned, served as an attachment
}

var pageTemplates map[string]*template.Template

func loadTemplates() error {
	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"toJSON": func(v any) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
	}

	pageTemplates = make(map[string]*template.Template)
	for _, name := range []string{"index.html", "profile.html", "file.html", "404.html"} {
		t, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/base.html", "templates/"+name)
		if err != nil {
			return fmt.Errorf("parsing template %s: %w", name, err)
		}
		pageTemplates[name] = t
	}
	return nil
}

func main() {
	if err := loadTemplates(); err != nil {
		log.Fatal(err)
	}

	s3, err := s3client.New()
	if err != nil {
		log.Fatalf("Failed to create S3 client: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("GET /"+sprobot.AvatarWebPath, handleAvatar)
	mux.HandleFunc("GET /profiles/{guildID}/{templateName}/{userID}", handleProfile(s3))
	mux.HandleFunc("GET /profiles/{guildID}/{templateName}/{userID}/{file}", handleProfileImage(s3))
	mux.HandleFunc("GET /files/{kind}/{guildID}/{name}", handleFile(s3))
	mux.HandleFunc("GET /files/{kind}/{guildID}/{name}/view", handleFileView(s3))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      securityHeaders(accessLog(mux)),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("Listening on :%s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server shutdown error: %v", err)
	}
}

// --- Public handlers ---

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		render404(w)
		return
	}
	renderPage(w, "index.html", nil)
}

func handleProfile(s3 *s3client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guildID")
		templateName := r.PathValue("templateName")
		userIDRaw := r.PathValue("userID")

		userID := strings.TrimSuffix(userIDRaw, ".json")

		profile, err := s3.FetchProfileSimple(r.Context(), guildID, templateName, userID)
		if err != nil {
			render404(w)
			return
		}

		renderPage(w, "profile.html", buildProfileData(r.Context(), s3, guildID, userID, lookupTemplate(templateName, profile), profile))
	}
}

func buildProfileData(ctx context.Context, s3 *s3client.Client, guildID, userID string, tmpl sprobot.Template, profile map[string]string) profileData {
	var fields []profileField
	var lines []string
	for _, f := range tmpl.Fields {
		value := profile[f.Name]
		if value == "" {
			continue
		}
		fields = append(fields, profileField{Name: f.Name, Value: value})
		lines = append(lines, f.Name+": "+value)
	}

	title := tmpl.Name
	if name := profile[sprobot.DisplayNameKey]; name != "" {
		title += " for " + name
	}

	return profileData{
		TemplateName: tmpl.Name,
		Fields:       fields,
		ImageURL:     s3.PresignExisting(ctx, profile[tmpl.Image.Name], s3client.PresignTTLPage),
		Preview: linkPreview{
			Title:       truncateBytes(title, maxPreviewTitleBytes),
			Description: truncateBytes(strings.Join(lines, "\n"), maxPreviewDescriptionBytes),
			ImageURL:    s3.ProfileImageURL(tmpl, guildID, userID, profile),
			ImageAlt:    tmpl.Image.Name,
		},
	}
}

// handleProfileImage serves a profile's image at a stable, non-expiring URL —
// but only to Discord, so embeds keep working without us becoming an image
// host. Anyone else is redirected to the profile page, whose image is a
// time-limited presigned URL; an <img> pointed here gets HTML and renders
// nothing.
func handleProfileImage(s3 *s3client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guildID")
		templateName := r.PathValue("templateName")
		userID := r.PathValue("userID")

		if !strings.HasPrefix(r.PathValue("file"), "image") {
			render404(w)
			return
		}

		// The response depends on the User-Agent; keep shared caches from mixing them up.
		w.Header().Set("Vary", "User-Agent")
		if !isDiscordFetcher(r.UserAgent()) {
			http.Redirect(w, r, "/"+sprobot.ProfileWebPath(guildID, templateName, userID), http.StatusFound)
			return
		}

		profile, err := s3.FetchProfileSimple(r.Context(), guildID, templateName, userID)
		if err != nil {
			render404(w)
			return
		}
		// The URL carries an image version, so a new upload gets a new URL.
		serveImageToDiscord(w, r, s3, profile[lookupTemplate(templateName, profile).Image.Name])
	}
}

// serveImageToDiscord streams a stored image. Only images are ever served
// this way, and the response is sandboxed so a mislabeled file can't run as
// a page on our origin.
func serveImageToDiscord(w http.ResponseWriter, r *http.Request, s3 *s3client.Client, urlOrKey string) {
	contentType := sprobot.InlineContentType(urlOrKey)
	if !strings.HasPrefix(contentType, "image/") {
		render404(w)
		return
	}

	body, size, err := s3.OpenImage(r.Context(), urlOrKey)
	if err != nil {
		if !errors.Is(err, s3client.ErrNotFound) {
			log.Printf("Image fetch error: %v", err)
		}
		render404(w)
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if _, err := io.Copy(w, body); err != nil {
		log.Printf("Image stream error: %v", err)
	}
}

// handleFile is the permanent link for a saved file. Everyone is redirected to
// the viewer page, except that Discord gets the bytes of shortcut and sticky
// images, which it has to fetch to show them in embeds. Mod files are only
// ever linked from embed fields, which Discord never unfurls, so they never
// need serving directly.
func handleFile(s3 *s3client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind, guildID, name := r.PathValue("kind"), r.PathValue("guildID"), r.PathValue("name")
		key, ok := sprobot.FileKey(kind, guildID, name)
		if !ok {
			render404(w)
			return
		}

		w.Header().Set("Vary", "User-Agent")
		embeddable := kind != "mod" && strings.HasPrefix(sprobot.InlineContentType(name), "image/")
		if !embeddable || !isDiscordFetcher(r.UserAgent()) {
			http.Redirect(w, r, "/"+sprobot.FileWebPath(kind, guildID, name)+"/view", http.StatusFound)
			return
		}
		// Saved-file names never change, so the bytes behind a URL never do.
		serveImageToDiscord(w, r, s3, key)
	}
}

// handleFileView renders a saved file behind short-lived presigned URLs, so
// the permanent link can't be used to hotlink or hand out the file directly.
func handleFileView(s3 *s3client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		key, ok := sprobot.FileKey(r.PathValue("kind"), r.PathValue("guildID"), name)
		if !ok {
			render404(w)
			return
		}

		size, err := s3.StatFile(r.Context(), key)
		if err != nil {
			if !errors.Is(err, s3client.ErrNotFound) {
				log.Printf("File stat error: %v", err)
			}
			render404(w)
			return
		}

		data := fileData{Name: name, Size: sprobot.FormatBytes(size)}
		download := `attachment; filename="` + name + `"` // name is validated to [A-Za-z0-9.]
		data.DownloadURL, err = s3.PresignFile(r.Context(), key, s3client.PresignTTLPage, "application/octet-stream", download)
		if err != nil {
			log.Printf("Presign error: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if contentType := sprobot.InlineContentType(name); contentType != "" {
			data.Media, _, _ = strings.Cut(contentType, "/")
			data.ViewURL, err = s3.PresignFile(r.Context(), key, s3client.PresignTTLPage, contentType, "inline")
			if err != nil {
				log.Printf("Presign error: %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
		}

		origin := s3.Origin()
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; img-src 'self' "+origin+"; media-src "+origin+";")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		w.Header().Set("Referrer-Policy", "no-referrer")
		renderPage(w, "file.html", data)
	}
}

func handleAvatar(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFileFS(w, r, staticFS, "static/sprobot.png")
}

// isDiscordFetcher matches Discord's link unfurler and media proxy. It is
// trivially spoofable; the point is that browsers can't spoof it from an
// <img> tag, which is what hotlinking needs.
func isDiscordFetcher(userAgent string) bool {
	ua := strings.ToLower(userAgent)
	return strings.Contains(ua, "discordbot") || strings.Contains(ua, "discordapp")
}

// lookupTemplate finds a hardcoded template by name. Profiles stored under a
// template that no longer exists fall back to rendering every stored field in
// key order.
func lookupTemplate(name string, profile map[string]string) sprobot.Template {
	for _, tmpls := range sprobot.AllTemplates() {
		for _, tmpl := range tmpls {
			if tmpl.Name == name {
				return tmpl
			}
		}
	}

	tmpl := sprobot.Template{Name: name, Image: sprobot.Field{Name: sprobot.ImageField}}
	keys := make([]string, 0, len(profile))
	for k := range profile {
		if k != tmpl.Image.Name && !strings.HasPrefix(k, "_") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		tmpl.Fields = append(tmpl.Fields, sprobot.Field{Name: k})
	}
	return tmpl
}

// truncateBytes shortens s to at most n bytes on a rune boundary, ending in an
// ellipsis when anything was cut.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	cut := n - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRightFunc(s[:cut], unicode.IsSpace) + ellipsis
}

// --- Rendering ---

func renderPage(w http.ResponseWriter, name string, data any) {
	t, ok := pageTemplates[name]
	if !ok {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base.html", data); err != nil {
		log.Printf("Template execution error: %v", err)
	}
}

func render404(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	renderPage(w, "404.html", nil)
}

// --- Middleware ---

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		ip := r.Header.Get("X-Forwarded-For")
		if ip == "" {
			ip = r.RemoteAddr
		}
		log.Printf("%s %s %s %d %s %q", sanitizeLog(ip), r.Method, sanitizeLog(r.URL.RequestURI()), rec.status, time.Since(start), sanitizeLog(r.UserAgent()))
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' https:;")
		next.ServeHTTP(w, r)
	})
}

// sanitizeLog replaces newlines and carriage returns to prevent log injection.
func sanitizeLog(s string) string {
	r := strings.NewReplacer("\n", "", "\r", "")
	return r.Replace(s)
}
