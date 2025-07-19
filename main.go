package main

import (
	"io"
	"log"
	"net/http"

	"github.com/wajeht/favicon/assets"
)

func getHomeHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("getHomeHandler()"))
}

func notFoundHandler(w http.ResponseWriter, r *http.Request, err error) {
	message := "The requested resource could not be found"
	http.Error(w, message, http.StatusNotFound)
}

func serverErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	message := "The server encountered a problem and could not process your request"
	http.Error(w, message, http.StatusInternalServerError)
}

func getRobotsDotTxtHandler(w http.ResponseWriter, r *http.Request) {
	f, err := assets.Embeddedfiles.Open("static/favicon.ico")
	if err != nil {
		serverErrorHandler(w, r, err)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "text/plain")
	io.Copy(w, f)

}

func getFaviconDotIcoHandler(w http.ResponseWriter, r *http.Request) {

	f, err := assets.Embeddedfiles.Open("static/favicon.ico")
	if err != nil {
		serverErrorHandler(w, r, err)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "image/x-icon")
	io.Copy(w, f)
}

func main() {
	mux := http.NewServeMux()

	fileServer := http.FileServer(http.FS(assets.Embeddedfiles))

	mux.Handle("GET /static/", fileServer)

	mux.HandleFunc("GET /robots.txt", getRobotsDotTxtHandler)

	mux.HandleFunc("GET /favicon.ico", getFaviconDotIcoHandler)

	mux.HandleFunc("GET /{$}", getHomeHandler)

	log.Println("Server was started on http://localhost")

	err := http.ListenAndServe(":80", mux)

	if err != nil {
		log.Fatal(err)
	}
}
