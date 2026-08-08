package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeDataAssetHandlerAllowlist(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "icons"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"favicon.png":                      {0x89, 'P', 'N', 'G'},
		"favicon.svg":                      []byte(`<svg></svg>`),
		"font.woff2":                       {'w', 'O', 'F', '2'},
		"legacy.jpg":                       {0xff, 0xd8, 0xff},
		filepath.Join("icons", "site.svg"): []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		"settings.json":                    []byte(`{"secret":true}`),
		"backup.zip":                       []byte("secret"),
		".hidden.png":                      []byte("secret"),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handler := safeDataAssetHandler(root)

	allowed := []string{"/data/favicon.png", "/data/font.woff2", "/data/legacy.jpg", "/data/icons/site.svg"}
	for _, path := range allowed {
		req := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s returned %d", path, rr.Code)
		}
		if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s missing nosniff", path)
		}
	}

	headReq := httptest.NewRequest(http.MethodHead, "http://example.com/data/icons/site.svg", nil)
	headRR := httptest.NewRecorder()
	handler.ServeHTTP(headRR, headReq)
	if headRR.Code != http.StatusOK || headRR.Body.Len() != 0 {
		t.Fatalf("HEAD returned %d with %d body bytes", headRR.Code, headRR.Body.Len())
	}
	if csp := headRR.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Fatalf("SVG sandbox CSP missing: %q", csp)
	}
}

func TestSafeDataAssetHandlerBlocksSensitiveAndTraversalPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "icons"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"settings.json", "backup.zip", ".hidden.png"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handler := safeDataAssetHandler(root)
	blocked := []string{
		"/data/",
		"/data/settings.json",
		"/data/backup.zip",
		"/data/favicon.svg",
		"/data/.hidden.png",
		"/data/icons/",
		"/data/icons/nested/site.png",
		"/data/../settings.json",
		"/data/%2e%2e/settings.json",
		"/data/%252e%252e%252fsettings.json",
		"/data/icons%5csite.png",
	}
	for _, path := range blocked {
		req := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET %s returned %d, want 404", path, rr.Code)
		}
	}

	postReq := httptest.NewRequest(http.MethodPost, "http://example.com/data/favicon.png", nil)
	postRR := httptest.NewRecorder()
	handler.ServeHTTP(postRR, postReq)
	if postRR.Code != http.StatusNotFound {
		t.Fatalf("POST returned %d, want 404", postRR.Code)
	}
}

func TestSafeDataAssetHandlerRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "icons"), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "settings.json")
	if err := os.WriteFile(secret, []byte(`{"secret":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "icons", "public.png")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/data/icons/public.png", nil)
	rr := httptest.NewRecorder()
	safeDataAssetHandler(root).ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("symlinked asset returned %d, want 404", rr.Code)
	}
}
