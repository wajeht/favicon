package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
	"github.com/wajeht/favicon/assets"
	"golang.org/x/image/draw"
)

const (
	httpTimeout           = 1 * time.Second
	maxIdleConns          = 200
	maxIdleConnsPerHost   = 30
	maxConnsPerHost       = 50
	idleConnTimeout       = 60 * time.Second
	writeBufferSize       = 64 * 1024
	readBufferSize        = 64 * 1024
	tlsHandshakeTimeout   = 500 * time.Millisecond
	responseHeaderTimeout = 500 * time.Millisecond
	expectContinueTimeout = 200 * time.Millisecond

	faviconFetchTimeout = 1500 * time.Millisecond
	maxHTMLReadSize     = 512 * 1024  // 512KB
	maxImageSize        = 1024 * 1024 // 1MB

	targetIconSize = 16
	jpegQuality    = 90

	maxOpenConns    = 100
	maxIdleDBConns  = 25
	connMaxLifetime = 5 * time.Minute

	cacheTTL     = 86400 // 1 day in seconds
	listCacheTTL = 300   // 5 minutes in seconds

	serverAddr      = ":80"
	shutdownTimeout = 30 * time.Second

	userAgent = "FaviconBot/1.0"
)

var (
	ErrNotFound = errors.New("favicon not found")

	repo *FaviconRepository

	httpClient = newHTTPClient()
)

type FaviconResult struct {
	Data        []byte
	ContentType string
	URL         string
	Error       error
}

type Manifest struct {
	Icons []ManifestIcon `json:"icons"`
}

type ManifestIcon struct {
	Src   string `json:"src"`
	Sizes string `json:"sizes"`
	Type  string `json:"type"`
}

type FaviconRepository struct {
	db *sql.DB
}

func NewFaviconRepository(dbPath string) (*FaviconRepository, error) {
	path := strings.Split(dbPath, "?")[0]
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleDBConns)
	db.SetConnMaxLifetime(connMaxLifetime)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	if err := applyPragmas(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to apply pragmas: %w", err)
	}

	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return &FaviconRepository{db: db}, nil
}

func (r *FaviconRepository) Get(domain string) ([]byte, string, error) {
	var data []byte
	var contentType string

	query := `SELECT data, content_type FROM favicons WHERE domain = ?`
	err := r.db.QueryRow(query, domain).Scan(&data, &contentType)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("failed to get favicon: %w", err)
	}

	return data, contentType, nil
}

func (r *FaviconRepository) Save(domain string, data []byte, contentType string) error {
	query := `INSERT OR REPLACE INTO favicons (domain, data, content_type) VALUES (?, ?, ?)`
	_, err := r.db.Exec(query, domain, data, contentType)
	if err != nil {
		return fmt.Errorf("failed to save favicon: %w", err)
	}
	return nil
}

func (r *FaviconRepository) List() (string, error) {
	query := `
		SELECT json_group_array(
			json_object(
				'domain', domain,
				'content_type', content_type,
				'created_at', created_at
			)
		)
		FROM favicons
		ORDER BY created_at DESC
	`

	rows, err := r.db.Query(query)
	if err != nil {
		return "", fmt.Errorf("failed to list favicons: %w", err)
	}
	defer rows.Close()

	var jsonResult string
	if rows.Next() {
		if err := rows.Scan(&jsonResult); err != nil {
			return "", fmt.Errorf("failed to scan result: %w", err)
		}
	}

	return jsonResult, nil
}

func (r *FaviconRepository) Ping() error {
	return r.db.Ping()
}

func (r *FaviconRepository) Close() error {
	return r.db.Close()
}

func applyPragmas(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=10000",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA mmap_size=268435456",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("Warning: Failed to set pragma %s: %v", pragma, err)
		}
	}

	return nil
}

func runMigrations(db *sql.DB) error {
	goose.SetBaseFS(assets.Embeddedfiles)

	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("failed to set goose dialect: %w", err)
	}

	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: httpTimeout,
		Transport: &http.Transport{
			MaxIdleConns:          maxIdleConns,
			MaxIdleConnsPerHost:   maxIdleConnsPerHost,
			MaxConnsPerHost:       maxConnsPerHost,
			IdleConnTimeout:       idleConnTimeout,
			DisableKeepAlives:     false,
			WriteBufferSize:       writeBufferSize,
			ReadBufferSize:        readBufferSize,
			TLSHandshakeTimeout:   tlsHandshakeTimeout,
			ResponseHeaderTimeout: responseHeaderTimeout,
			ExpectContinueTimeout: expectContinueTimeout,
			ForceAttemptHTTP2:     true,
		},
	}
}

func extractDomain(rawURL string) string {
	u := rawURL
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")

	if idx := strings.IndexByte(u, '/'); idx != -1 {
		u = u[:idx]
	}

	if idx := strings.IndexByte(u, ':'); idx != -1 {
		u = u[:idx]
	}

	if u == "" {
		return rawURL
	}

	return strings.ToLower(u)
}

func normalizeIconURL(baseURL, iconURL string) string {
	if strings.HasPrefix(iconURL, "./") {
		iconURL = strings.TrimPrefix(iconURL, ".")
	}

	if strings.HasPrefix(iconURL, "http://") || strings.HasPrefix(iconURL, "https://") {
		return iconURL
	}

	if strings.HasPrefix(iconURL, "/") {
		return baseURL + iconURL
	}

	return baseURL + "/" + iconURL
}

func getFaviconURLs(baseURL, domain string) [][]string {
	groups := [][]string{
		{
			baseURL + "/favicon.ico",
			baseURL + "/favicon.png",
			baseURL + "/favicon.svg",
			baseURL + "/" + domain + ".ico",
			baseURL + "/" + domain + ".png",
		},
		{
			baseURL + "/apple-touch-icon.png",
			baseURL + "/apple-touch-icon-precomposed.png",
		},
		{
			baseURL + "/apple-touch-icon-180x180.png",
			baseURL + "/apple-touch-icon-152x152.png",
			baseURL + "/apple-touch-icon-120x120.png",
		},
	}

	if manifestIcons := getManifestIcons(baseURL); len(manifestIcons) > 0 {
		groups = append(groups, manifestIcons)
	}

	if htmlIcons := getHTMLIconLinks(baseURL); len(htmlIcons) > 0 {
		groups = append(groups, htmlIcons)
	}

	return groups
}

func getManifestIcons(baseURL string) []string {
	resp, err := httpClient.Get(baseURL + "/manifest.json")
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()

	var manifest Manifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil
	}

	icons := make([]string, 0, len(manifest.Icons))
	for _, icon := range manifest.Icons {
		iconURL := icon.Src

		parsed, err := url.Parse(iconURL)
		if err == nil && parsed.IsAbs() {
			icons = append(icons, iconURL)
			continue
		}

		icons = append(icons, normalizeIconURL(baseURL, iconURL))
	}

	return icons
}

func getHTMLIconLinks(baseURL string) []string {
	resp, err := httpClient.Get(baseURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLReadSize))
	if err != nil {
		return nil
	}

	return parseIconLinks(string(body), baseURL)
}

func parseIconLinks(html, baseURL string) []string {
	var icons []string
	offset := 0

	for {
		idx := strings.Index(html[offset:], "<link")
		if idx == -1 {
			break
		}
		offset += idx

		end := strings.Index(html[offset:], ">")
		if end == -1 {
			break
		}

		tag := html[offset : offset+end+1]

		if isIconLink(tag) {
			if href := extractHrefAttribute(tag); href != "" {
				icons = append(icons, normalizeIconURL(baseURL, href))
			}
		}

		offset += end + 1
	}

	return icons
}

func isIconLink(tag string) bool {
	rel := extractAttribute(tag, "rel")
	if rel == "" {
		return false
	}

	rel = strings.ToLower(strings.TrimSpace(rel))

	if !strings.Contains(rel, "icon") {
		return false
	}

	excludedRels := []string{"preload", "modulepreload", "dns-prefetch", "preconnect", "prefetch"}
	for _, excluded := range excludedRels {
		if strings.Contains(rel, excluded) {
			return false
		}
	}

	return true
}

func extractHrefAttribute(tag string) string {
	return extractAttribute(tag, "href")
}

func extractAttribute(tag, attrName string) string {
	attrPrefix := attrName + "="
	idx := strings.Index(tag, attrPrefix)
	if idx == -1 {
		return ""
	}

	start := idx + len(attrPrefix)
	if start >= len(tag) {
		return ""
	}

	quote := tag[start]
	if quote != '"' && quote != '\'' {
		return ""
	}

	start++
	end := strings.IndexByte(tag[start:], quote)
	if end == -1 {
		return ""
	}

	return tag[start : start+end]
}

func resizeImage(data []byte, contentType string) ([]byte, error) {
	var img image.Image
	var err error

	switch {
	case strings.Contains(contentType, "png"):
		img, err = png.Decode(bytes.NewReader(data))
	case strings.Contains(contentType, "jpeg"), strings.Contains(contentType, "jpg"):
		img, err = jpeg.Decode(bytes.NewReader(data))
	default:
		return data, nil
	}

	if err != nil {
		return data, nil
	}

	bounds := img.Bounds()
	if bounds.Dx() <= targetIconSize && bounds.Dy() <= targetIconSize {
		return data, nil
	}

	dst := image.NewRGBA(image.Rect(0, 0, targetIconSize, targetIconSize))
	draw.NearestNeighbor.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if strings.Contains(contentType, "png") {
		err = png.Encode(&buf, dst)
	} else {
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: jpegQuality})
	}

	if err != nil || buf.Len() >= len(data) {
		return data, nil
	}

	return buf.Bytes(), nil
}

func fetchFavicon(ctx context.Context, targetURL string) FaviconResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return FaviconResult{Error: err, URL: targetURL}
	}

	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "image/*")

	resp, err := httpClient.Do(req)
	if err != nil {
		return FaviconResult{Error: err, URL: targetURL}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return FaviconResult{
			Error: fmt.Errorf("HTTP %d", resp.StatusCode),
			URL:   targetURL,
		}
	}

	contentType := resp.Header.Get("Content-Type")
	if !isValidImageType(contentType) {
		return FaviconResult{
			Error: fmt.Errorf("invalid content type: %s", contentType),
			URL:   targetURL,
		}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize))
	if err != nil {
		return FaviconResult{Error: err, URL: targetURL}
	}

	optimizedData, _ := resizeImage(data, contentType)

	return FaviconResult{
		Data:        optimizedData,
		ContentType: inferContentType(targetURL, contentType),
		URL:         targetURL,
	}
}

func fetchFaviconsParallel(urlGroups [][]string, timeout time.Duration) *FaviconResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	resultChan := make(chan FaviconResult, 10)
	var wg sync.WaitGroup

	for _, urls := range urlGroups {
		for _, u := range urls {
			wg.Add(1)
			go func(targetURL string) {
				defer wg.Done()
				result := fetchFavicon(ctx, targetURL)
				if result.Error == nil {
					select {
					case resultChan <- result:
					case <-ctx.Done():
					}
				}
			}(u)
		}
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	for result := range resultChan {
		if result.Error == nil {
			cancel()
			return &result
		}
	}

	return nil
}

func isValidImageType(contentType string) bool {
	if contentType == "" {
		return false
	}

	contentType = strings.ToLower(strings.Split(contentType, ";")[0])

	switch contentType {
	case "image/x-icon", "image/vnd.microsoft.icon", "image/icon", "image/ico",
		"image/png", "image/jpeg", "image/jpg", "image/gif",
		"image/svg+xml", "image/webp":
		return true
	default:
		return false
	}
}

func inferContentType(targetURL, respContentType string) string {
	if respContentType != "" {
		return respContentType
	}

	if strings.HasSuffix(targetURL, ".png") {
		return "image/png"
	}

	return "image/x-icon"
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func stripTrailingSlashMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") && r.URL.Path != "/static/" {
			http.Error(w, "The requested resource could not be found", http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, "The requested resource could not be found", http.StatusNotFound)
		return
	}

	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		http.Error(w, "Missing 'url' query parameter. Usage: /?url=<url>", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	domain := extractDomain(rawURL)

	if serveFromCache(w, r, domain) {
		return
	}

	baseURL := "https://" + domain
	faviconURLGroups := getFaviconURLs(baseURL, domain)

	result := fetchFaviconsParallel(faviconURLGroups, faviconFetchTimeout)
	if result != nil {
		if err := repo.Save(domain, result.Data, result.ContentType); err != nil {
			log.Printf("Failed to cache favicon for %s: %v", domain, err)
		}

		serveFaviconData(w, result.Data, result.ContentType, false)
		return
	}

	serveDefaultFavicon(w, r)
}

func serveFromCache(w http.ResponseWriter, r *http.Request, domain string) bool {
	data, contentType, err := repo.Get(domain)
	if err != nil {
		return false
	}

	etag := fmt.Sprintf(`"fav-%s"`, domain)

	clientETag := r.Header.Get("If-None-Match")
	if clientETag == etag || clientETag == "W/"+etag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, immutable", cacheTTL))
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Cache", "HIT")
	w.Header().Set("X-Favicon-Source", "cached")

	if _, err := w.Write(data); err != nil {
		log.Printf("Error writing cached response: %v", err)
	}

	return true
}

func serveFaviconData(w http.ResponseWriter, data []byte, contentType string, cached bool) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", cacheTTL))

	if cached {
		w.Header().Set("X-Cache", "HIT")
		w.Header().Set("X-Favicon-Source", "cached")
	} else {
		w.Header().Set("X-Cache", "MISS")
		w.Header().Set("X-Favicon-Source", "fetched")
	}

	if _, err := w.Write(data); err != nil {
		log.Printf("Error writing favicon response: %v", err)
	}
}

func serveDefaultFavicon(w http.ResponseWriter, r *http.Request) {
	file, err := assets.Embeddedfiles.Open("static/favicon.ico")
	if err != nil {
		log.Printf("Error opening default favicon: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", cacheTTL))
	w.Header().Set("X-Cache", "DEFAULT")
	w.Header().Set("X-Favicon-Source", "default")

	if _, err := io.Copy(w, file); err != nil {
		log.Printf("Error copying default favicon: %v", err)
	}
}

func handleDomains(w http.ResponseWriter, r *http.Request) {
	if err := repo.Ping(); err != nil {
		http.Error(w, "Database connection failed", http.StatusServiceUnavailable)
		return
	}

	jsonResult, err := repo.List()
	if err != nil {
		log.Printf("Error listing domains: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, must-revalidate", listCacheTTL))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(jsonResult))
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := repo.Ping(); err != nil {
		http.Error(w, "Database connection failed", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func handleFavicon(w http.ResponseWriter, r *http.Request) {
	file, err := assets.Embeddedfiles.Open("static/favicon.ico")
	if err != nil {
		log.Printf("Error opening favicon: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "image/x-icon")
	if _, err := io.Copy(w, file); err != nil {
		log.Printf("Error serving favicon: %v", err)
	}
}

func handleRobotsTxt(w http.ResponseWriter, r *http.Request) {
	file, err := assets.Embeddedfiles.Open("static/robots.txt")
	if err != nil {
		log.Printf("Error opening robots.txt: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "text/plain")
	if _, err := io.Copy(w, file); err != nil {
		log.Printf("Error serving robots.txt: %v", err)
	}
}

func main() {
	var err error
	repo, err = NewFaviconRepository("./data/db.sqlite?cache=shared&mode=rwc&_journal_mode=WAL")
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer repo.Close()

	mux := http.NewServeMux()
	mux.Handle("GET /static/", stripTrailingSlashMiddleware(http.FileServer(http.FS(assets.Embeddedfiles))))
	mux.HandleFunc("GET /robots.txt", handleRobotsTxt)
	mux.HandleFunc("GET /favicon.ico", handleFavicon)
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /domains", handleDomains)
	mux.HandleFunc("GET /", handleHome)

	server := &http.Server{
		Addr:    serverAddr,
		Handler: corsMiddleware(mux),
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("Server starting on http://localhost%s", serverAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server stopped")
}
