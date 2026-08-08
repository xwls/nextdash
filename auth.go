package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	sessionCookieName  = "nextdash_session"
	sessionIdleTTL     = 12 * time.Hour
	sessionAbsoluteTTL = 7 * 24 * time.Hour
	loginFailureLimit  = 5
	loginFailureWindow = 5 * time.Minute
)

type authConfig struct {
	username     string
	passwordHash argon2PasswordHash
	cookieSecure bool
}

func loadAuthConfigFromEnv() (authConfig, error) {
	username := strings.TrimSpace(os.Getenv("NEXTDASH_ADMIN_USERNAME"))
	if username == "" {
		username = "admin"
	}
	rawHash := strings.TrimSpace(os.Getenv("NEXTDASH_ADMIN_PASSWORD_HASH"))
	if rawHash == "" {
		return authConfig{}, errors.New("NEXTDASH_ADMIN_PASSWORD_HASH is required; run 'nextdash hash-password' to generate one")
	}
	parsed, err := parseArgon2idPHC(rawHash)
	if err != nil {
		return authConfig{}, fmt.Errorf("invalid NEXTDASH_ADMIN_PASSWORD_HASH: %w", err)
	}
	secure := true
	switch raw := strings.TrimSpace(os.Getenv("NEXTDASH_AUTH_COOKIE_SECURE")); raw {
	case "", "1":
	case "0":
		secure = false
	default:
		return authConfig{}, errors.New("NEXTDASH_AUTH_COOKIE_SECURE must be 0 or 1")
	}
	return authConfig{username: username, passwordHash: parsed, cookieSecure: secure}, nil
}

type sessionRecord struct {
	ID              string
	CSRFToken       string
	CreatedAt       time.Time
	IdleExpiresAt   time.Time
	AbsoluteExpires time.Time
}

type sessionManager struct {
	mu       sync.Mutex
	sessions map[string]sessionRecord
	now      func() time.Time
}

func newSessionManager() *sessionManager {
	return &sessionManager{sessions: make(map[string]sessionRecord), now: time.Now}
}

func randomOpaqueToken(byteLength int) (string, error) {
	buf := make([]byte, byteLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (m *sessionManager) create() (sessionRecord, error) {
	id, err := randomOpaqueToken(32)
	if err != nil {
		return sessionRecord{}, err
	}
	csrf, err := randomOpaqueToken(32)
	if err != nil {
		return sessionRecord{}, err
	}
	now := m.now()
	record := sessionRecord{
		ID:              id,
		CSRFToken:       csrf,
		CreatedAt:       now,
		IdleExpiresAt:   now.Add(sessionIdleTTL),
		AbsoluteExpires: now.Add(sessionAbsoluteTTL),
	}
	m.mu.Lock()
	m.sessions[id] = record
	m.mu.Unlock()
	return record, nil
}

func (m *sessionManager) get(id string, refresh bool) (sessionRecord, bool) {
	if id == "" {
		return sessionRecord{}, false
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.sessions[id]
	if !ok {
		return sessionRecord{}, false
	}
	if !now.Before(record.IdleExpiresAt) || !now.Before(record.AbsoluteExpires) {
		delete(m.sessions, id)
		return sessionRecord{}, false
	}
	if refresh {
		record.IdleExpiresAt = now.Add(sessionIdleTTL)
		if record.IdleExpiresAt.After(record.AbsoluteExpires) {
			record.IdleExpiresAt = record.AbsoluteExpires
		}
		m.sessions[id] = record
	}
	return record, true
}

func (m *sessionManager) delete(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

func (m *sessionManager) cleanupExpired() {
	now := m.now()
	m.mu.Lock()
	for id, record := range m.sessions {
		if !now.Before(record.IdleExpiresAt) || !now.Before(record.AbsoluteExpires) {
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
}

func (m *sessionManager) startCleanup(stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.cleanupExpired()
			case <-stop:
				return
			}
		}
	}()
}

type loginFailureState struct {
	mu     sync.Mutex
	events map[string][]time.Time
	now    func() time.Time
	limit  int
	window time.Duration
}

func newLoginFailureState() *loginFailureState {
	return &loginFailureState{events: make(map[string][]time.Time), now: time.Now, limit: loginFailureLimit, window: loginFailureWindow}
}

func (l *loginFailureState) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-l.window)
	list := l.events[key]
	kept := list[:0]
	for _, event := range list {
		if event.After(cutoff) {
			kept = append(kept, event)
		}
	}
	l.events[key] = kept
	return len(kept) >= l.limit
}

func (l *loginFailureState) fail(key string) {
	l.mu.Lock()
	l.events[key] = append(l.events[key], l.now())
	l.mu.Unlock()
}

func (l *loginFailureState) clear(key string) {
	l.mu.Lock()
	delete(l.events, key)
	l.mu.Unlock()
}

type authContextKey struct{}

type requestAuth struct {
	method  string
	session sessionRecord
}

func authFromContext(ctx context.Context) (requestAuth, bool) {
	value, ok := ctx.Value(authContextKey{}).(requestAuth)
	return value, ok
}

func csrfTokenFromRequest(r *http.Request) string {
	if auth, ok := authFromContext(r.Context()); ok && auth.method == "session" {
		return auth.session.CSRFToken
	}
	return ""
}

type loginThemeStore interface {
	GetSettings() Settings
	GetColors() ColorTheme
}

type authService struct {
	config         authConfig
	sessions       *sessionManager
	failures       *loginFailureState
	files          embed.FS
	themeStore     loginThemeStore
	passwordChecks chan struct{}
}

func newAuthService(config authConfig, files embed.FS, themeStore loginThemeStore) *authService {
	return &authService{
		config:         config,
		sessions:       newSessionManager(),
		failures:       newLoginFailureState(),
		files:          files,
		themeStore:     themeStore,
		passwordChecks: make(chan struct{}, 1),
	}
}

func constantTimeStringEqual(a, b string) bool {
	aHash := sha256.Sum256([]byte(a))
	bHash := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aHash[:], bHash[:]) == 1
}

func (a *authService) verifyCredentials(username, password string) bool {
	// Argon2 intentionally consumes substantial memory. Serialize checks so concurrent
	// login attempts cannot multiply memory use and exhaust a small container.
	a.passwordChecks <- struct{}{}
	defer func() { <-a.passwordChecks }()

	usernameOK := constantTimeStringEqual(username, a.config.username)
	passwordOK := verifyAdminPassword(a.config.passwordHash, []byte(password))
	return usernameOK && passwordOK
}

func remoteAddressIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func loginFailureKey(r *http.Request, username string) string {
	return remoteAddressIP(r) + "\x00" + strings.ToLower(strings.TrimSpace(username))
}

type loginPageData struct {
	Username           string
	Next               string
	Error              string
	Theme              string
	AutoDarkMode       bool
	RandomThemeMode    string
	ThemePoolCSV       string
	CustomThemeIDsCSV  string
	ShowBackgroundDots bool
	ThemeColorMeta     string
}

func (a *authService) withLoginTheme(data loginPageData) loginPageData {
	settings := Settings{
		Theme:              "dark",
		RandomThemeMode:    "off",
		ShowBackgroundDots: true,
	}
	colors := getDefaultColors()
	if a.themeStore != nil {
		settings = a.themeStore.GetSettings()
		colors = a.themeStore.GetColors()
	}

	themeID := normalizeLegacyThemeID(settings.Theme)
	data.Theme = themeID
	data.AutoDarkMode = settings.AutoDarkMode
	data.RandomThemeMode = normalizeRandomThemeMode(settings.RandomThemeMode, settings.RandomThemeOnRefresh)
	data.ThemePoolCSV = themePoolCSV(colors)
	data.CustomThemeIDsCSV = customThemeIDsCSV(colors)
	data.ShowBackgroundDots = settings.ShowBackgroundDots
	data.ThemeColorMeta = themeBackgroundPrimary(themeID, colors)
	return data
}

func (a *authService) parseLoginTemplate() (*template.Template, error) {
	if info, err := os.Stat("templates"); err == nil && info.IsDir() {
		return template.ParseFiles(filepath.FromSlash("templates/login.html"))
	}
	return template.ParseFS(a.files, "templates/login.html")
}

func (a *authService) renderLogin(w http.ResponseWriter, status int, data loginPageData) {
	data = a.withLoginTheme(data)
	tmpl, err := a.parseLoginTemplate()
	if err != nil {
		http.Error(w, "Template parsing error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Template execution error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func safeNextPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, "\\") || strings.ContainsAny(raw, "\r\n\x00") {
		return "/"
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil {
		return "/"
	}
	decoded, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || strings.HasPrefix(decoded, "//") || strings.Contains(decoded, "\\") || strings.Contains(decoded, "%") {
		return "/"
	}
	if parsed.Path == "/login" || parsed.Path == "/logout" {
		return "/"
	}
	return raw
}

func (a *authService) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			if _, ok := a.sessions.get(cookie.Value, true); ok {
				http.Redirect(w, r, safeNextPath(r.URL.Query().Get("next")), http.StatusFound)
				return
			}
		}
		a.renderLogin(w, http.StatusOK, loginPageData{Next: safeNextPath(r.URL.Query().Get("next"))})
		return
	}
	if err := r.ParseForm(); err != nil {
		a.renderLogin(w, http.StatusBadRequest, loginPageData{Next: "/", Error: "用户名或密码错误"})
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	next := safeNextPath(r.FormValue("next"))
	key := loginFailureKey(r, username)
	if a.failures.blocked(key) {
		logRateLimitHit(r, "/login")
		w.Header().Set("Retry-After", "300")
		a.renderLogin(w, http.StatusTooManyRequests, loginPageData{Username: username, Next: next, Error: "登录尝试过多，请稍后再试"})
		return
	}
	if !a.verifyCredentials(username, r.FormValue("password")) {
		a.failures.fail(key)
		logAuthDenied(r, "invalid_admin_credentials")
		a.renderLogin(w, http.StatusUnauthorized, loginPageData{Username: username, Next: next, Error: "用户名或密码错误"})
		return
	}
	a.failures.clear(key)
	session, err := a.sessions.create()
	if err != nil {
		http.Error(w, "Unable to create session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.config.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *authService) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		a.sessions.delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.config.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *authService) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func isPublicRoute(r *http.Request) bool {
	path := r.URL.Path
	if r.Method == http.MethodOptions {
		return true
	}
	if path == "/login" || path == "/healthz" || path == "/manifest.webmanifest" || path == "/push-service-worker.js" {
		return true
	}
	if path == "/api/theme.css" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		return true
	}
	return strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/locales/") || strings.HasPrefix(path, "/data/")
}

func extensionRouteAllowed(r *http.Request) bool {
	switch r.Method + " " + r.URL.Path {
	case "GET /api/pages", "GET /api/categories", "GET /api/bookmarks", "POST /api/bookmarks/add", "POST /api/inbox", "GET /api/bookmark-preview", "POST /api/icon/from-url":
		return true
	default:
		return false
	}
}

func validWriteTokenFromRequest(r *http.Request) bool {
	expected := writeAccessToken()
	provided := strings.TrimSpace(r.Header.Get("X-NextDash-Token"))
	return expected != "" && provided != "" && constantTimeStringEqual(provided, expected)
}

func requestExpectedOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

func sameOriginRequest(r *http.Request) bool {
	expected := requestExpectedOrigin(r)
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		return err == nil && parsed.Scheme != "" && parsed.Host != "" && strings.EqualFold(parsed.Scheme+"://"+parsed.Host, expected)
	}
	if referer := strings.TrimSpace(r.Referer()); referer != "" {
		parsed, err := url.Parse(referer)
		return err == nil && parsed.Scheme != "" && parsed.Host != "" && strings.EqualFold(parsed.Scheme+"://"+parsed.Host, expected)
	}
	return false
}

func unsafeHTTPMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func isHTMLNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/version" {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html") || !strings.Contains(r.URL.Path, ".")
}

func loginRedirectTarget(r *http.Request) string {
	next := safeNextPath(r.URL.RequestURI())
	return "/login?next=" + url.QueryEscape(next)
}

func (a *authService) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicRoute(r) {
			next.ServeHTTP(w, r)
			return
		}
		if extensionRouteAllowed(r) {
			applyCORSHeaders(w, r)
			if validWriteTokenFromRequest(r) {
				ctx := context.WithValue(r.Context(), authContextKey{}, requestAuth{method: "extension"})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err == nil {
			if session, ok := a.sessions.get(cookie.Value, false); ok {
				if unsafeHTTPMethod(r.Method) {
					if !sameOriginRequest(r) {
						logAuthDenied(r, "csrf_cross_origin")
						http.Error(w, "Forbidden", http.StatusForbidden)
						return
					}
					provided := strings.TrimSpace(r.Header.Get("X-NextDash-CSRF"))
					if provided == "" || !constantTimeStringEqual(provided, session.CSRFToken) {
						logAuthDenied(r, "csrf_invalid")
						http.Error(w, "Forbidden", http.StatusForbidden)
						return
					}
				}
				refreshed, ok := a.sessions.get(cookie.Value, true)
				if !ok {
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				ctx := context.WithValue(r.Context(), authContextKey{}, requestAuth{method: "session", session: refreshed})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		logAuthDenied(r, "session_required")
		w.Header().Set("Cache-Control", "no-store")
		if isHTMLNavigation(r) {
			http.Redirect(w, r, loginRedirectTarget(r), http.StatusFound)
			return
		}
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

func runHashPasswordCommand() error {
	fmt.Fprint(os.Stderr, "Password: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	defer func() {
		for i := range first {
			first[i] = 0
		}
	}()
	fmt.Fprint(os.Stderr, "Confirm password: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("read password confirmation: %w", err)
	}
	defer func() {
		for i := range second {
			second[i] = 0
		}
	}()
	if len(first) == 0 {
		return errors.New("password must not be empty")
	}
	if subtle.ConstantTimeCompare(first, second) != 1 {
		return errors.New("passwords do not match")
	}
	hash, err := hashAdminPassword(first)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, hash)
	return nil
}

func logAuthConfiguration(config authConfig) {
	secureMode := "enabled"
	if !config.cookieSecure {
		secureMode = "disabled (HTTP development only)"
	}
	log.Printf("Administrator authentication enabled for username %q; Secure cookie %s", config.username, secureMode)
}
