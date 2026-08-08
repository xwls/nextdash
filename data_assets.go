package main

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

var publicImageExtensions = map[string]string{
	".avif": "image/avif",
	".gif":  "image/gif",
	".ico":  "image/x-icon",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".png":  "image/png",
	".svg":  "image/svg+xml",
	".webp": "image/webp",
}

var publicFontExtensions = map[string]string{
	".otf":   "font/otf",
	".ttf":   "font/ttf",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

func dataAssetRelativePath(r *http.Request) (string, string, bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return "", "", false
	}
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(strings.ToLower(escaped), "/data/") {
		return "", "", false
	}
	raw := escaped[len("/data/"):]
	lowerRaw := strings.ToLower(raw)
	if raw == "" || strings.Contains(lowerRaw, "%25") || strings.Contains(lowerRaw, "%5c") || strings.Contains(raw, "\\") {
		return "", "", false
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil || decoded == "" || strings.Contains(decoded, "\\") || strings.Contains(decoded, "%") || strings.ContainsRune(decoded, '\x00') {
		return "", "", false
	}
	parts := strings.Split(decoded, "/")
	if len(parts) == 0 {
		return "", "", false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") || filepath.Base(part) != part {
			return "", "", false
		}
	}

	var relative, contentType string
	if len(parts) == 2 && parts[0] == "icons" {
		ext := strings.ToLower(filepath.Ext(parts[1]))
		contentType = publicImageExtensions[ext]
		if contentType == "" {
			return "", "", false
		}
		relative = filepath.Join("icons", parts[1])
	} else if len(parts) == 1 {
		name := parts[0]
		ext := strings.ToLower(filepath.Ext(name))
		base := strings.TrimSuffix(name, filepath.Ext(name))
		switch base {
		case "font":
			contentType = publicFontExtensions[ext]
		case "favicon":
			if ext == ".ico" || ext == ".png" || ext == ".jpg" || ext == ".gif" {
				contentType = publicImageExtensions[ext]
			}
		default:
			contentType = publicImageExtensions[ext]
		}
		if contentType == "" {
			return "", "", false
		}
		relative = name
	} else {
		return "", "", false
	}
	return relative, contentType, true
}

func pathWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func safeDataAssetHandler(dataDir string) http.Handler {
	root, err := filepath.Abs(dataDir)
	if err != nil {
		root = filepath.Clean(dataDir)
	}
	if resolvedRoot, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolvedRoot
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relative, contentType, ok := dataAssetRelativePath(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		candidate, err := filepath.Abs(filepath.Join(root, relative))
		if err != nil || !pathWithinRoot(root, candidate) {
			http.NotFound(w, r)
			return
		}
		resolvedCandidate, candidateErr := filepath.EvalSymlinks(candidate)
		if candidateErr != nil || !pathWithinRoot(root, resolvedCandidate) || filepath.Clean(resolvedCandidate) != filepath.Clean(candidate) {
			http.NotFound(w, r)
			return
		}
		file, err := os.Open(resolvedCandidate)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		if detected := mime.TypeByExtension(strings.ToLower(filepath.Ext(candidate))); detected != "" {
			contentType = detected
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if strings.EqualFold(filepath.Ext(candidate), ".svg") {
			w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
		}
		w.Header().Set("Content-Length", stringInt64(info.Size()))
		w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = io.Copy(w, file)
	})
}

func stringInt64(value int64) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}
