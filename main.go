package main

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/wajeht/favicon/assets"
)

func extractDomainAndTLD(rawUrl string) string {
	parsedUrl, err := url.Parse(rawUrl)
	if err != nil {
		return rawUrl
	}
	return parsedUrl.Hostname()
}

func stripTrailingSlashMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
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
	io.Copy(w, file)
}

func handleFavicon(w http.ResponseWriter, r *http.Request) {
	file, err := assets.Embeddedfiles.Open("static/favicon.ico")
	if err != nil {
		handleServerError(w, r, err)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", "image/x-icon")
	io.Copy(w, file)
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	rawUrl := r.URL.Query().Get("url")

	if rawUrl == "" {
		http.Error(w, "URL must not be empty", http.StatusUnprocessableEntity)
		return
	}

	domain := extractDomainAndTLD(rawUrl)

	w.Write([]byte(domain))
}

func main() {
	mux := http.NewServeMux()

	staticHandler := http.FileServer(http.FS(assets.Embeddedfiles))

	mux.Handle("GET /static/", stripTrailingSlashMiddleware(staticHandler))
	mux.HandleFunc("GET /robots.txt", handleRobotsTxt)
	mux.HandleFunc("GET /favicon.ico", handleFavicon)
	mux.HandleFunc("GET /{$}", handleHome)

	log.Println("Server is running at http://localhost")
	err := http.ListenAndServe(":80", mux)
	if err != nil {
		log.Fatal(err)
	}
}
