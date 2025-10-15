package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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

var (
	repo       *FaviconRepository
	httpClient = &http.Client{
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
)

type FaviconResult struct {
	Data        []byte
	ContentType string
	URL         string
	Error       error
}

type FaviconRepository struct {
	db *sql.DB
}

type Manifest struct {
	Icons []struct {
		Src   string `json:"src"`
		Sizes string `json:"sizes"`
		Type  string `json:"type"`
	} `json:"icons"`
}

func NewFaviconRepository(dbPath string) (*FaviconRepository, error) {
	path := strings.Split(dbPath, "?")[0]
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(100)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA cache_size=10000;",
		"PRAGMA temp_store=MEMORY;",
		"PRAGMA mmap_size=268435456;",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("Warning: Failed to set pragma %s: %v", pragma, err)
		}
	}

	goose.SetBaseFS(assets.Embeddedfiles)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return nil, err
	}

	if err := goose.Up(db, "migrations"); err != nil {
		return nil, err
	}

	repo := &FaviconRepository{db: db}

	return repo, nil
}

func (r *FaviconRepository) Get(domain string) ([]byte, string, error) {
	var data []byte
	var contentType string
	err := r.db.QueryRow(`SELECT data, content_type FROM favicons WHERE domain = ?`, domain).Scan(&data, &contentType)
	if err != nil {
		return nil, "", err
	}
	return data, contentType, nil
}

func (r *FaviconRepository) Save(domain string, data []byte, contentType string) error {
	_, err := r.db.Exec(`INSERT OR REPLACE INTO favicons (domain, data, content_type) VALUES (?, ?, ?)`, domain, data, contentType)
	return err
}

func (r *FaviconRepository) List() (string, error) {
	rows, err := r.db.Query(`SELECT json_group_array(json_object('domain', domain, 'content_type', content_type, 'created_at', created_at)) FROM favicons ORDER BY created_at DESC`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var jsonResult string
	if rows.Next() {
		if err := rows.Scan(&jsonResult); err != nil {
			return "", err
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

	var icons []string
	for _, icon := range manifest.Icons {
		iconURL := icon.Src
		if strings.HasPrefix(iconURL, "./") {
			iconURL = strings.TrimPrefix(iconURL, ".")
		}
		if strings.HasPrefix(iconURL, "/") {
			icons = append(icons, baseURL+iconURL)
		} else {
			parsed, err := url.Parse(iconURL)
			if err == nil && parsed.IsAbs() {
				icons = append(icons, iconURL)
			} else {
				icons = append(icons, baseURL+"/"+iconURL)
			}
		}
	}
	return icons
}

func getHTMLIconLinks(baseURL string) []string {
	resp, err := httpClient.Get(baseURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024)) // Read up to 512KB
	if err != nil {
		return nil
	}

	html := string(body)
	var icons []string

	// Find all <link rel="icon" href="..."> and similar tags
	start := 0
	for {
		idx := strings.Index(html[start:], "<link")
		if idx == -1 {
			break
		}
		start += idx
		end := strings.Index(html[start:], ">")
		if end == -1 {
			break
		}
		tag := html[start : start+end+1]

		// Check if it's an icon link
		if (strings.Contains(tag, `rel="icon"`) || strings.Contains(tag, `rel='icon'`) ||
			strings.Contains(tag, `rel="shortcut icon"`) || strings.Contains(tag, `rel='shortcut icon'`)) {
			// Extract href
			hrefIdx := strings.Index(tag, "href=")
			if hrefIdx != -1 {
				hrefStart := hrefIdx + 5
				if hrefStart < len(tag) {
					quote := tag[hrefStart]
					if quote == '"' || quote == '\'' {
						hrefStart++
						hrefEnd := strings.IndexByte(tag[hrefStart:], quote)
						if hrefEnd != -1 {
							iconURL := tag[hrefStart : hrefStart+hrefEnd]
							if strings.HasPrefix(iconURL, "./") {
								iconURL = strings.TrimPrefix(iconURL, ".")
							}
							if strings.HasPrefix(iconURL, "/") {
								icons = append(icons, baseURL+iconURL)
							} else if strings.HasPrefix(iconURL, "http://") || strings.HasPrefix(iconURL, "https://") {
								icons = append(icons, iconURL)
							} else {
								icons = append(icons, baseURL+"/"+iconURL)
							}
						}
					}
				}
			}
		}
		start += end + 1
	}

	return icons
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

func resizeImage(data []byte, contentType string) ([]byte, error) {
	var img image.Image
	var err error

	switch {
	case strings.Contains(contentType, "png"):
		img, err = png.Decode(bytes.NewReader(data))
	case strings.Contains(contentType, "jpeg") || strings.Contains(contentType, "jpg"):
		img, err = jpeg.Decode(bytes.NewReader(data))
	default:
		return data, nil
	}

	if err != nil {
		return data, nil
	}

	bounds := img.Bounds()
	if bounds.Dx() <= 16 && bounds.Dy() <= 16 {
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

	if err != nil || buf.Len() >= len(data) {
		return data, nil
	}

	return buf.Bytes(), nil
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

	for _, urls := range urlGroups {
		for _, url := range urls {
			wg.Add(1)
			go func(u string) {
				defer wg.Done()
				result := fetchFavicon(ctx, u)
				if result.Error == nil {
					select {
					case resultChan <- result:
					case <-ctx.Done():
					}
				}
			}(url)
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

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
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
	http.Error(w, "Internal server error", http.StatusInternalServerError)
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

func handleDomains(w http.ResponseWriter, r *http.Request) {
	if err := repo.Ping(); err != nil {
		http.Error(w, "Database connection failed", http.StatusServiceUnavailable)
		return
	}

	jsonResult, err := repo.List()
	if err != nil {
		handleServerError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate") // cached for 5 minutes
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(jsonResult))
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		handleNotFound(w, r)
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
		w.Header().Set("X-Favicon-Source", "cached")

		_, err = w.Write(data)
		if err != nil {
			handleServerError(w, r, err)
		}
		return
	}

	baseURL := "https://" + domain
	faviconURLGroups := getFaviconURLs(baseURL, domain)

	if result := fetchFaviconsParallel(faviconURLGroups, 1500*time.Millisecond); result != nil {
		if err := repo.Save(domain, result.Data, result.ContentType); err != nil {
			log.Printf("Failed to cache favicon for %s: %v", domain, err)
		}

		w.Header().Set("Content-Type", result.ContentType)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Cache", "MISS")
		w.Header().Set("X-Favicon-Source", "fetched")

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
	w.Header().Set("X-Favicon-Source", "default")
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

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func main() {
	var err error
	repo, err = NewFaviconRepository("./data/db.sqlite?cache=shared&mode=rwc&_journal_mode=WAL")
	if err != nil {
		log.Fatal(err)
	}
	defer repo.Close()

	mux := http.NewServeMux()
	mux.Handle("GET /static/", stripTrailingSlashMiddleware(http.FileServer(http.FS(assets.Embeddedfiles))))
	mux.HandleFunc("GET /robots.txt", handleRobotsTxt)
	mux.HandleFunc("GET /favicon.ico", handleFavicon)
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /domains", handleDomains)
	mux.HandleFunc("GET /", handleHome)

	server := &http.Server{Addr: ":80", Handler: corsMiddleware(mux)}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Println("Server is running at http://localhost")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}
}
