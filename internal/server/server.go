// Package server exposes the JSON API, video streams and the web UI.
package server

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hardope/itan/internal/config"
	"github.com/hardope/itan/internal/scanner"
)

//go:embed web
var webFS embed.FS

// profileID is the single built-in profile until multi-profile support lands.
const profileID = 1

type Server struct {
	db      *sql.DB
	cfg     *config.Config
	scanner *scanner.Scanner
	thumbs  *thumbnailer
	hls     *hlsManager
}

func New(database *sql.DB, cfg *config.Config, sc *scanner.Scanner) *Server {
	return &Server{db: database, cfg: cfg, scanner: sc, thumbs: newThumbnailer(cfg), hls: newHLSManager(cfg)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/items", s.listItems)
	mux.HandleFunc("GET /api/items/{id}", s.getItem)
	mux.HandleFunc("PATCH /api/items/{id}", s.updateItem)
	mux.HandleFunc("POST /api/items/{id}/progress", s.saveProgress)
	mux.HandleFunc("GET /api/items/{id}/thumbnail", s.thumbnail)
	mux.HandleFunc("GET /api/tags", s.listTags)
	mux.HandleFunc("GET /api/libraries", s.listLibraries)
	mux.HandleFunc("POST /api/libraries", s.addLibrary)
	mux.HandleFunc("PATCH /api/libraries/{id}", s.updateLibrary)
	mux.HandleFunc("DELETE /api/libraries/{id}", s.deleteLibrary)
	mux.HandleFunc("GET /api/export", s.exportLibrary)
	mux.HandleFunc("POST /api/import", s.importLibrary)
	mux.HandleFunc("GET /api/scan", s.scanStatus)
	mux.HandleFunc("POST /api/scan", s.startScan)
	mux.HandleFunc("GET /stream/{id}", s.stream)
	mux.HandleFunc("POST /api/items/{id}/hls", s.startHLS)
	mux.HandleFunc("DELETE /api/hls/{session}", s.stopHLS)
	mux.HandleFunc("GET /hls/{session}/{file}", s.serveHLS)

	static, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(static)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// Embedded files have no modification time, so browsers would cache
		// them by guesswork and keep old UI code after an upgrade.
		if strings.HasPrefix(r.URL.Path, "/vendor/") {
			w.Header().Set("Cache-Control", "public, max-age=2592000") // versioned third-party files
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})

	return logRequests(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// serverError logs the real error and returns a generic 500 (or 404 for no rows).
func serverError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		// Skip noisy paths: the UI polls scan status, players fetch segments constantly.
		if r.URL.Path != "/api/scan" && !strings.HasPrefix(r.URL.Path, "/hls/") {
			log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
		}
	})
}
