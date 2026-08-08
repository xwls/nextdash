package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

type loginThemeStoreStub struct {
	settings Settings
	colors   ColorTheme
}

func (s loginThemeStoreStub) GetSettings() Settings { return s.settings }
func (s loginThemeStoreStub) GetColors() ColorTheme { return s.colors }

func fastTestAuthService(t *testing.T, secure bool) *authService {
	t.Helper()
	salt := []byte("test-salt-123456")
	password := []byte("secret-password")
	parsed := argon2PasswordHash{
		memory:      32,
		iterations:  1,
		parallelism: 1,
		salt:        salt,
		hash:        argon2.IDKey(password, salt, 1, 32, 1, 16),
	}
	return newAuthService(authConfig{username: "admin", passwordHash: parsed, cookieSecure: secure}, embeddedFiles, nil)
}

func loginRequest(t *testing.T, auth *authService, next string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"username": {"admin"}, "password": {"secret-password"}, "next": {next}}
	req := httptest.NewRequest(http.MethodPost, "http://example.com/login", strings.NewReader(form.Encode()))
	req.RemoteAddr = "192.0.2.10:12345"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	auth.login(rr, req)
	return rr
}

func responseSessionCookie(t *testing.T, response *http.Response) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName {
			return cookie
		}
	}
	t.Fatal("session cookie missing")
	return nil
}

func TestSessionIdleRefreshAndAbsoluteExpiry(t *testing.T) {
	manager := newSessionManager()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	idleSession, err := manager.create()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(sessionIdleTTL)
	if _, ok := manager.get(idleSession.ID, false); ok {
		t.Fatal("session remained valid at idle expiry boundary")
	}

	now = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	refreshed, err := manager.create()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Hour)
	if _, ok := manager.get(refreshed.ID, true); !ok {
		t.Fatal("session could not be refreshed before idle expiry")
	}
	now = time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	if _, ok := manager.get(refreshed.ID, true); ok {
		t.Fatal("session remained valid at absolute expiry boundary")
	}
}

func TestSafeNextPathRejectsExternalRedirects(t *testing.T) {
	for _, unsafe := range []string{"https://evil.example/", "//evil.example/", "/\\evil.example/", "%2F%2Fevil.example/", "/%252F%252Fevil.example/", "/login", "/logout"} {
		if got := safeNextPath(unsafe); got != "/" {
			t.Errorf("safeNextPath(%q) = %q, want /", unsafe, got)
		}
	}
	if got := safeNextPath("/health?q=broken"); got != "/health?q=broken" {
		t.Fatalf("safe relative target changed: %q", got)
	}
}

func TestLoginPageUsesConfiguredTheme(t *testing.T) {
	auth := fastTestAuthService(t, false)
	colors := getDefaultColors()
	auth.themeStore = loginThemeStoreStub{
		settings: Settings{
			Theme:              "electric-orchid-light",
			AutoDarkMode:       true,
			RandomThemeMode:    "refresh",
			ShowBackgroundDots: false,
		},
		colors: colors,
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/login", nil)
	rr := httptest.NewRecorder()
	auth.login(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login page returned %d", rr.Code)
	}

	body := rr.Body.String()
	for _, want := range []string{
		`data-theme="electric-orchid-light"`,
		`data-auto-dark-mode="true"`,
		`data-random-theme-mode="refresh"`,
		`data-show-background-dots="false"`,
		`content="` + colors.BuiltIn["electric-orchid-light"].BackgroundPrimary + `"`,
		`href="/api/theme.css"`,
		`src="/static/js/theme-loader.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("login page missing %q", want)
		}
	}
}

func TestLoginCreatesSecureSessionAndUsesGenericFailure(t *testing.T) {
	auth := fastTestAuthService(t, true)

	bad := url.Values{"username": {"unknown"}, "password": {"wrong"}}
	badReq := httptest.NewRequest(http.MethodPost, "http://example.com/login", strings.NewReader(bad.Encode()))
	badReq.RemoteAddr = "192.0.2.11:12345"
	badReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badRR := httptest.NewRecorder()
	auth.login(badRR, badReq)
	if badRR.Code != http.StatusUnauthorized || !strings.Contains(badRR.Body.String(), "用户名或密码错误") {
		t.Fatalf("unexpected login failure response: %d %s", badRR.Code, badRR.Body.String())
	}

	rr := loginRequest(t, auth, "/health")
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/health" {
		t.Fatalf("unexpected login redirect: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	cookie := responseSessionCookie(t, rr.Result())
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("incorrect cookie attributes: %+v", cookie)
	}
}

func TestLoginRateLimit(t *testing.T) {
	auth := fastTestAuthService(t, false)
	form := url.Values{"username": {"admin"}, "password": {"wrong"}}
	for attempt := 0; attempt < loginFailureLimit; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/login", strings.NewReader(form.Encode()))
		req.RemoteAddr = "192.0.2.12:12345"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		auth.login(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d returned %d", attempt+1, rr.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "http://example.com/login", strings.NewReader(form.Encode()))
	req.RemoteAddr = "192.0.2.12:12345"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	auth.login(rr, req)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit response = %d, Retry-After=%q", rr.Code, rr.Header().Get("Retry-After"))
	}
}

func TestAuthMiddlewareRedirectsHTMLAndReturnsAPI401(t *testing.T) {
	auth := fastTestAuthService(t, false)
	handler := auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	htmlReq := httptest.NewRequest(http.MethodGet, "http://example.com/config?section=data", nil)
	htmlReq.Header.Set("Accept", "text/html")
	htmlRR := httptest.NewRecorder()
	handler.ServeHTTP(htmlRR, htmlReq)
	if htmlRR.Code != http.StatusFound || !strings.HasPrefix(htmlRR.Header().Get("Location"), "/login?next=") {
		t.Fatalf("HTML response = %d %q", htmlRR.Code, htmlRR.Header().Get("Location"))
	}

	apiReq := httptest.NewRequest(http.MethodGet, "http://example.com/api/settings", nil)
	apiRR := httptest.NewRecorder()
	handler.ServeHTTP(apiRR, apiReq)
	if apiRR.Code != http.StatusUnauthorized || strings.Contains(strings.ToLower(apiRR.Body.String()), "<html") {
		t.Fatalf("API response = %d %q", apiRR.Code, apiRR.Body.String())
	}
}

func TestSessionCSRFAndOriginProtection(t *testing.T) {
	auth := fastTestAuthService(t, false)
	login := loginRequest(t, auth, "/")
	cookie := responseSessionCookie(t, login.Result())
	session, ok := auth.sessions.get(cookie.Value, false)
	if !ok {
		t.Fatal("created session missing")
	}
	handler := auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	request := func(origin, token string) int {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/settings", strings.NewReader("{}"))
		req.AddCookie(cookie)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if token != "" {
			req.Header.Set("X-NextDash-CSRF", token)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Code
	}
	if code := request("http://example.com", ""); code != http.StatusForbidden {
		t.Fatalf("missing CSRF returned %d", code)
	}
	if code := request("https://evil.example", session.CSRFToken); code != http.StatusForbidden {
		t.Fatalf("cross-origin request returned %d", code)
	}
	if code := request("http://example.com", session.CSRFToken); code != http.StatusNoContent {
		t.Fatalf("valid CSRF request returned %d", code)
	}
}

func TestExtensionTokenHasRestrictedAllowlist(t *testing.T) {
	t.Setenv("NEXTDASH_WRITE_TOKEN", "extension-secret")
	auth := fastTestAuthService(t, false)
	handler := auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value, ok := authFromContext(r.Context())
		if !ok || value.method != "extension" {
			http.Error(w, "wrong auth", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	allowed := httptest.NewRequest(http.MethodGet, "http://example.com/api/pages", nil)
	allowed.Header.Set("Origin", "chrome-extension://example")
	allowed.Header.Set("X-NextDash-Token", "extension-secret")
	allowedRR := httptest.NewRecorder()
	handler.ServeHTTP(allowedRR, allowed)
	if allowedRR.Code != http.StatusNoContent {
		t.Fatalf("allowed extension route returned %d", allowedRR.Code)
	}
	if allowedRR.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("allowed extension response missing CORS header: %q", allowedRR.Header().Get("Access-Control-Allow-Origin"))
	}

	invalid := httptest.NewRequest(http.MethodGet, "http://example.com/api/pages", nil)
	invalid.Header.Set("Origin", "chrome-extension://example")
	invalid.Header.Set("X-NextDash-Token", "wrong-secret")
	invalidRR := httptest.NewRecorder()
	handler.ServeHTTP(invalidRR, invalid)
	if invalidRR.Code != http.StatusUnauthorized || invalidRR.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("invalid extension response = %d, CORS=%q", invalidRR.Code, invalidRR.Header().Get("Access-Control-Allow-Origin"))
	}

	blocked := httptest.NewRequest(http.MethodGet, "http://example.com/api/settings", nil)
	blocked.Header.Set("X-NextDash-Token", "extension-secret")
	blockedRR := httptest.NewRecorder()
	handler.ServeHTTP(blockedRR, blocked)
	if blockedRR.Code != http.StatusUnauthorized {
		t.Fatalf("restricted extension route returned %d", blockedRR.Code)
	}
}

func TestLogoutDeletesSession(t *testing.T) {
	auth := fastTestAuthService(t, false)
	login := loginRequest(t, auth, "/")
	cookie := responseSessionCookie(t, login.Result())
	session, _ := auth.sessions.get(cookie.Value, false)

	req := httptest.NewRequest(http.MethodPost, "http://example.com/logout", nil)
	req.AddCookie(cookie)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("X-NextDash-CSRF", session.CSRFToken)
	rr := httptest.NewRecorder()
	auth.middleware(http.HandlerFunc(auth.logout)).ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		body, _ := io.ReadAll(rr.Result().Body)
		t.Fatalf("logout returned %d: %s", rr.Code, body)
	}
	if _, ok := auth.sessions.get(cookie.Value, false); ok {
		t.Fatal("session remained after logout")
	}
	cleared := responseSessionCookie(t, rr.Result())
	if cleared.MaxAge >= 0 {
		t.Fatalf("logout cookie not cleared: %+v", cleared)
	}
}

func TestAuthMiddlewareAllowsOnlyDeclaredPublicRoutes(t *testing.T) {
	auth := fastTestAuthService(t, false)
	handler := auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	public := []string{
		"/login",
		"/healthz",
		"/manifest.webmanifest",
		"/push-service-worker.js",
		"/static/css/login.css",
		"/api/theme.css",
		"/locales/en.json",
		"/data/favicon.png",
	}
	for _, path := range public {
		req := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Errorf("public route %s returned %d", path, rr.Code)
		}
	}

	for _, path := range []string{"/", "/health", "/config", "/version", "/api/health"} {
		req := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		if !strings.HasPrefix(path, "/api/") && path != "/version" {
			req.Header.Set("Accept", "text/html")
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusFound && rr.Code != http.StatusUnauthorized {
			t.Errorf("protected route %s returned %d", path, rr.Code)
		}
	}
}
