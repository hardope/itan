package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hardope/itan/internal/config"
	"github.com/hardope/itan/internal/db"
	"github.com/hardope/itan/internal/proc"
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
	out, err := s.thumbnailFor(r.Context(), id)
	if errors.Is(err, errNoThumb) {
		writeError(w, http.StatusNotFound, "no thumbnail")
		return
	} else if err != nil {
		serverError(w, r, err)
		return
	}
	// Thumbnails can be regenerated: browsers check for a newer one (a
	// quick "not modified" when there isn't) rather than keep it a day.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, out)
}

// thumbnailFor returns the path of an item's thumbnail, making it first
// if needed.
func (s *Server) thumbnailFor(ctx context.Context, id int64) (string, error) {
	var hash string
	var duration, at sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT content_hash, duration_ms, thumbnail_at_ms FROM items WHERE id = ?`, id).Scan(&hash, &duration, &at); err != nil {
		return "", err
	}
	src, err := s.filePath(ctx, id)
	if err != nil {
		return "", err
	}
	return s.thumbs.get(ctx, hash, src, duration.Int64, at.Int64)
}

// thumbnailMoments are where "another moment" looks in turn, as fractions
// of the video; the default thumbnail is at the first.
var thumbnailMoments = []float64{0.1, 0.25, 0.4, 0.55, 0.7, 0.85}

// setThumbnail makes a new thumbnail for an item: from the frame at
// at_ms (the one on screen in the player), or else from the next of
// thumbnailMoments. The choice is kept for regenerating.
func (s *Server) setThumbnail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		AtMS *int64 `json:"at_ms"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	var hash string
	var duration, cur sql.NullInt64
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT content_hash, duration_ms, thumbnail_at_ms FROM items WHERE id = ?`, id).Scan(&hash, &duration, &cur); err != nil {
		serverError(w, r, err)
		return
	}
	var at sql.NullInt64
	switch {
	case body.AtMS != nil:
		at = sql.NullInt64{Int64: max(0, *body.AtMS), Valid: true}
		if duration.Int64 > 0 {
			at.Int64 = min(at.Int64, duration.Int64-1)
		}
	case duration.Int64 > 0:
		now := thumbnailMoments[0]
		if cur.Valid {
			now = float64(cur.Int64) / float64(duration.Int64)
		}
		next := thumbnailMoments[0]
		for _, m := range thumbnailMoments {
			if m > now+0.01 {
				next = m
				break
			}
		}
		if next != thumbnailMoments[0] {
			at = sql.NullInt64{Int64: int64(next * float64(duration.Int64)), Valid: true}
		}
	}
	err := db.Tx(r.Context(), s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `UPDATE items SET thumbnail_at_ms = ? WHERE id = ?`, at, id); err != nil {
			return err
		}
		// An edit like any other: it goes in backups and folder files.
		_, err := tx.ExecContext(r.Context(), `
			INSERT INTO item_field_sources(item_id, field, source, updated_at) VALUES (?, 'thumbnail_at_ms', 'user', unixepoch())
			ON CONFLICT(item_id, field) DO UPDATE SET source = 'user', updated_at = unixepoch()`, id)
		return err
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	s.folders.itemChanged(id)
	s.thumbs.forget(hash)
	if _, err := s.thumbnailFor(r.Context(), id); err != nil {
		if errors.Is(err, errNoThumb) || errors.Is(err, sql.ErrNoRows) { // no frame, or no file
			writeError(w, http.StatusUnprocessableEntity, "Couldn't take a frame from this video. Is its drive connected?")
			return
		}
		serverError(w, r, err)
		return
	}
	var chosen any // null: the default frame
	if at.Valid {
		chosen = at.Int64
	}
	writeJSON(w, http.StatusOK, map[string]any{"thumbnail_at_ms": chosen})
}

// regenerateThumbnails removes every thumbnail, so each is made again the
// next time it's shown (from the frame chosen for it, if any), including
// ones that failed before.
func (s *Server) regenerateThumbnails(w http.ResponseWriter, r *http.Request) {
	n, err := s.thumbs.clear()
	if err != nil {
		serverError(w, r, err)
		return
	}
	log.Printf("thumbnails: removed %d to make again", n)
	writeJSON(w, http.StatusOK, map[string]int{"removed": n})
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

// path is where the thumbnail taken at atMS (0: the default moment) is
// kept. Each moment has its own file, so a moment chosen elsewhere and
// imported shows without anything to clear.
func (t *thumbnailer) path(hash string, atMS int64) string {
	name := strings.ReplaceAll(hash, ":", "_")
	if atMS > 0 {
		name += fmt.Sprintf("-at%d", atMS)
	}
	return filepath.Join(t.dir, name+"-w640.jpg")
}

// get returns the thumbnail for hash, taken from src at atMS (or 10% in
// when that's 0).
func (t *thumbnailer) get(ctx context.Context, hash, src string, durationMS, atMS int64) (string, error) {
	out := t.path(hash, atMS)
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

	err := t.generate(ctx, src, out, durationMS, atMS)

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

// forget removes hash's thumbnails, and that it failed, if it did.
func (t *thumbnailer) forget(hash string) {
	t.mu.Lock()
	delete(t.failed, hash)
	t.mu.Unlock()
	old, _ := filepath.Glob(filepath.Join(t.dir, strings.ReplaceAll(hash, ":", "_")+"-*.jpg"))
	for _, f := range old {
		os.Remove(f)
	}
}

// clear removes every thumbnail and every failure, returning how many
// thumbnails there were.
func (t *thumbnailer) clear() (int, error) {
	t.mu.Lock()
	t.failed = map[string]bool{}
	t.mu.Unlock()
	entries, err := os.ReadDir(t.dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jpg") && os.Remove(filepath.Join(t.dir, e.Name())) == nil {
			n++
		}
	}
	return n, nil
}

func (t *thumbnailer) generate(ctx context.Context, src, out string, durationMS, atMS int64) error {
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
	seek := float64(durationMS) / 1000 * thumbnailMoments[0]
	if atMS > 0 {
		seek = float64(atMS) / 1000
	} else if seek <= 0 {
		seek = 5
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tmp := out + ".tmp.jpg"
	cmd := proc.Command(ctx, t.ffmpeg, "-hide_banner", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.2f", seek), "-i", src,
		"-frames:v", "1", "-vf", "scale=640:-2", "-q:v", "4", "-y", tmp)
	if msg, err := cmd.CombinedOutput(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(msg)))
	}
	return os.Rename(tmp, out)
}
