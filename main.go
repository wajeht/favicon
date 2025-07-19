package main

import (
	"database/sql"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
	"github.com/wajeht/favicon/assets"
)

var db *sql.DB

func initDB() error {
	var err error
	db, err = sql.Open("sqlite3", "./favicon.db")
	if err != nil {
		return err
	}

	if err := db.Ping(); err != nil {
		return err
	}

	_, err = db.Exec("PRAGMA journal_mode=WAL;")
	if err != nil {
		return err
	}

	goose.SetBaseFS(assets.Embeddedfiles)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}

	if err := goose.Up(db, "migrations"); err != nil {
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
	query := `
		SELECT data, content_type
		FROM favicons
		WHERE domain = ? AND expires_at > CURRENT_TIMESTAMP
	`

	err := db.QueryRow(query, domain).Scan(&data, &contentType)
	if err != nil {
		return nil, "", err
	}

	return data, contentType, nil
}

func saveFavicon(domain string, data []byte, contentType string) error {
	query := `
		INSERT OR REPLACE INTO favicons (domain, data, content_type, expires_at)
		VALUES (?, ?, ?, datetime('now', '+24 hours'))
	`

	_, err := db.Exec(query, domain, data, contentType)
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

func getFaviconURLs(baseURL string) []string {
	return []string{
		baseURL + "/favicon.ico",
		baseURL + "/favicon.png",
		baseURL + "/apple-touch-icon.png",
		baseURL + "/apple-touch-icon-precomposed.png",
		baseURL + "/apple-touch-icon-120x120.png",
		baseURL + "/apple-touch-icon-152x152.png",
		baseURL + "/apple-touch-icon-180x180.png",
	}
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
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-Cache", "HIT")

		_, err = w.Write(data)
		if err != nil {
			handleServerError(w, r, err)
		}
		return
	}

	baseURL := "https://" + domain
	faviconURLs := getFaviconURLs(baseURL)

	for _, url := range faviconURLs {
		resp, err := http.Get(url)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			continue
		}

		contentType := resp.Header.Get("Content-Type")
		if !isImage(contentType) {
			continue
		}

		faviconData, err := io.ReadAll(resp.Body)
		if err != nil {
			continue
		}

		responseContentType := getContentType(url, contentType)

		if err := saveFavicon(domain, faviconData, responseContentType); err != nil {
			log.Printf("Failed to cache favicon for %s: %v", domain, err)
		}

		w.Header().Set("Content-Type", responseContentType)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-Cache", "MISS")

		_, err = w.Write(faviconData)
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
	w.Header().Set("Cache-Control", "public, max-age=3600")
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
