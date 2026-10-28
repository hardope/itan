package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hardope/itan/internal/recommend"
)

// recCacheTTL bounds how stale library features can get after a scan.
// Edits made through the API invalidate the cache right away.
const recCacheTTL = time.Minute

// recCache holds the library's feature vectors, the expensive part of a
// recommendation. Watch history is read fresh for every request.
type recCache struct {
	mu  sync.Mutex
	lib *recommend.Library
	at  time.Time
}

func (c *recCache) model(ctx context.Context, d *sql.DB) (*recommend.Model, error) {
	c.mu.Lock()
	if c.lib == nil || time.Since(c.at) > recCacheTTL {
		lib, err := recommend.LoadLibrary(ctx, d)
		if err != nil {
			c.mu.Unlock()
			return nil, err
		}
		c.lib, c.at = lib, time.Now()
	}
	lib := c.lib
	c.mu.Unlock()
	return lib.Model(ctx, d, profileID, time.Now())
}

func (c *recCache) invalidate() {
	c.mu.Lock()
	c.lib = nil
	c.mu.Unlock()
}

type shelfJSON struct {
	ID    string         `json:"id"`
	Title string         `json:"title"`
	Items []*itemSummary `json:"items"`
}

// recommendations returns the home page shelves. limit= items per shelf.
func (s *Server) recommendations(w http.ResponseWriter, r *http.Request) {
	m, err := s.recs.model(r.Context(), s.db)
	if err != nil {
		serverError(w, r, err)
		return
	}
	shelves := []shelfJSON{}
	for _, sh := range m.Shelves(clamp(atoi(r.URL.Query().Get("limit"), 12), 1, 50)) {
		items, err := s.recSummaries(r.Context(), sh.Items)
		if err != nil {
			serverError(w, r, err)
			return
		}
		shelves = append(shelves, shelfJSON{ID: sh.ID, Title: sh.Title, Items: items})
	}
	writeJSON(w, http.StatusOK, map[string]any{"shelves": shelves})
}

// similarItems is the item page's "More like this".
func (s *Server) similarItems(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	m, err := s.recs.model(r.Context(), s.db)
	if err != nil {
		serverError(w, r, err)
		return
	}
	items, err := s.recSummaries(r.Context(), m.Similar(id, clamp(atoi(r.URL.Query().Get("limit"), 12), 1, 50), nil, false))
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// recSummaries loads cards for recommendations, in recommendation order.
func (s *Server) recSummaries(ctx context.Context, recs []recommend.Rec) ([]*itemSummary, error) {
	if len(recs) == 0 {
		return []*itemSummary{}, nil
	}
	ids := make([]string, len(recs))
	for i, rec := range recs {
		ids[i] = strconv.FormatInt(rec.ItemID, 10)
	}
	items, err := s.querySummaries(ctx, `WHERE i.id IN (`+strings.Join(ids, ",")+`)`, profileID)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*itemSummary, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	out := make([]*itemSummary, 0, len(recs))
	for _, rec := range recs {
		if it := byID[rec.ItemID]; it != nil {
			it.Reason = rec.Reason
			out = append(out, it)
		}
	}
	return out, nil
}

// setFavorite marks or unmarks a favorite. Body: {"favorite": true}
func (s *Server) setFavorite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		Favorite bool `json:"favorite"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	_, err := s.db.ExecContext(r.Context(), `
		INSERT INTO play_state(profile_id, item_id, favorite)
		SELECT ?, id, ? FROM items WHERE id = ?
		ON CONFLICT(profile_id, item_id) DO UPDATE SET favorite = excluded.favorite`,
		profileID, body.Favorite, id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"favorite": body.Favorite})
}
