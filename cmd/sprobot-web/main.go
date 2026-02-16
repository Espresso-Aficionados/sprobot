package main

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"

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
	ImageURL     string
}

var templates *template.Template

func main() {
	var err error
	templates, err = template.ParseFS(templateFS, "templates/base.html", "templates/index.html", "templates/profile.html", "templates/404.html")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	s3, err := s3client.New()
	if err != nil {
		log.Fatalf("Failed to create S3 client: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("GET /{bucket}/profiles/{guildID}/{templateName}/{userID}", handleProfile(s3))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Listening on :%s", port)
	if err := http.ListenAndServe(":"+port, accessLog(mux)); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

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

		// Strip .json suffix
		userID := userIDRaw
		if len(userID) > 5 && userID[len(userID)-5:] == ".json" {
			userID = userID[:len(userID)-5]
		}

		profile, err := s3.FetchProfileSimple(r.Context(), guildID, templateName, userID)
		if err != nil {
			render404(w)
			return
		}

		var fields []profileField
		var imageURL string
		for key, value := range profile {
			if key == sprobot.ImageField && value != "" {
				imageURL = value
			} else {
				fields = append(fields, profileField{Name: key, Value: value})
			}
		}

		renderPage(w, "profile.html", profileData{
			TemplateName: templateName,
			Fields:       fields,
			ImageURL:     imageURL,
		})
	}
}

func renderPage(w http.ResponseWriter, name string, data any) {
	// We need to execute base.html, which will invoke the blocks defined in the page template.
	// Since all templates are parsed together, the last-defined blocks win.
	// We need to re-parse for each page to get the right blocks.
	var t *template.Template
	var err error

	switch name {
	case "index.html":
		t, err = template.ParseFS(templateFS, "templates/base.html", "templates/index.html")
	case "profile.html":
		t, err = template.ParseFS(templateFS, "templates/base.html", "templates/profile.html")
	case "404.html":
		t, err = template.ParseFS(templateFS, "templates/base.html", "templates/404.html")
	default:
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if err != nil {
		log.Printf("Template parse error: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base.html", data); err != nil {
		log.Printf("Template execution error: %v", err)
	}
}

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
		log.Printf("%s %s %s %d %s %q", ip, r.Method, r.URL.RequestURI(), rec.status, time.Since(start), r.UserAgent())
	})
}

func render404(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	renderPage(w, "404.html", nil)
}
