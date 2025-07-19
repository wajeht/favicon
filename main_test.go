package main

import (
	"bytes"
	"database/sql"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
	"github.com/wajeht/favicon/assets"
)

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://example.com", "example.com"},
		{"http://example.com", "example.com"},
		{"https://example.com/path", "example.com"},
		{"https://example.com:8080", "example.com"},
		{"https://sub.example.com", "sub.example.com"},
		{"example.com", "example.com"},
		{"EXAMPLE.COM", "example.com"},
		{"", ""},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result := extractDomain(test.input)
			if result != test.expected {
				t.Errorf("extractDomain(%q) = %q, want %q", test.input, result, test.expected)
			}
		})
	}
}

func TestIsImage(t *testing.T) {
	tests := []struct {
		contentType string
		expected    bool
	}{
		{"image/x-icon", true},
		{"image/vnd.microsoft.icon", true},
		{"image/png", true},
		{"image/jpeg", true},
		{"image/gif", true},
		{"image/svg+xml", true},
		{"image/webp", true},
		{"text/html", false},
		{"application/json", false},
		{"", false},
		{"image/png; charset=utf-8", true},
	}

	for _, test := range tests {
		t.Run(test.contentType, func(t *testing.T) {
			result := isImage(test.contentType)
			if result != test.expected {
				t.Errorf("isImage(%q) = %t, want %t", test.contentType, result, test.expected)
			}
		})
	}
}

func TestGetContentType(t *testing.T) {
	tests := []struct {
		url             string
		respContentType string
		expected        string
	}{
		{"https://example.com/favicon.png", "", "image/png"},
		{"https://example.com/favicon.ico", "", "image/x-icon"},
		{"https://example.com/favicon.png", "image/png", "image/png"},
		{"https://example.com/favicon.ico", "image/x-icon", "image/x-icon"},
	}

	for _, test := range tests {
		t.Run(test.url, func(t *testing.T) {
			result := getContentType(test.url, test.respContentType)
			if result != test.expected {
				t.Errorf("getContentType(%q, %q) = %q, want %q", test.url, test.respContentType, result, test.expected)
			}
		})
	}
}

func TestGetFaviconURLs(t *testing.T) {
	baseURL := "https://example.com"
	urls := getFaviconURLs(baseURL)

	if len(urls) == 0 {
		t.Error("getFaviconURLs should return at least one group of URLs")
	}

	// Check that favicon.ico is in the first group
	found := false
	for _, url := range urls[0] {
		if strings.Contains(url, "favicon.ico") {
			found = true
			break
		}
	}
	if !found {
		t.Error("First group should contain favicon.ico")
	}
}

func TestResizeImage(t *testing.T) {
	// Create a simple 32x32 PNG image
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	resized, err := resizeImage(buf.Bytes(), "image/png")
	if err != nil {
		t.Errorf("resizeImage failed: %v", err)
	}

	if len(resized) == 0 {
		t.Error("resizeImage returned empty data")
	}

	// Test with small image (should not resize)
	smallImg := image.NewRGBA(image.Rect(0, 0, 16, 16))
	var smallBuf bytes.Buffer
	if err := png.Encode(&smallBuf, smallImg); err != nil {
		t.Fatal(err)
	}

	notResized, err := resizeImage(smallBuf.Bytes(), "image/png")
	if err != nil {
		t.Errorf("resizeImage failed for small image: %v", err)
	}

	if !bytes.Equal(notResized, smallBuf.Bytes()) {
		t.Error("Small image should not be resized")
	}
}

func setupTestDB(t *testing.T) {
	var err error
	db, err = sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}

	goose.SetBaseFS(assets.Embeddedfiles)
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}

	if err := goose.Up(db, "migrations"); err != nil {
		t.Fatal(err)
	}

	getFaviconStmt, err = db.Prepare(`
		SELECT data, content_type
		FROM favicons
		WHERE domain = ? AND expires_at > CURRENT_TIMESTAMP
	`)
	if err != nil {
		t.Fatal(err)
	}

	saveFaviconStmt, err = db.Prepare(`
		INSERT OR REPLACE INTO favicons (domain, data, content_type, expires_at)
		VALUES (?, ?, ?, datetime('now', '+24 hours'))
	`)
	if err != nil {
		t.Fatal(err)
	}
}

func teardownTestDB(t *testing.T) {
	if getFaviconStmt != nil {
		getFaviconStmt.Close()
	}
	if saveFaviconStmt != nil {
		saveFaviconStmt.Close()
	}
	if db != nil {
		db.Close()
	}
}

func TestFaviconCaching(t *testing.T) {
	setupTestDB(t)
	defer teardownTestDB(t)

	domain := "example.com"
	data := []byte("test data")
	contentType := "image/x-icon"

	// Test saving
	err := saveFavicon(domain, data, contentType)
	if err != nil {
		t.Errorf("saveFavicon failed: %v", err)
	}

	// Test retrieval
	cachedData, cachedContentType, err := getCachedFavicon(domain)
	if err != nil {
		t.Errorf("getCachedFavicon failed: %v", err)
	}

	if !bytes.Equal(cachedData, data) {
		t.Error("Cached data doesn't match original")
	}

	if cachedContentType != contentType {
		t.Errorf("Cached content type = %q, want %q", cachedContentType, contentType)
	}

	// Test non-existent domain
	_, _, err = getCachedFavicon("nonexistent.com")
	if err == nil {
		t.Error("Expected error for non-existent domain")
	}
}

func TestHandleHealthz(t *testing.T) {
	setupTestDB(t)
	defer teardownTestDB(t)

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()

	handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	if body := w.Body.String(); body != "ok" {
		t.Errorf("Expected body 'ok', got %q", body)
	}
}

func TestHandleRobotsTxt(t *testing.T) {
	req := httptest.NewRequest("GET", "/robots.txt", nil)
	w := httptest.NewRecorder()

	handleRobotsTxt(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "text/plain" {
		t.Errorf("Expected Content-Type 'text/plain', got %q", contentType)
	}
}

func TestHandleFavicon(t *testing.T) {
	req := httptest.NewRequest("GET", "/favicon.ico", nil)
	w := httptest.NewRecorder()

	handleFavicon(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "image/x-icon" {
		t.Errorf("Expected Content-Type 'image/x-icon', got %q", contentType)
	}
}

func TestHandleHomeMissingURL(t *testing.T) {
	setupTestDB(t)
	defer teardownTestDB(t)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handleHome(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandleHomeWithCachedFavicon(t *testing.T) {
	setupTestDB(t)
	defer teardownTestDB(t)

	// Pre-populate cache
	domain := "example.com"
	data := []byte("cached favicon data")
	contentType := "image/x-icon"
	saveFavicon(domain, data, contentType)

	req := httptest.NewRequest("GET", "/?url=example.com", nil)
	w := httptest.NewRecorder()

	handleHome(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, w.Code)
	}

	if w.Header().Get("X-Cache") != "HIT" {
		t.Error("Expected cache hit")
	}

	if !bytes.Equal(w.Body.Bytes(), data) {
		t.Error("Response body doesn't match cached data")
	}
}

func TestStripTrailingSlashMiddleware(t *testing.T) {
	handler := stripTrailingSlashMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test path without trailing slash (should pass through)
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d for path without trailing slash, got %d", http.StatusOK, w.Code)
	}

	// Test path with trailing slash (should return 404)
	req = httptest.NewRequest("GET", "/test/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status %d for path with trailing slash, got %d", http.StatusNotFound, w.Code)
	}

	// Test /static/ (should pass through)
	req = httptest.NewRequest("GET", "/static/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d for /static/, got %d", http.StatusOK, w.Code)
	}
}

func TestCleanupExpiredFavicons(t *testing.T) {
	setupTestDB(t)
	defer teardownTestDB(t)

	// Insert an expired favicon
	_, err := db.Exec(`
		INSERT INTO favicons (domain, data, content_type, expires_at)
		VALUES (?, ?, ?, datetime('now', '-1 hour'))
	`, "expired.com", []byte("data"), "image/x-icon")
	if err != nil {
		t.Fatal(err)
	}

	// Insert a valid favicon
	err = saveFavicon("valid.com", []byte("data"), "image/x-icon")
	if err != nil {
		t.Fatal(err)
	}

	// Run cleanup
	err = cleanupExpiredFavicons()
	if err != nil {
		t.Errorf("cleanupExpiredFavicons failed: %v", err)
	}

	// Check that expired favicon is gone
	_, _, err = getCachedFavicon("expired.com")
	if err == nil {
		t.Error("Expected expired favicon to be cleaned up")
	}

	// Check that valid favicon is still there
	_, _, err = getCachedFavicon("valid.com")
	if err != nil {
		t.Error("Valid favicon should not be cleaned up")
	}
}

func BenchmarkExtractDomain(b *testing.B) {
	url := "https://www.example.com/path/to/page"
	for i := 0; i < b.N; i++ {
		extractDomain(url)
	}
}

func BenchmarkGetCachedFavicon(b *testing.B) {
	setupTestDB(nil)
	defer teardownTestDB(nil)

	// Pre-populate cache
	saveFavicon("example.com", []byte("test data"), "image/x-icon")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		getCachedFavicon("example.com")
	}
}
