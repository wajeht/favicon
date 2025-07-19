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
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
	"github.com/wajeht/favicon/assets"
	"golang.org/x/image/draw"
)

var db *sql.DB

var getFaviconStmt *sql.Stmt
var saveFaviconStmt *sql.Stmt

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

type FaviconResult struct {
	Data        []byte
	ContentType string
	URL         string
	Error       error
}

func initDB() error {
	var err error
	db, err = sql.Open("sqlite3", "./db.sqlite")
	if err != nil {
		return err
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return err
	}

	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA cache_size=10000;",
		"PRAGMA temp_store=MEMORY;",
		"PRAGMA mmap_size=268435456;",
	}

	for _, pragma := range pragmas {
		if _, err = db.Exec(pragma); err != nil {
			log.Printf("Warning: Failed to set pragma %s: %v", pragma, err)
		}
	}

	goose.SetBaseFS(assets.Embeddedfiles)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}

	if err := goose.Up(db, "migrations"); err != nil {
		return err
	}

	getFaviconStmt, err = db.Prepare(`
	SELECT data, content_type
	FROM favicons
	WHERE domain = ? AND expires_at > CURRENT_TIMESTAMP
`)
	if err != nil {
		return err
	}

	saveFaviconStmt, err = db.Prepare(`
	INSERT OR REPLACE INTO favicons (domain, data, content_type, expires_at)
	VALUES (?, ?, ?, datetime('now', '+24 hours'))
`)
	if err != nil {
		return err
	}

	if err := cleanupExpiredFavicons(); err != nil {
		log.Printf("Warning: Failed to cleanup expired favicons: %v", err)
	}

	return nil
}

func cleanupExpiredFavicons() error {
	query := `DELETE FROM favicons WHERE expires_at <= CURRENT_TIMESTAMP`
	result, err := db.Exec(query)
	if err != nil {
		return err
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		log.Printf("Cleaned up %d expired favicon entries", rowsAffected)
	}

	return nil
}

func getCachedFavicon(domain string) ([]byte, string, error) {
	var data []byte
	var contentType string
	err := getFaviconStmt.QueryRow(domain).Scan(&data, &contentType)
	if err != nil {
		return nil, "", err
	}
	return data, contentType, nil
}

func saveFavicon(domain string, data []byte, contentType string) error {
	_, err := saveFaviconStmt.Exec(domain, data, contentType)
	return err
}

func extractDomain(rawURL string) string {
	url := rawURL
	if strings.HasPrefix(url, "http://") {
		url = url[7:]
	} else if strings.HasPrefix(url, "https://") {
		url = url[8:]
	}

	if slashIndex := strings.Index(url, "/"); slashIndex != -1 {
		url = url[:slashIndex]
	}

	if colonIndex := strings.Index(url, ":"); colonIndex != -1 {
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
	http.Error(w, "The server encountered a problem and could not process your request", http.StatusInternalServerError)
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

	if data, contentType, err := getCachedFavicon(domain); err == nil {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
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
		if err := saveFavicon(domain, result.Data, result.ContentType); err != nil {
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
	if err := db.Ping(); err != nil {
		http.Error(w, "Database connection failed", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("ok"))
}

func main() {
	if err := initDB(); err != nil {
		log.Fatal("Failed to initialize database:", err)
	}
	defer db.Close()
	defer getFaviconStmt.Close()
	defer saveFaviconStmt.Close()

	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := cleanupExpiredFavicons(); err != nil {
				log.Printf("Periodic cleanup failed: %v", err)
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

	log.Println("Server is running at http://localhost")
	err := http.ListenAndServe(":80", mux)
	if err != nil {
		log.Fatal("Server failed to start:", err)
	}
}
