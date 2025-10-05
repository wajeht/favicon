package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
	"github.com/wajeht/favicon/assets"
	"golang.org/x/image/draw"
)

type FaviconResult struct {
	Data        []byte
	ContentType string
	URL         string
	Error       error
}

var (
	repo *FaviconRepository
)

var httpClient = &http.Client{
	Timeout: 1 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   30,
		MaxConnsPerHost:       50,
		IdleConnTimeout:       60 * time.Second,
		DisableKeepAlives:     false,
		WriteBufferSize:       64 * 1024,
		ReadBufferSize:        64 * 1024,
		TLSHandshakeTimeout:   500 * time.Millisecond,
		ResponseHeaderTimeout: 500 * time.Millisecond,
		ExpectContinueTimeout: 200 * time.Millisecond,
		ForceAttemptHTTP2:     true,
	},
}

type FaviconRepository struct {
	db *sql.DB
}

func NewFaviconRepository(dbPath string) (*FaviconRepository, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(100)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	repo := &FaviconRepository{db: db}

	if err := repo.configurePragmas(); err != nil {
		return nil, err
	}

	if err := repo.runMigrations(); err != nil {
		return nil, err
	}

	if err := repo.CleanupExpired(); err != nil {
		log.Printf("Warning: Failed to cleanup expired favicons: %v", err)
	}

	return repo, nil
}

func (r *FaviconRepository) configurePragmas() error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA cache_size=10000;",
		"PRAGMA temp_store=MEMORY;",
		"PRAGMA mmap_size=268435456;",
	}

	for _, pragma := range pragmas {
		if _, err := r.db.Exec(pragma); err != nil {
			log.Printf("Warning: Failed to set pragma %s: %v", pragma, err)
		}
	}

	return nil
}

func (r *FaviconRepository) runMigrations() error {
	goose.SetBaseFS(assets.Embeddedfiles)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("failed to set goose dialect: %w", err)
	}

	if err := goose.Up(r.db, "migrations"); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

func (r *FaviconRepository) Get(domain string) ([]byte, string, error) {
	query := `SELECT data, content_type FROM favicons WHERE domain = ? AND expires_at > CURRENT_TIMESTAMP`

	var data []byte
	var contentType string
	err := r.db.QueryRow(query, domain).Scan(&data, &contentType)
	if err != nil {
		return nil, "", err
	}
	return data, contentType, nil
}

func (r *FaviconRepository) Save(domain string, data []byte, contentType string) error {
	query := `INSERT OR REPLACE INTO favicons (domain, data, content_type, expires_at) VALUES (?, ?, ?, datetime('now', '+24 hours'))`

	_, err := r.db.Exec(query, domain, data, contentType)
	if err != nil {
		return fmt.Errorf("failed to save favicon: %w", err)
	}
	return nil
}

func (r *FaviconRepository) CleanupExpired() error {
	query := `DELETE FROM favicons WHERE expires_at <= CURRENT_TIMESTAMP`
	result, err := r.db.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to cleanup expired favicons: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		log.Printf("Cleaned up %d expired favicon entries", rowsAffected)
	}

	return nil
}

func (r *FaviconRepository) Ping() error {
	return r.db.Ping()
}

func (r *FaviconRepository) Close() error {
	if r.db != nil {
		return r.db.Close()
	}
	return nil
}

func extractDomain(rawURL string) string {
	url := rawURL
	if len(url) > 8 && url[:8] == "https://" {
		url = url[8:]
	} else if len(url) > 7 && url[:7] == "http://" {
		url = url[7:]
	}

	if slashIndex := strings.IndexByte(url, '/'); slashIndex != -1 {
		url = url[:slashIndex]
	}

	if colonIndex := strings.IndexByte(url, ':'); colonIndex != -1 {
		url = url[:colonIndex]
	}

	if url == "" {
		return rawURL
	}
	return strings.ToLower(url)
}

func getFaviconURLs(baseURL string) [][]string {
	return [][]string{
		{
			baseURL + "/favicon.ico",
			baseURL + "/favicon.png",
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
}

func resizeImage(data []byte, contentType string) ([]byte, error) {
	var img image.Image
	var err error

	reader := bytes.NewReader(data)

	switch {
	case strings.Contains(contentType, "png"):
		img, err = png.Decode(reader)
	case strings.Contains(contentType, "jpeg") || strings.Contains(contentType, "jpg"):
		img, err = jpeg.Decode(reader)
	default:
		return data, nil
	}

	if err != nil {
		return data, nil
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	if width <= 16 && height <= 16 {
		return data, nil
	}

	dst := image.NewRGBA(image.Rect(0, 0, 16, 16))
	draw.NearestNeighbor.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if strings.Contains(contentType, "png") {
		err = png.Encode(&buf, dst)
	} else {
		err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 90})
	}

	if err != nil {
		return data, nil
	}

	if buf.Len() < len(data) {
		return buf.Bytes(), nil
	}

	return data, nil
}

func fetchFavicon(ctx context.Context, url string) FaviconResult {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return FaviconResult{Error: err, URL: url}
	}

	req.Header.Set("User-Agent", "FaviconBot/1.0")
	req.Header.Set("Accept", "image/*")

	resp, err := httpClient.Do(req)
	if err != nil {
		return FaviconResult{Error: err, URL: url}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return FaviconResult{Error: fmt.Errorf("status %d", resp.StatusCode), URL: url}
	}

	contentType := resp.Header.Get("Content-Type")
	if !isImage(contentType) {
		return FaviconResult{Error: fmt.Errorf("not an image"), URL: url}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return FaviconResult{Error: err, URL: url}
	}

	optimizedData, _ := resizeImage(data, contentType)

	return FaviconResult{
		Data:        optimizedData,
		ContentType: getContentType(url, contentType),
		URL:         url,
	}
}

func fetchFaviconsParallel(urlGroups [][]string, timeout time.Duration) *FaviconResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	resultChan := make(chan FaviconResult, 10)

	var wg sync.WaitGroup

	for groupIdx, urls := range urlGroups {
		for _, url := range urls {
			wg.Add(1)
			go func(u string, priority int) {
				defer wg.Done()

				result := fetchFavicon(ctx, u)
				if result.Error == nil {
					select {
					case resultChan <- result:
					case <-ctx.Done():
					}
				}
			}(url, groupIdx)
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

func getContentType(url string, respContentType string) string {
	if respContentType != "" {
		return respContentType
	}

	if strings.HasSuffix(url, ".png") {
		return "image/png"
	}

	return "image/x-icon"
}

func isImage(contentType string) bool {
	if contentType == "" {
		return false
	}

	contentType = strings.ToLower(strings.Split(contentType, ";")[0])
	switch contentType {
	case "image/x-icon", "image/vnd.microsoft.icon", "image/icon", "image/ico":
		return true
	case "image/png", "image/jpeg", "image/jpg", "image/gif":
		return true
	case "image/svg+xml", "image/webp":
		return true
	default:
		return false
	}
}

func stripTrailingSlashMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") && r.URL.Path != "/static/" {
			handleNotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleNotFound(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "The requested resource could not be found", http.StatusNotFound)
}

func handleServerError(w http.ResponseWriter, r *http.Request, err error) {
	log.Println("Internal server error:", err)
}

func handleRobotsTxt(w http.ResponseWriter, r *http.Request) {
	file, err := assets.Embeddedfiles.Open("static/robots.txt")
	if err != nil {
		handleServerError(w, r, err)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "text/plain")
	_, err = io.Copy(w, file)
	if err != nil {
		handleServerError(w, r, err)
	}
}

func handleFavicon(w http.ResponseWriter, r *http.Request) {
	file, err := assets.Embeddedfiles.Open("static/favicon.ico")
	if err != nil {
		handleServerError(w, r, err)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "image/x-icon")
	_, err = io.Copy(w, file)
	if err != nil {
		handleServerError(w, r, err)
	}
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		http.Error(w, "Missing 'url' query parameter. Usage: /?url=<url>", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	domain := extractDomain(rawURL)

	if data, contentType, err := repo.Get(domain); err == nil {
		etag := fmt.Sprintf(`"fav-%s"`, domain)

		// Check ETag match (handle Cloudflare's W/ prefix)
		clientETag := r.Header.Get("If-None-Match")
		if clientETag == etag || clientETag == "W/"+etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
		w.Header().Set("ETag", etag)
		w.Header().Set("X-Cache", "HIT")

		_, err = w.Write(data)
		if err != nil {
			handleServerError(w, r, err)
		}
		return
	}

	baseURL := "https://" + domain
	faviconURLGroups := getFaviconURLs(baseURL)

	if result := fetchFaviconsParallel(faviconURLGroups, 180*time.Millisecond); result != nil {
		if err := repo.Save(domain, result.Data, result.ContentType); err != nil {
			log.Printf("Failed to cache favicon for %s: %v", domain, err)
		}

		w.Header().Set("Content-Type", result.ContentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Cache", "MISS")

		_, err := w.Write(result.Data)
		if err != nil {
			handleServerError(w, r, err)
		}
		return
	}

	file, err := assets.Embeddedfiles.Open("static/favicon.ico")
	if err != nil {
		handleServerError(w, r, err)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Cache", "DEFAULT")
	_, err = io.Copy(w, file)
	if err != nil {
		handleServerError(w, r, err)
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := repo.Ping(); err != nil {
		http.Error(w, "Database connection failed", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("ok"))
}

func main() {
	var err error
	repo, err = NewFaviconRepository("/data/db.sqlite?cache=shared&mode=rwc&_journal_mode=WAL")
	if err != nil {
		log.Fatal("Failed to initialize database:", err)
	}
	defer repo.Close()

	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := repo.CleanupExpired(); err != nil {
					log.Printf("Periodic cleanup failed: %v", err)
				}
			case <-cleanupCtx.Done():
				return
			}
		}
	}()

	mux := http.NewServeMux()

	staticHandler := http.FileServer(http.FS(assets.Embeddedfiles))
	mux.Handle("GET /static/", stripTrailingSlashMiddleware(staticHandler))
	mux.HandleFunc("GET /robots.txt", handleRobotsTxt)
	mux.HandleFunc("GET /favicon.ico", handleFavicon)
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /", handleHome)

	server := &http.Server{
		Addr:    ":80",
		Handler: mux,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Println("Server is running at http://localhost")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("Server failed to start:", err)
		}
	}()

	<-quit
	log.Println("Shutting down server...")

	cleanupCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
		return
	}

	log.Println("Server gracefully stopped")
}
