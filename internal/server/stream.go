package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hardope/itan/internal/config"
)

var contentTypes = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime",
	".webm": "video/webm", ".mkv": "video/x-matroska", ".avi": "video/x-msvideo",
	".ts": "video/mp2t", ".m2ts": "video/mp2t", ".ogv": "video/ogg",
}

// stream serves the raw file (direct play). http.ServeContent handles Range
// requests, so seeking works and the server never buffers the whole file.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	path, err := s.filePath(r.Context(), id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		serverError(w, r, err)
		return
	}
	if ct, ok := contentTypes[strings.ToLower(filepath.Ext(path))]; ok {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
}

// filePath returns the on-disk path of a present file for the item.
func (s *Server) filePath(ctx context.Context, itemID int64) (string, error) {
	var root, rel string
	err := s.db.QueryRowContext(ctx, `
		SELECT l.root_path, f.rel_path FROM files f JOIN libraries l ON l.id = f.library_id
		 WHERE f.item_id = ? AND f.missing_since IS NULL
		 ORDER BY f.last_seen_at DESC LIMIT 1`, itemID).Scan(&root, &rel)
	return filepath.Join(root, rel), err
}

func (s *Server) thumbnail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var hash string
	var duration *int64
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT content_hash, duration_ms FROM items WHERE id = ?`, id).Scan(&hash, &duration); err != nil {
		serverError(w, r, err)
		return
	}
	src, err := s.filePath(r.Context(), id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	var durMS int64
	if duration != nil {
		durMS = *duration
	}
	out, err := s.thumbs.get(r.Context(), hash, src, durMS)
	if err != nil {
		writeError(w, http.StatusNotFound, "no thumbnail")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, out)
}

// thumbnailer extracts one frame per item with ffmpeg, on first request,
// and caches it as <data>/thumbs/<hash>-w640.jpg.
type thumbnailer struct {
	dir    string
	ffmpeg string
	sem    chan struct{} // limits concurrent ffmpeg processes

	mu       sync.Mutex
	inflight map[string]*sync.WaitGroup
	failed   map[string]bool // don't retry broken files until restart
}

func newThumbnailer(cfg *config.Config) *thumbnailer {
	return &thumbnailer{
		dir:      filepath.Join(cfg.DataDir, "thumbs"),
		ffmpeg:   cfg.FFmpeg,
		sem:      make(chan struct{}, 2),
		inflight: map[string]*sync.WaitGroup{},
		failed:   map[string]bool{},
	}
}

var errNoThumb = errors.New("thumbnail unavailable")

func (t *thumbnailer) get(ctx context.Context, hash, src string, durationMS int64) (string, error) {
	out := filepath.Join(t.dir, strings.ReplaceAll(hash, ":", "_")+"-w640.jpg")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}

	t.mu.Lock()
	if t.failed[hash] {
		t.mu.Unlock()
		return "", errNoThumb
	}
	if wg, ok := t.inflight[hash]; ok {
		t.mu.Unlock()
		wg.Wait()
		if _, err := os.Stat(out); err == nil {
			return out, nil
		}
		return "", errNoThumb
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	t.inflight[hash] = wg
	t.mu.Unlock()

	err := t.generate(ctx, src, out, durationMS)

	t.mu.Lock()
	delete(t.inflight, hash)
	if err != nil && ctx.Err() == nil {
		t.failed[hash] = true
		log.Printf("thumbnail %s: %v", src, err)
	}
	t.mu.Unlock()
	wg.Done()

	if err != nil {
		return "", errNoThumb
	}
	return out, nil
}

func (t *thumbnailer) generate(ctx context.Context, src, out string, durationMS int64) error {
	select {
	case t.sem <- struct{}{}:
		defer func() { <-t.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := os.MkdirAll(t.dir, 0o755); err != nil {
		return err
	}
	// 10% in usually skips intros and black frames.
	seek := float64(durationMS) / 1000 * 0.1
	if seek <= 0 {
		seek = 5
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tmp := out + ".tmp.jpg"
	cmd := exec.CommandContext(ctx, t.ffmpeg, "-hide_banner", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.2f", seek), "-i", src,
		"-frames:v", "1", "-vf", "scale=640:-2", "-q:v", "4", "-y", tmp)
	if msg, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(msg)))
	}
	return os.Rename(tmp, out)
}
