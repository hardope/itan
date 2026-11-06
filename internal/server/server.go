// Package server exposes the JSON API, video streams and the web UI.
package server

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
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

// SourceURL is where Itan's source code is. The AGPL asks anyone running a
// changed version for others to offer its source: point this at yours.
const SourceURL = "https://github.com/hardope/itan"

type Server struct {
	db      *sql.DB
	cfg     *config.Config
	scanner *scanner.Scanner
	thumbs  *thumbnailer
	hls     *hlsManager
	recs    recCache
	folders *folderWriter
	pins    *pinGuard
}

func New(database *sql.DB, cfg *config.Config, sc *scanner.Scanner) *Server {
	return &Server{db: database, cfg: cfg, scanner: sc, thumbs: newThumbnailer(cfg), hls: newHLSManager(cfg),
		folders: newFolderWriter(database), pins: newPinGuard()}
}

// Close finishes background work that shouldn't be lost on shutdown.
func (s *Server) Close() { s.folders.Close() }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/items", s.listItems)
	mux.HandleFunc("GET /api/items/{id}", s.getItem)
	mux.HandleFunc("PATCH /api/items/{id}", s.requireEdit(s.updateItem))
	mux.HandleFunc("POST /api/items/{id}/progress", s.saveProgress)
	mux.HandleFunc("GET /api/items/{id}/thumbnail", s.thumbnail)
	mux.HandleFunc("POST /api/items/{id}/thumbnail", s.requireEdit(s.setThumbnail))
	mux.HandleFunc("POST /api/thumbnails/regenerate", s.requireEdit(s.regenerateThumbnails))
	mux.HandleFunc("GET /api/items/{id}/similar", s.similarItems)
	mux.HandleFunc("PUT /api/items/{id}/favorite", s.setFavorite)
	mux.HandleFunc("GET /api/recommendations", s.recommendations)
	mux.HandleFunc("GET /api/series", s.listSeries)
	mux.HandleFunc("GET /api/series/{id}", s.getSeries)
	mux.HandleFunc("PATCH /api/series/{id}", s.requireEdit(s.updateSeries))
	mux.HandleFunc("DELETE /api/series/{id}", s.requireEdit(s.deleteSeries))
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/tags", s.listTags)
	mux.HandleFunc("GET /api/libraries", s.listLibraries)
	mux.HandleFunc("POST /api/libraries", s.requireEdit(s.addLibrary))
	mux.HandleFunc("PATCH /api/libraries/{id}", s.requireEdit(s.updateLibrary))
	mux.HandleFunc("DELETE /api/libraries/{id}", s.requireEdit(s.deleteLibrary))
	mux.HandleFunc("GET /api/folders", s.requireEdit(s.browseFolders))
	mux.HandleFunc("GET /api/export", s.requireEdit(s.exportLibrary))
	mux.HandleFunc("POST /api/import", s.requireEdit(s.importLibrary))
	mux.HandleFunc("GET /api/auth", s.authState)
	mux.HandleFunc("POST /api/auth/unlock", s.unlock)
	mux.HandleFunc("POST /api/auth/lock", s.lock)
	mux.HandleFunc("PUT /api/auth/pin", s.setPIN)
	mux.HandleFunc("DELETE /api/auth/pin", s.removePIN)
	mux.HandleFunc("GET /api/scan", s.scanStatus)
	mux.HandleFunc("POST /api/scan", s.startScan)
	mux.HandleFunc("GET /stream/{id}", s.stream)
	mux.HandleFunc("POST /api/items/{id}/hls", s.startHLS)
	mux.HandleFunc("DELETE /api/hls/{session}", s.stopHLS)
	mux.HandleFunc("GET /hls/{session}/{file}", s.serveHLS)

	static, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(static)
	mux.HandleFunc("GET /{$}", s.index)
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

// index serves the page with this device's edit lock state on <html
// data-auth="none|locked|unlocked">, so the header is right from the first
// frame instead of changing once the UI has asked.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	page, err := webFS.ReadFile("web/index.html")
	if err != nil {
		serverError(w, r, err)
		return
	}
	state := "none"
	if stored, err := s.storedPIN(r.Context()); err == nil && stored != "" {
		state = "locked"
		if ok, _ := s.unlocked(r); ok {
			state = "unlocked"
		}
	}
	page = bytes.Replace(page, []byte(`<html lang="en">`), []byte(`<html lang="en" data-auth="`+state+`">`), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(page)
}

// LANURLs lists the URLs other devices on the local network can use.
func LANURLs(addr string) []string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	var urls []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
			urls = append(urls, fmt.Sprintf("http://%s:%s", ipn.IP, port))
		}
	}
	return urls
}

// info tells the UI how other devices can reach this server, and which
// version it is.
func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	urls := LANURLs(s.cfg.Addr)
	if urls == nil {
		urls = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lan_urls": urls, "version": s.cfg.Version, "source": SourceURL})
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
