package main

import (
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/wajeht/favicon/assets"
)

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
		w.Header().Set("Content-Type", responseContentType)
		w.Header().Set("Cache-Control", "public, max-age=3600")

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
	_, err = io.Copy(w, file)
	if err != nil {
		handleServerError(w, r, err)
	}
}

func main() {
	mux := http.NewServeMux()

	staticHandler := http.FileServer(http.FS(assets.Embeddedfiles))
	mux.Handle("GET /static/", stripTrailingSlashMiddleware(staticHandler))
	mux.HandleFunc("GET /robots.txt", handleRobotsTxt)
	mux.HandleFunc("GET /favicon.ico", handleFavicon)
	mux.HandleFunc("GET /", handleHome)

	log.Println("Server is running at http://localhost")
	err := http.ListenAndServe(":80", mux)
	if err != nil {
		log.Fatal("Server failed to start:", err)
	}
}
