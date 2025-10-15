package main

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	domain := "example.com"
	urls := getFaviconURLs(baseURL, domain)

	if len(urls) == 0 {
		t.Error("getFaviconURLs should return at least one group of URLs")
	}

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

func TestGetHTMLIconLinks(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		html := `<!DOCTYPE html>
<html>
<head>
	<link rel="icon" href="/favicon.ico" type="image/x-icon">
	<link href="/icons/custom.png" rel="icon" type="image/png">
	<link rel="shortcut icon" href="/shortcut.ico">
	<link rel="icon" href="https://cdn.example.com/icon.png">
	<link rel="icon" href="./relative/icon.png">
	<link rel="stylesheet" href="/style.css">
</head>
<body>Test</body>
</html>`
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(html))
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	icons := getHTMLIconLinks(server.URL)

	if len(icons) == 0 {
		t.Error("getHTMLIconLinks should find icon links")
	}

	expectedIcons := []string{
		server.URL + "/favicon.ico",
		server.URL + "/icons/custom.png",
		server.URL + "/shortcut.ico",
		"https://cdn.example.com/icon.png",
		server.URL + "/relative/icon.png",
	}

	if len(icons) != len(expectedIcons) {
		t.Errorf("Expected %d icons, got %d", len(expectedIcons), len(icons))
	}

	for _, expected := range expectedIcons {
		found := false
		for _, icon := range icons {
			if icon == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected to find icon %q, but it was not found", expected)
		}
	}
}

func TestGetFaviconURLsPriority(t *testing.T) {
	// Test that HTML icons are fetched after manifest icons
	// This is a simple check to ensure the priority order is correct
	baseURL := "https://example.com"
	domain := "example.com"
	groups := getFaviconURLs(baseURL, domain)

	// First group should be standard favicon locations
	if len(groups) < 1 {
		t.Fatal("Expected at least 1 URL group")
	}

	firstGroup := groups[0]
	if !strings.Contains(firstGroup[0], "favicon.ico") {
		t.Error("First priority should be favicon.ico")
	}

	// Second group should be apple touch icons
	if len(groups) < 2 {
		t.Fatal("Expected at least 2 URL groups")
	}

	secondGroup := groups[1]
	if !strings.Contains(secondGroup[0], "apple-touch-icon") {
		t.Error("Second priority should be apple touch icons")
	}
}

func TestResizeImage(t *testing.T) {
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

var testRepo *FaviconRepository

func setupTestDB(t *testing.T) *FaviconRepository {
	var err error
	testRepo, err = NewFaviconRepository(":memory:")
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return testRepo
}

func teardownTestDB(t *testing.T) {
	if testRepo != nil {
		testRepo.Close()
	}
}

func TestFaviconCaching(t *testing.T) {
	repo := setupTestDB(t)
	defer teardownTestDB(t)

	domain := "example.com"
	data := []byte("test data")
	contentType := "image/x-icon"

	err := repo.Save(domain, data, contentType)
	if err != nil {
		t.Errorf("Save failed: %v", err)
	}

	cachedData, cachedContentType, err := repo.Get(domain)
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}

	if !bytes.Equal(cachedData, data) {
		t.Error("Cached data doesn't match original")
	}

	if cachedContentType != contentType {
		t.Errorf("Cached content type = %q, want %q", cachedContentType, contentType)
	}

	_, _, err = repo.Get("nonexistent.com")
	if err == nil {
		t.Error("Expected error for non-existent domain")
	}
}

func TestHandleHealthz(t *testing.T) {
	repo = setupTestDB(t)
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
	if _, err := assets.Embeddedfiles.Open("static/robots.txt"); err != nil {
		t.Skip("Embedded static files not available, skipping test")
	}

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
	if _, err := assets.Embeddedfiles.Open("static/favicon.ico"); err != nil {
		t.Skip("Embedded static files not available, skipping test")
	}

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
	repo = setupTestDB(t)
	defer teardownTestDB(t)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handleHome(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHandleHomeWithCachedFavicon(t *testing.T) {
	repo = setupTestDB(t)
	defer teardownTestDB(t)

	domain := "example.com"
	data := []byte("cached favicon data")
	contentType := "image/x-icon"
	repo.Save(domain, data, contentType)

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

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d for path without trailing slash, got %d", http.StatusOK, w.Code)
	}

	req = httptest.NewRequest("GET", "/test/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status %d for path with trailing slash, got %d", http.StatusNotFound, w.Code)
	}

	req = httptest.NewRequest("GET", "/static/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d for /static/, got %d", http.StatusOK, w.Code)
	}
}

func BenchmarkExtractDomain(b *testing.B) {
	url := "https://www.example.com/path/to/page"
	for b.Loop() {
		extractDomain(url)
	}
}

func BenchmarkGetCachedFavicon(b *testing.B) {
	repo = setupTestDB(nil)
	defer teardownTestDB(nil)

	repo.Save("example.com", []byte("test data"), "image/x-icon")

	for b.Loop() {
		repo.Get("example.com")
	}
}
