package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path"
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

var pageTemplates map[string]*template.Template

func main() {
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

	s3, err := s3client.New()
	if err != nil {
		log.Fatalf("Failed to create S3 client: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("GET /profiles/{guildID}/{templateName}/{userID}", handleProfile(s3))
	mux.HandleFunc("GET /profiles/{guildID}/{templateName}/{userID}/{file}", handleProfileImage(s3))

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
	for _, f := range tmpl.Fields {
		value := profile[f.Name]
		if value == "" {
			continue
		}
		fields = append(fields, profileField{Name: f.Name, Value: value})
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
			Description: previewDescription(fields, maxPreviewDescriptionBytes),
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
		value := profile[lookupTemplate(templateName, profile).Image.Name]
		contentType := mime.TypeByExtension(path.Ext(value))
		if !strings.HasPrefix(contentType, "image/") {
			render404(w)
			return
		}

		body, size, err := s3.OpenImage(r.Context(), value)
		if err != nil {
			if !errors.Is(err, s3client.ErrNotFound) {
				log.Printf("Image fetch error: %v", err)
			}
			render404(w)
			return
		}
		defer body.Close()

		w.Header().Set("Content-Type", contentType)
		if size > 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		}
		// The URL carries an image version, so a new upload gets a new URL.
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if _, err := io.Copy(w, body); err != nil {
			log.Printf("Image stream error: %v", err)
		}
	}
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

// previewDescription renders fields as Discord markdown (bold field names, a
// blank line between fields) within n bytes. Fields that don't fit are
// dropped; the field that crosses the limit is cut short with an ellipsis.
// Truncation happens on the raw value, before escaping, so a cut never splits
// an escape sequence or leaves a dangling "**".
func previewDescription(fields []profileField, n int) string {
	// Below this, a truncated value isn't worth showing.
	const minValueBytes = 16

	var b strings.Builder
	for i, f := range fields {
		header := "**" + escapeMarkdown(f.Name) + "**\n"
		if i > 0 {
			header = "\n\n" + header
		}
		room := n - b.Len() - len(header)
		if room < minValueBytes {
			break
		}
		value := escapeMarkdown(f.Value)
		truncated := len(value) > room
		if truncated {
			value = truncateEscaped(f.Value, room)
		}
		b.WriteString(header)
		b.WriteString(value)
		if truncated {
			break
		}
	}
	return b.String()
}

// truncateEscaped returns escapeMarkdown of the longest prefix of raw (plus an
// ellipsis) whose escaped form fits in n bytes.
func truncateEscaped(raw string, n int) string {
	// Escaping can double a value's length, so binary-search the raw cut point.
	best := ""
	lo, hi := 0, min(n, len(raw))
	for lo <= hi {
		mid := (lo + hi) / 2
		if s := escapeMarkdown(truncateBytes(raw, mid)); len(s) <= n {
			best, lo = s, mid+1
		} else {
			hi = mid - 1
		}
	}
	return best
}

// markdownEscaper backslash-escapes the characters Discord markdown treats as
// syntax: emphasis, code, spoilers, quotes, headings, lists, masked links, and
// <@mention>/<t:timestamp> tags. Discord drops a backslash before any
// punctuation, so over-escaping is harmless.
var markdownEscaper = strings.NewReplacer(
	`\`, `\\`, `*`, `\*`, `_`, `\_`, "`", "\\`", `~`, `\~`, `|`, `\|`,
	`>`, `\>`, `<`, `\<`, `#`, `\#`, `-`, `\-`, `[`, `\[`, `]`, `\]`,
	`(`, `\(`, `)`, `\)`,
)

// escapeMarkdown makes user text render literally in a rich Discord preview.
// Bare URLs are left intact so Discord still autolinks them.
func escapeMarkdown(s string) string {
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		for j, word := range strings.Split(line, " ") {
			if j > 0 {
				b.WriteByte(' ')
			}
			if strings.HasPrefix(word, "http://") || strings.HasPrefix(word, "https://") {
				b.WriteString(word)
			} else {
				b.WriteString(markdownEscaper.Replace(word))
			}
		}
	}
	return b.String()
}

// truncateBytes shortens s to at most n bytes on a rune boundary, ending in an
// ellipsis when anything was cut.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	cut := max(n-len(ellipsis), 0)
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
