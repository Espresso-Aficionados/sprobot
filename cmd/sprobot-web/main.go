package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

var pageTemplates map[string]*template.Template

// --- OAuth2 / session types ---

type discordGuild struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Permissions int64  `json:"permissions,string"`
}

type discordUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Avatar   string `json:"avatar"`
}

type session struct {
	AccessToken string
	User        discordUser
	Guilds      []discordGuild
	ExpiresAt   time.Time
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*session)}
}

func (s *sessionStore) Get(id string) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok || time.Now().After(sess.ExpiresAt) {
		return nil, false
	}
	return sess, true
}

func (s *sessionStore) Set(id string, sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = sess
}

func (s *sessionStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// --- OAuth2 config ---

type oauthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	SecureCookie bool // false only in dev
}

func getOAuthConfig() *oauthConfig {
	clientID := os.Getenv("DISCORD_CLIENT_ID")
	clientSecret := os.Getenv("DISCORD_CLIENT_SECRET")
	redirectURI := os.Getenv("DISCORD_REDIRECT_URI")
	if clientID == "" || clientSecret == "" || redirectURI == "" {
		return nil
	}
	return &oauthConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURI:  redirectURI,
		SecureCookie: os.Getenv("SPROBOT_ENV") != "dev",
	}
}

// --- Discord API helpers ---

var oauthHTTPClient = &http.Client{Timeout: 10 * time.Second}

func exchangeCode(cfg *oauthConfig, code string) (string, error) {
	data := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURI},
	}

	resp, err := oauthHTTPClient.PostForm("https://discord.com/api/oauth2/token", data)
	if err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	return result.AccessToken, nil
}

func fetchDiscordUser(token string) (discordUser, error) {
	var u discordUser
	req, _ := http.NewRequest("GET", "https://discord.com/api/v10/users/@me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return u, fmt.Errorf("fetching user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return u, fmt.Errorf("user API returned %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return u, fmt.Errorf("decoding user: %w", err)
	}
	return u, nil
}

func fetchDiscordGuilds(authHeader string) ([]discordGuild, error) {
	req, _ := http.NewRequest("GET", "https://discord.com/api/v10/users/@me/guilds", nil)
	req.Header.Set("Authorization", authHeader)
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching guilds: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("guilds API returned %d", resp.StatusCode)
	}
	var guilds []discordGuild
	if err := json.NewDecoder(resp.Body).Decode(&guilds); err != nil {
		return nil, fmt.Errorf("decoding guilds: %w", err)
	}
	return guilds, nil
}

func randomSessionID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// isGuildAdmin checks ADMINISTRATOR (0x8) or MANAGE_GUILD (0x20).
func isGuildAdmin(perms int64) bool {
	return perms&0x8 != 0 || perms&0x20 != 0
}

// --- Template seeding ---

func seedTemplates(ctx context.Context, s3 *s3client.Client, env string) {
	templates := sprobot.AllTemplates(env)
	if templates == nil {
		return
	}

	for guildID, tmpls := range templates {
		guildStr := fmt.Sprintf("%d", guildID)
		_, err := s3.FetchTemplates(ctx, guildStr)
		if err == nil {
			continue // already seeded
		}

		data, err := json.Marshal(tmpls)
		if err != nil {
			log.Printf("Failed to marshal seed templates for guild %s: %v", guildStr, err)
			continue
		}
		if err := s3.SaveTemplates(ctx, guildStr, data); err != nil {
			log.Printf("Failed to seed templates for guild %s: %v", guildStr, err)
		} else {
			log.Printf("Seeded templates for guild %s", guildStr)
		}
	}
}

// --- Selfrole types (mirrored from pkg/bot for JSON) ---

type selfroleButton struct {
	Label  string `json:"label"`
	Emoji  string `json:"emoji"`
	RoleID uint64 `json:"role_id"`
}

type selfrolePanel struct {
	ChannelID uint64           `json:"channel_id"`
	Message   string           `json:"message"`
	Buttons   []selfroleButton `json:"buttons"`
}

type ticketWebConfig struct {
	ChannelID        uint64 `json:"channel_id"`
	StaffRoleID      uint64 `json:"staff_role_id"`
	CounterOffset    int    `json:"counter_offset"`
	PanelButtonLabel string `json:"panel_button_label"`
	PanelMessage     string `json:"panel_message"`
	TicketIntro      string `json:"ticket_intro"`
	CloseButtonLabel string `json:"close_button_label"`
}

func seedSelfroles(ctx context.Context, s3 *s3client.Client, env string) {
	configs := getHardcodedSelfroles(env)
	if configs == nil {
		return
	}

	for guildID, panels := range configs {
		_, err := s3.FetchSelfroles(ctx, guildID)
		if err == nil {
			continue
		}

		data, err := json.Marshal(panels)
		if err != nil {
			log.Printf("Failed to marshal seed selfroles for guild %s: %v", guildID, err)
			continue
		}
		if err := s3.SaveSelfroles(ctx, guildID, data); err != nil {
			log.Printf("Failed to seed selfroles for guild %s: %v", guildID, err)
		} else {
			log.Printf("Seeded selfroles for guild %s", guildID)
		}
	}
}

func seedTicketConfigs(ctx context.Context, s3 *s3client.Client, env string) {
	configs := getHardcodedTickets(env)
	if configs == nil {
		return
	}

	for guildID, cfg := range configs {
		_, err := s3.FetchTicketConfig(ctx, guildID)
		if err == nil {
			continue
		}

		data, err := json.Marshal(cfg)
		if err != nil {
			log.Printf("Failed to marshal seed ticket config for guild %s: %v", guildID, err)
			continue
		}
		if err := s3.SaveTicketConfig(ctx, guildID, data); err != nil {
			log.Printf("Failed to seed ticket config for guild %s: %v", guildID, err)
		} else {
			log.Printf("Seeded ticket config for guild %s", guildID)
		}
	}
}

func getHardcodedSelfroles(env string) map[string][]selfrolePanel {
	switch env {
	case "prod":
		return map[string][]selfrolePanel{
			"726985544038612993": {
				{
					ChannelID: 727325278820368456,
					Message:   "Want to share your pronouns? Clicking the buttons below will add a role that will allow other people to click your username and identify your pronouns! Please note that there is no need to share your pronouns if you don't want to for any reason.\n\n:one: \"Ask Me/Check Profile\"\n:two: \"They/them\"\n:three: \"She/her\"\n:four: \"He/him\"\n:five: \"It/its\"\n\nIf your chosen pronouns are not present and you would like them to be, please make a ticket to let us know. We do ask you to respect other people and not make a joke of pronouns here or in bot profiles.\n\nMade a mistake? Just click again to remove the role",
					Buttons: []selfroleButton{
						{Label: "Ask Me/Check Profile", Emoji: "1\ufe0f\u20e3", RoleID: 807495977362653214},
						{Label: "They/them", Emoji: "2\ufe0f\u20e3", RoleID: 807495948405178379},
						{Label: "She/her", Emoji: "3\ufe0f\u20e3", RoleID: 807495895499014165},
						{Label: "He/him", Emoji: "4\ufe0f\u20e3", RoleID: 807495784756936745},
						{Label: "It/its", Emoji: "5\ufe0f\u20e3", RoleID: 1088661493685432391},
					},
				},
				{
					ChannelID: 727325278820368456,
					Message:   "Are you excellent at dialing shots in? Do you know a lot about fixing espresso machines? Want to help people? Don't mind getting pings? Clicking the reaction below will add a role that will allow other people to request your help. You'll be able to be pinged via this role, and you'll get automatically pinged when a help thread hasn't been responded to in 24 hours.\n\nMade a mistake? Hate pings? Just click again to remove the role.",
					Buttons: []selfroleButton{
						{Label: "Helper", Emoji: "\U0001f527", RoleID: 1020401507121774722},
					},
				},
			},
		}
	case "dev":
		return map[string][]selfrolePanel{
			"1013566342345019512": {
				{
					ChannelID: 1019680095893471322,
					Message:   "Click a button below to toggle a role on or off.",
					Buttons: []selfroleButton{
						{Label: "BOTBROS", Emoji: "\U0001f916", RoleID: 1015493549430685706},
					},
				},
			},
		}
	default:
		return nil
	}
}

func getHardcodedTickets(env string) map[string]ticketWebConfig {
	introMessage := "Hello, %s! Thank you for contacting support.\nPlease describe your issue and wait for a response.\n\nWe've had a lot of questions about access to buying, selling, and trading recently. If this question is in regards to that topic, please see the answers below.\n\n**Marketplace Access FAQ:**\n**How do I (re)gain access to the Marketplace?**\nSimple - by interacting in the rest of the server. We are first and foremost a community server, not a buy/sell/trade server. As a matter of protecting members from fraudulent activity as well as philosophically, we only want people who engage in the server otherwise to have access.\n\n**How much does it take to (re)gain access?**\nFor what I should hope are fairly obvious reasons, we will not be revealing the exact parameters for gaining access so it is harder to game.\n\n**Will my access lapse if I don't interact in the server for some time?**\nNo - once you have access you will always have access, unless removed manually by staff."

	switch env {
	case "prod":
		return map[string]ticketWebConfig{
			"726985544038612993": {
				ChannelID:        733016849561944156,
				StaffRoleID:      738986689749450769,
				CounterOffset:    300,
				PanelButtonLabel: "Open Ticket",
				PanelMessage:     fmt.Sprintf("**Open a ticket!**\nClick the button below, and one of our <@&%d> will be with you shortly!\n\nQuestions regarding buy/sell/trade have been answered in <#%d> and <#%d>. We do not make exceptions for the policy and will not answer questions about specific requirements for access.\n\nPossible Reasons to open a ticket are:\n- Make a private suggestion to the @Staff about a way we can improve the server!\n- Get some help working out an issue you have with a server member.\n- Report a technical problem to the @Staff.\n- Any other issue that needs resolved by a member of our team.\n- Apply for the professional role. Please send a couple lines about your experience so we know more about you!\n\nPlease don't use the tickets for joke posts; we try to respond quickly to tickets so we'll get pulled away from something important to answer.", 738986689749450769, 727212292684644412, 727325278820368456),
				TicketIntro:      introMessage,
				CloseButtonLabel: "Close Ticket",
			},
		}
	case "dev":
		return map[string]ticketWebConfig{
			"1013566342345019512": {
				ChannelID:        1475318848956661921,
				StaffRoleID:      1015493549430685706,
				CounterOffset:    40,
				PanelButtonLabel: "Open Ticket",
				PanelMessage:     fmt.Sprintf("**Open a ticket!**\nClick the button below, and one of our <@&%d> will be with you shortly!\n\nQuestions regarding buy/sell/trade have been answered in <#%d> and <#%d>. We do not make exceptions for the policy and will not answer questions about specific requirements for access.\n\nPossible Reasons to open a ticket are:\n- Make a private suggestion to the @Staff about a way we can improve the server!\n- Get some help working out an issue you have with a server member.\n- Report a technical problem to the @Staff.\n- Any other issue that needs resolved by a member of our team.\n- Apply for the professional role. Please send a couple lines about your experience so we know more about you!\n\nPlease don't use the tickets for joke posts; we try to respond quickly to tickets so we'll get pulled away from something important to answer.", 1015493549430685706, 1019680095893471322, 1013566342865092671),
				TicketIntro:      introMessage,
				CloseButtonLabel: "Close Ticket",
			},
		}
	default:
		return nil
	}
}

// --- Main ---

func main() {
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

	adminTemplateNames := []string{"login.html", "admin_dashboard.html", "admin_guild.html", "admin_profiles.html", "admin_selfroles.html", "admin_tickets.html"}
	for _, name := range adminTemplateNames {
		t, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/base.html", "templates/"+name)
		if err != nil {
			log.Fatalf("Failed to parse admin template %s: %v", name, err)
		}
		pageTemplates[name] = t
	}

	s3, err := s3client.New()
	if err != nil {
		log.Fatalf("Failed to create S3 client: %v", err)
	}

	env := os.Getenv("SPROBOT_ENV")
	seedTemplates(context.Background(), s3, env)
	seedSelfroles(context.Background(), s3, env)
	seedTicketConfigs(context.Background(), s3, env)

	sessions := newSessionStore()
	oauth := getOAuthConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleIndex)
	mux.HandleFunc("GET /profiles/{guildID}/{templateName}/{userID}", handleProfile(s3))

	// Admin routes
	if oauth != nil {
		mux.HandleFunc("GET /admin/login", handleLogin(oauth))
		mux.HandleFunc("GET /auth/callback", handleCallback(oauth, sessions))
		mux.HandleFunc("GET /admin/logout", handleLogout(oauth, sessions))
		botToken := os.Getenv("SPROBOT_DISCORD_TOKEN")
		mux.HandleFunc("GET /admin/{$}", adminAuth(sessions, handleDashboard(botToken)))
		mux.HandleFunc("GET /admin/{guildID}/{$}", adminAuth(sessions, handleGuildHub()))
		mux.HandleFunc("GET /admin/{guildID}/profiles", adminAuth(sessions, handleAdminProfiles(s3)))
		mux.HandleFunc("POST /admin/{guildID}/profiles", adminAuth(sessions, handleSaveProfiles(s3)))
		mux.HandleFunc("GET /admin/{guildID}/selfroles", adminAuth(sessions, handleAdminSelfroles(s3)))
		mux.HandleFunc("POST /admin/{guildID}/selfroles", adminAuth(sessions, handleSaveSelfroles(s3)))
		mux.HandleFunc("GET /admin/{guildID}/tickets", adminAuth(sessions, handleAdminTickets(s3)))
		mux.HandleFunc("POST /admin/{guildID}/tickets", adminAuth(sessions, handleSaveTickets(s3)))
	} else {
		log.Println("DISCORD_CLIENT_ID/SECRET/REDIRECT_URI not set — admin routes disabled")
		adminDisabled := func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Admin dashboard is not configured. Set DISCORD_CLIENT_ID, DISCORD_CLIENT_SECRET, and DISCORD_REDIRECT_URI.", http.StatusServiceUnavailable)
		}
		mux.HandleFunc("GET /admin/{$}", adminDisabled)
		mux.HandleFunc("GET /admin/login", adminDisabled)
		mux.HandleFunc("GET /admin/{guildID}/profiles", adminDisabled)
	}

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

		// Load template config to identify the image field
		imageFieldName := sprobot.ImageField // default fallback
		tmplData, tmplErr := s3.FetchTemplates(r.Context(), guildID)
		if tmplErr == nil {
			var tmpls []sprobot.Template
			if json.Unmarshal(tmplData, &tmpls) == nil {
				for _, tmpl := range tmpls {
					if tmpl.Name == templateName {
						imageFieldName = tmpl.Image.Name
						break
					}
				}
			}
		}

		var fields []profileField
		var imageURL string
		keys := make([]string, 0, len(profile))
		for k := range profile {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := profile[key]
			if key == imageFieldName && value != "" {
				imageURL = s3.PresignExisting(r.Context(), value)
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

// --- Auth handlers ---

func handleLogin(cfg *oauthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		loginURL := fmt.Sprintf(
			"https://discord.com/api/oauth2/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=identify+guilds",
			cfg.ClientID,
			url.QueryEscape(cfg.RedirectURI),
		)
		renderPage(w, "login.html", struct{ LoginURL string }{loginURL})
	}
}

func handleCallback(cfg *oauthConfig, sessions *sessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		token, err := exchangeCode(cfg, code)
		if err != nil {
			log.Printf("OAuth2 exchange failed: %v", err)
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		user, err := fetchDiscordUser(token)
		if err != nil {
			log.Printf("Failed to fetch Discord user: %v", err)
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		guilds, err := fetchDiscordGuilds("Bearer " + token)
		if err != nil {
			log.Printf("Failed to fetch Discord guilds: %v", err)
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		sessionID := randomSessionID()
		sessions.Set(sessionID, &session{
			AccessToken: token,
			User:        user,
			Guilds:      guilds,
			ExpiresAt:   time.Now().Add(24 * time.Hour),
		})

		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    sessionID,
			Path:     "/",
			HttpOnly: true,
			Secure:   cfg.SecureCookie,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   86400,
		})

		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
	}
}

func handleLogout(cfg *oauthConfig, sessions *sessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err == nil {
			sessions.Delete(c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   cfg.SecureCookie,
			MaxAge:   -1,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// --- Admin middleware ---

type contextKey string

const sessionKey contextKey = "session"

func adminAuth(sessions *sessionStore, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session")
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		sess, ok := sessions.Get(c.Value)
		if !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		// If route has guildID, verify admin permissions
		guildIDStr := r.PathValue("guildID")
		if guildIDStr != "" {
			found := false
			for _, g := range sess.Guilds {
				if g.ID == guildIDStr && isGuildAdmin(g.Permissions) {
					found = true
					break
				}
			}
			if !found {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}

		ctx := context.WithValue(r.Context(), sessionKey, sess)
		next(w, r.WithContext(ctx))
	}
}

func getSession(r *http.Request) *session {
	sess, _ := r.Context().Value(sessionKey).(*session)
	return sess
}

// --- Admin handlers ---

func handleDashboard(botToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := getSession(r)
		if sess == nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}

		// Fetch guilds the bot is in via Discord API.
		botGuilds := make(map[string]bool)
		if botToken != "" {
			guilds, err := fetchDiscordGuilds("Bot " + botToken)
			if err != nil {
				log.Printf("Failed to fetch bot guilds: %v", err)
			} else {
				for _, g := range guilds {
					botGuilds[g.ID] = true
				}
			}
		}

		type guildInfo struct {
			ID   string
			Name string
		}
		var guilds []guildInfo
		for _, g := range sess.Guilds {
			if isGuildAdmin(g.Permissions) && botGuilds[g.ID] {
				guilds = append(guilds, guildInfo{ID: g.ID, Name: g.Name})
			}
		}

		renderPage(w, "admin_dashboard.html", struct {
			Guilds []guildInfo
		}{guilds})
	}
}

func handleAdminProfiles(s3 *s3client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guildID")

		var templates []sprout
		data, err := s3.FetchTemplates(r.Context(), guildID)
		if err == nil {
			json.Unmarshal(data, &templates)
		}

		success := r.URL.Query().Get("saved") == "1"
		errMsg := r.URL.Query().Get("error")

		renderAdminPage(w, "admin_profiles.html", struct {
			GuildID   string
			Templates []sprout
			Success   bool
			Error     string
		}{guildID, templates, success, errMsg})
	}
}

// sprout is a local type alias to avoid template rendering issues with sprobot.TextStyle.
type sprout = sprobot.Template

var shortNameRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func handleSaveProfiles(s3 *s3client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guildID")

		if err := r.ParseForm(); err != nil {
			redirectProfilesError(w, r, guildID, "Invalid form data.")
			return
		}

		var templates []sprobot.Template
		seenShortNames := make(map[string]bool)

		for i := 0; ; i++ {
			prefix := fmt.Sprintf("tmpl_%d_", i)
			name := strings.TrimSpace(r.FormValue(prefix + "name"))
			if name == "" {
				break
			}

			shortName := strings.TrimSpace(r.FormValue(prefix + "shortname"))
			description := strings.TrimSpace(r.FormValue(prefix + "description"))

			if !shortNameRegex.MatchString(shortName) {
				redirectProfilesError(w, r, guildID, fmt.Sprintf("Invalid short name %q. Must be lowercase letters/numbers/hyphens, starting with a letter.", shortName))
				return
			}
			if seenShortNames[shortName] {
				redirectProfilesError(w, r, guildID, fmt.Sprintf("Duplicate short name %q.", shortName))
				return
			}
			seenShortNames[shortName] = true

			if description == "" {
				redirectProfilesError(w, r, guildID, fmt.Sprintf("Description is required for template %q.", name))
				return
			}

			var fields []sprobot.Field
			for j := 0; j < 4; j++ {
				fPrefix := fmt.Sprintf("%sfield_%d_", prefix, j)
				fName := strings.TrimSpace(r.FormValue(fPrefix + "name"))
				if fName == "" {
					continue
				}
				fPlaceholder := strings.TrimSpace(r.FormValue(fPrefix + "placeholder"))
				fStyleStr := r.FormValue(fPrefix + "style")
				fStyle := sprobot.TextStyleShort
				if fStyleStr == "1" {
					fStyle = sprobot.TextStyleLong
				}
				fields = append(fields, sprobot.Field{
					Name:        fName,
					Placeholder: fPlaceholder,
					Style:       fStyle,
				})
			}

			if len(fields) == 0 {
				redirectProfilesError(w, r, guildID, fmt.Sprintf("Template %q must have at least 1 field.", name))
				return
			}
			if len(fields) > 4 {
				redirectProfilesError(w, r, guildID, fmt.Sprintf("Template %q has too many fields (max 4).", name))
				return
			}

			imageName := strings.TrimSpace(r.FormValue(prefix + "image_name"))
			imagePlaceholder := strings.TrimSpace(r.FormValue(prefix + "image_placeholder"))
			if imageName == "" {
				imageName = "Gear Picture"
			}

			templates = append(templates, sprobot.Template{
				Name:        name,
				ShortName:   shortName,
				Description: description,
				Fields:      fields,
				Image: sprobot.Field{
					Name:        imageName,
					Placeholder: imagePlaceholder,
					Style:       sprobot.TextStyleShort,
				},
			})
		}

		if len(templates) == 0 {
			redirectProfilesError(w, r, guildID, "At least one template is required.")
			return
		}

		data, err := json.Marshal(templates)
		if err != nil {
			redirectProfilesError(w, r, guildID, "Failed to encode templates.")
			return
		}

		if err := s3.SaveTemplates(r.Context(), guildID, data); err != nil {
			log.Printf("Failed to save templates for guild %s: %v", guildID, err)
			redirectProfilesError(w, r, guildID, "Failed to save templates.")
			return
		}

		log.Printf("Templates saved for guild %s by admin", guildID)
		http.Redirect(w, r, fmt.Sprintf("/admin/%s/profiles?saved=1", guildID), http.StatusSeeOther)
	}
}

func redirectProfilesError(w http.ResponseWriter, r *http.Request, guildID, msg string) {
	http.Redirect(w, r, fmt.Sprintf("/admin/%s/profiles?error=%s", guildID, url.QueryEscape(msg)), http.StatusSeeOther)
}

// --- Guild hub handler ---

func handleGuildHub() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guildID")
		renderAdminPage(w, "admin_guild.html", struct {
			GuildID string
		}{guildID})
	}
}

// --- Selfrole handlers ---

func handleAdminSelfroles(s3 *s3client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guildID")

		var panels []selfrolePanel
		data, err := s3.FetchSelfroles(r.Context(), guildID)
		if err == nil {
			json.Unmarshal(data, &panels)
		}

		success := r.URL.Query().Get("saved") == "1"
		errMsg := r.URL.Query().Get("error")

		renderAdminPage(w, "admin_selfroles.html", struct {
			GuildID string
			Panels  []selfrolePanel
			Success bool
			Error   string
		}{guildID, panels, success, errMsg})
	}
}

func handleSaveSelfroles(s3 *s3client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guildID")

		if err := r.ParseForm(); err != nil {
			redirectSelfroleError(w, r, guildID, "Invalid form data.")
			return
		}

		var panels []selfrolePanel
		for i := 0; ; i++ {
			prefix := fmt.Sprintf("panel_%d_", i)
			channelIDStr := strings.TrimSpace(r.FormValue(prefix + "channel_id"))
			if channelIDStr == "" {
				break
			}

			channelID, err := strconv.ParseUint(channelIDStr, 10, 64)
			if err != nil || channelID == 0 {
				redirectSelfroleError(w, r, guildID, fmt.Sprintf("Invalid Channel ID for panel %d.", i+1))
				return
			}

			message := strings.TrimSpace(r.FormValue(prefix + "message"))
			if message == "" {
				redirectSelfroleError(w, r, guildID, fmt.Sprintf("Message is required for panel %d.", i+1))
				return
			}

			var buttons []selfroleButton
			for j := 0; j < 5; j++ {
				bPrefix := fmt.Sprintf("%sbtn_%d_", prefix, j)
				label := strings.TrimSpace(r.FormValue(bPrefix + "label"))
				if label == "" {
					continue
				}
				emoji := strings.TrimSpace(r.FormValue(bPrefix + "emoji"))
				roleIDStr := strings.TrimSpace(r.FormValue(bPrefix + "role_id"))
				roleID, err := strconv.ParseUint(roleIDStr, 10, 64)
				if err != nil || roleID == 0 {
					redirectSelfroleError(w, r, guildID, fmt.Sprintf("Invalid Role ID for panel %d, button %d.", i+1, j+1))
					return
				}
				buttons = append(buttons, selfroleButton{
					Label:  label,
					Emoji:  emoji,
					RoleID: roleID,
				})
			}

			if len(buttons) == 0 {
				redirectSelfroleError(w, r, guildID, fmt.Sprintf("Panel %d must have at least 1 button.", i+1))
				return
			}
			if len(buttons) > 5 {
				redirectSelfroleError(w, r, guildID, fmt.Sprintf("Panel %d has too many buttons (max 5).", i+1))
				return
			}

			panels = append(panels, selfrolePanel{
				ChannelID: channelID,
				Message:   message,
				Buttons:   buttons,
			})
		}

		if len(panels) == 0 {
			redirectSelfroleError(w, r, guildID, "At least one panel is required.")
			return
		}

		data, err := json.Marshal(panels)
		if err != nil {
			redirectSelfroleError(w, r, guildID, "Failed to encode selfroles.")
			return
		}

		if err := s3.SaveSelfroles(r.Context(), guildID, data); err != nil {
			log.Printf("Failed to save selfroles for guild %s: %v", guildID, err)
			redirectSelfroleError(w, r, guildID, "Failed to save selfroles.")
			return
		}

		log.Printf("Selfroles saved for guild %s by admin", guildID)
		http.Redirect(w, r, fmt.Sprintf("/admin/%s/selfroles?saved=1", guildID), http.StatusSeeOther)
	}
}

func redirectSelfroleError(w http.ResponseWriter, r *http.Request, guildID, msg string) {
	http.Redirect(w, r, fmt.Sprintf("/admin/%s/selfroles?error=%s", guildID, url.QueryEscape(msg)), http.StatusSeeOther)
}

// --- Ticket handlers ---

func handleAdminTickets(s3 *s3client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guildID")

		var cfg ticketWebConfig
		hasConfig := false
		data, err := s3.FetchTicketConfig(r.Context(), guildID)
		if err == nil {
			if json.Unmarshal(data, &cfg) == nil && cfg.ChannelID != 0 {
				hasConfig = true
			}
		}

		success := r.URL.Query().Get("saved") == "1"
		errMsg := r.URL.Query().Get("error")

		renderAdminPage(w, "admin_tickets.html", struct {
			GuildID   string
			Config    ticketWebConfig
			HasConfig bool
			Success   bool
			Error     string
		}{guildID, cfg, hasConfig, success, errMsg})
	}
}

func handleSaveTickets(s3 *s3client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		guildID := r.PathValue("guildID")

		if err := r.ParseForm(); err != nil {
			redirectTicketError(w, r, guildID, "Invalid form data.")
			return
		}

		// Handle delete action
		if r.FormValue("action") == "delete" {
			// Save an empty config (zero ChannelID signals no config)
			data, _ := json.Marshal(ticketWebConfig{})
			if err := s3.SaveTicketConfig(r.Context(), guildID, data); err != nil {
				log.Printf("Failed to delete ticket config for guild %s: %v", guildID, err)
				redirectTicketError(w, r, guildID, "Failed to remove configuration.")
				return
			}
			log.Printf("Ticket config removed for guild %s by admin", guildID)
			http.Redirect(w, r, fmt.Sprintf("/admin/%s/tickets?saved=1", guildID), http.StatusSeeOther)
			return
		}

		channelIDStr := strings.TrimSpace(r.FormValue("channel_id"))
		channelID, err := strconv.ParseUint(channelIDStr, 10, 64)
		if err != nil || channelID == 0 {
			redirectTicketError(w, r, guildID, "Invalid Channel ID.")
			return
		}

		staffRoleIDStr := strings.TrimSpace(r.FormValue("staff_role_id"))
		staffRoleID, err := strconv.ParseUint(staffRoleIDStr, 10, 64)
		if err != nil || staffRoleID == 0 {
			redirectTicketError(w, r, guildID, "Invalid Staff Role ID.")
			return
		}

		counterOffset := intFromForm(r.FormValue("counter_offset"))
		if counterOffset < 0 {
			counterOffset = 0
		}

		panelButtonLabel := strings.TrimSpace(r.FormValue("panel_button_label"))
		if panelButtonLabel == "" {
			redirectTicketError(w, r, guildID, "Panel Button Label is required.")
			return
		}

		panelMessage := strings.TrimSpace(r.FormValue("panel_message"))
		if panelMessage == "" {
			redirectTicketError(w, r, guildID, "Panel Message is required.")
			return
		}

		ticketIntro := strings.TrimSpace(r.FormValue("ticket_intro"))
		if ticketIntro == "" {
			redirectTicketError(w, r, guildID, "Ticket Intro is required.")
			return
		}

		closeButtonLabel := strings.TrimSpace(r.FormValue("close_button_label"))
		if closeButtonLabel == "" {
			redirectTicketError(w, r, guildID, "Close Button Label is required.")
			return
		}

		cfg := ticketWebConfig{
			ChannelID:        channelID,
			StaffRoleID:      staffRoleID,
			CounterOffset:    counterOffset,
			PanelButtonLabel: panelButtonLabel,
			PanelMessage:     panelMessage,
			TicketIntro:      ticketIntro,
			CloseButtonLabel: closeButtonLabel,
		}

		data, err := json.Marshal(cfg)
		if err != nil {
			redirectTicketError(w, r, guildID, "Failed to encode ticket config.")
			return
		}

		if err := s3.SaveTicketConfig(r.Context(), guildID, data); err != nil {
			log.Printf("Failed to save ticket config for guild %s: %v", guildID, err)
			redirectTicketError(w, r, guildID, "Failed to save ticket config.")
			return
		}

		log.Printf("Ticket config saved for guild %s by admin", guildID)
		http.Redirect(w, r, fmt.Sprintf("/admin/%s/tickets?saved=1", guildID), http.StatusSeeOther)
	}
}

func redirectTicketError(w http.ResponseWriter, r *http.Request, guildID, msg string) {
	http.Redirect(w, r, fmt.Sprintf("/admin/%s/tickets?error=%s", guildID, url.QueryEscape(msg)), http.StatusSeeOther)
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

func renderAdminPage(w http.ResponseWriter, name string, data any) {
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
		if strings.HasPrefix(r.URL.Path, "/admin/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src 'self' https:; form-action 'self';")
		} else {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' https:;")
		}
		next.ServeHTTP(w, r)
	})
}

// sanitizeLog replaces newlines and carriage returns to prevent log injection.
func sanitizeLog(s string) string {
	r := strings.NewReplacer("\n", "", "\r", "")
	return r.Replace(s)
}

func render404(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	renderPage(w, "404.html", nil)
}

// intFromForm parses a form value as int, returning 0 on failure.
func intFromForm(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
