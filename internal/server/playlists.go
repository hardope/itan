package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hardope/itan/internal/db"
)

// Playlists are collections of kind 'playlist': any videos, in the order
// someone put them, played one after another. A video is in a playlist at
// most once. collection_items.position is the order, renumbered 1, 2, 3...
// on every change.
//
// Videos whose files are missing (a drive that isn't plugged in) stay in the
// playlist but aren't listed or played, as for series.

// playlistMembers selects the videos of playlists that have a file on disk:
// columns playlist_id, position, item_id.
const playlistMembers = `
	SELECT ci.collection_id AS playlist_id, ci.position, ci.item_id
	  FROM collection_items ci
	  JOIN collections c ON c.id = ci.collection_id AND c.kind = 'playlist'
	 WHERE EXISTS (SELECT 1 FROM files f WHERE f.item_id = ci.item_id AND f.missing_since IS NULL)`

type playlistJSON struct {
	ID          int64   `json:"id"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Count       int     `json:"count"` // videos that can be played now
	DurationMS  int64   `json:"duration_ms"`
	Watched     int     `json:"watched"`
	CoverID     *int64  `json:"cover_id"` // the first video, for the thumbnail
	UpdatedAt   int64   `json:"updated_at"`
	// With ?items=: how many of those videos are in this playlist.
	Contains *int `json:"contains,omitempty"`
}

type playlistDetail struct {
	playlistJSON
	Items []*itemSummary `json:"items"`
	// Next is what "Play" plays: the video in progress, or the one after
	// the last one watched, or the first.
	Next     *itemSummary `json:"next"`
	NextVerb string       `json:"next_verb"`
}

// loadPlaylists returns playlists, most recently changed first: all of
// them, or the one with id onlyID. contains, if not nil, are videos to
// count in each.
func (s *Server) loadPlaylists(ctx context.Context, onlyID int64, contains []int64) ([]*playlistJSON, error) {
	containsCol := "NULL"
	if contains != nil {
		containsCol = `(SELECT COUNT(*) FROM collection_items x WHERE x.collection_id = c.id AND x.item_id IN (` + idList(contains) + `))`
	}
	where, args := "", []any{profileID}
	if onlyID > 0 {
		where, args = "AND c.id = ?", append(args, onlyID)
	}
	out := []*playlistJSON{}
	err := eachRow(ctx, s.db, `
		WITH m AS (`+playlistMembers+`)
		SELECT c.id, c.title, c.description, c.updated_at,
		       COUNT(m.item_id), COALESCE(SUM(i.duration_ms), 0), COALESCE(SUM(ps.completed), 0),
		       (SELECT f.item_id FROM m f WHERE f.playlist_id = c.id ORDER BY f.position, f.item_id LIMIT 1),
		       `+containsCol+`
		  FROM collections c
		  LEFT JOIN m ON m.playlist_id = c.id
		  LEFT JOIN items i ON i.id = m.item_id
		  LEFT JOIN play_state ps ON ps.item_id = m.item_id AND ps.profile_id = ?
		 WHERE c.kind = 'playlist' `+where+`
		 GROUP BY c.id
		 ORDER BY c.updated_at DESC, c.id DESC`, args, func(rows *sql.Rows) error {
		p := &playlistJSON{}
		var n sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Title, &p.Description, &p.UpdatedAt,
			&p.Count, &p.DurationMS, &p.Watched, &p.CoverID, &n); err != nil {
			return err
		}
		if n.Valid {
			c := int(n.Int64)
			p.Contains = &c
		}
		out = append(out, p)
		return nil
	})
	return out, err
}

// listPlaylists returns every playlist. ?items=1,2,3 adds "contains", for
// a picker that shows which playlists already have those videos.
func (s *Server) listPlaylists(w http.ResponseWriter, r *http.Request) {
	var contains []int64
	if v := r.URL.Query().Get("items"); v != "" {
		contains = []int64{}
		for _, part := range strings.Split(v, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && id > 0 {
				contains = append(contains, id)
			}
		}
	}
	list, err := s.loadPlaylists(r.Context(), 0, contains)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getPlaylist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	s.writePlaylist(w, r, id, http.StatusOK)
}

// writePlaylist answers with a playlist's details: its videos in order and
// what to play next.
func (s *Server) writePlaylist(w http.ResponseWriter, r *http.Request, id int64, status int) {
	ctx := r.Context()
	list, err := s.loadPlaylists(ctx, id, nil)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if len(list) == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	d := &playlistDetail{playlistJSON: *list[0]}
	d.Items, err = s.querySummaries(ctx, `
		JOIN collection_items pl ON pl.item_id = i.id AND pl.collection_id = ?
		WHERE EXISTS (SELECT 1 FROM files f WHERE f.item_id = i.id AND f.missing_since IS NULL)
		ORDER BY pl.position, i.id`, profileID, id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	lastPlayed := map[int64]int64{}
	err = eachRow(ctx, s.db, `SELECT item_id, COALESCE(last_played_at, 0) FROM play_state WHERE profile_id = ?
		AND item_id IN (SELECT item_id FROM collection_items WHERE collection_id = ?)`, []any{profileID, id},
		func(rows *sql.Rows) error {
			var item, at int64
			if err := rows.Scan(&item, &at); err != nil {
				return err
			}
			lastPlayed[item] = at
			return nil
		})
	if err != nil {
		serverError(w, r, err)
		return
	}
	d.Next, d.NextVerb = nextInOrder(d.Items, lastPlayed)
	writeJSON(w, status, d)
}

type playlistBody struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Items       []int64 `json:"items"` // create: the first videos
	Order       []int64 `json:"order"` // update: the new order
}

func decodePlaylistBody(w http.ResponseWriter, r *http.Request) (*playlistBody, bool) {
	var b playlistBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, `invalid JSON: expected {"title": "...", "items": [1, 2]}`)
		return nil, false
	}
	return &b, true
}

// createPlaylist makes a playlist: {"title": "...", "description": "...",
// "items": [ids]}. Only the title is needed.
func (s *Server) createPlaylist(w http.ResponseWriter, r *http.Request) {
	b, ok := decodePlaylistBody(w, r)
	if !ok {
		return
	}
	title := ""
	if b.Title != nil {
		title = strings.TrimSpace(*b.Title)
	}
	if title == "" {
		writeError(w, http.StatusBadRequest, "The playlist needs a name.")
		return
	}
	ctx := r.Context()
	var id int64
	err := db.Tx(ctx, s.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `INSERT INTO collections(uid, kind, title, description) VALUES (?, 'playlist', ?, ?) RETURNING id`,
			db.NewUID(), title, optionalText(b.Description)).Scan(&id); err != nil {
			return err
		}
		return addToPlaylist(ctx, tx, id, b.Items)
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	s.recs.invalidate()
	s.writePlaylist(w, r, id, http.StatusCreated)
}

// updatePlaylist renames, describes or reorders a playlist:
// {"title": "...", "description": "...", "order": [ids]}.
//
// "order" lists videos of the playlist in their new order. Videos it leaves
// out (such as ones whose drive isn't plugged in, which the page doesn't
// show) keep their place; the listed ones fill the places they had between
// them.
func (s *Server) updatePlaylist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	b, ok := decodePlaylistBody(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := s.editPlaylist(ctx, id, func(tx *sql.Tx) error {
		if b.Title != nil {
			t := strings.TrimSpace(*b.Title)
			if t == "" {
				return badRequest("The playlist needs a name.")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE collections SET title = ? WHERE id = ?`, t, id); err != nil {
				return err
			}
		}
		if b.Description != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE collections SET description = ? WHERE id = ?`, optionalText(b.Description), id); err != nil {
				return err
			}
		}
		if b.Order == nil {
			return nil
		}
		cur, err := playlistOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		member := map[int64]bool{}
		for _, item := range cur {
			member[item] = true
		}
		var moved []int64
		listed := map[int64]bool{}
		for _, item := range b.Order {
			if member[item] && !listed[item] {
				listed[item] = true
				moved = append(moved, item)
			}
		}
		next := make([]int64, 0, len(cur))
		for _, item := range cur {
			if listed[item] {
				item, moved = moved[0], moved[1:]
			}
			next = append(next, item)
		}
		return renumberPlaylist(ctx, tx, id, next)
	})
	if br, ok := err.(badRequest); ok {
		writeError(w, http.StatusBadRequest, string(br))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	s.writePlaylist(w, r, id, http.StatusOK)
}

func (s *Server) deletePlaylist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM collections WHERE id = ? AND kind = 'playlist'`, id); err != nil {
		serverError(w, r, err)
		return
	}
	s.recs.invalidate()
	w.WriteHeader(http.StatusNoContent)
}

// addPlaylistItems adds videos at the end, in the order given:
// {"items": [ids]}. Videos already in the playlist stay where they are.
func (s *Server) addPlaylistItems(w http.ResponseWriter, r *http.Request) {
	s.changeMembers(w, r, addToPlaylist)
}

// removePlaylistItems takes videos out: {"items": [ids]}.
func (s *Server) removePlaylistItems(w http.ResponseWriter, r *http.Request) {
	s.changeMembers(w, r, func(ctx context.Context, tx *sql.Tx, id int64, items []int64) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM collection_items WHERE collection_id = ? AND item_id IN (`+idList(items)+`)`, id); err != nil {
			return err
		}
		cur, err := playlistOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		return renumberPlaylist(ctx, tx, id, cur)
	})
}

func (s *Server) changeMembers(w http.ResponseWriter, r *http.Request, change func(context.Context, *sql.Tx, int64, []int64) error) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	b, ok := decodePlaylistBody(w, r)
	if !ok {
		return
	}
	err := s.editPlaylist(r.Context(), id, func(tx *sql.Tx) error {
		return change(r.Context(), tx, id, b.Items)
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	s.writePlaylist(w, r, id, http.StatusOK)
}

// editPlaylist runs fn in a transaction if the playlist exists (otherwise
// sql.ErrNoRows, which answers 404), and marks it as just changed.
func (s *Server) editPlaylist(ctx context.Context, id int64, fn func(*sql.Tx) error) error {
	err := db.Tx(ctx, s.db, func(tx *sql.Tx) error {
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM collections WHERE id = ? AND kind = 'playlist'`, id).Scan(&found); err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at = unixepoch() WHERE id = ?`, id)
		return err
	})
	if err == nil {
		s.recs.invalidate() // videos in one playlist count as alike
	}
	return err
}

// addToPlaylist appends videos that exist and aren't in the playlist yet.
func addToPlaylist(ctx context.Context, tx *sql.Tx, id int64, items []int64) error {
	var last int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) FROM collection_items WHERE collection_id = ?`, id).Scan(&last); err != nil {
		return err
	}
	for _, item := range items {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO collection_items(collection_id, item_id, position)
			SELECT ?, id, ? FROM items WHERE id = ?
			ON CONFLICT(collection_id, item_id) DO NOTHING`, id, last+1, item)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			last++
		}
	}
	return nil
}

// playlistOrder returns all of a playlist's videos in order, including
// those whose files are missing.
func playlistOrder(ctx context.Context, q db.Execer, id int64) ([]int64, error) {
	var out []int64
	err := eachRow(ctx, q, `SELECT item_id FROM collection_items WHERE collection_id = ? ORDER BY position, item_id`, []any{id},
		func(rows *sql.Rows) error {
			var item int64
			if err := rows.Scan(&item); err != nil {
				return err
			}
			out = append(out, item)
			return nil
		})
	return out, err
}

func renumberPlaylist(ctx context.Context, tx *sql.Tx, id int64, order []int64) error {
	for i, item := range order {
		if _, err := tx.ExecContext(ctx, `UPDATE collection_items SET position = ? WHERE collection_id = ? AND item_id = ?`,
			i+1, id, item); err != nil {
			return err
		}
	}
	return nil
}

// optionalText trims s, with empty or missing as NULL.
func optionalText(s *string) any {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	return strings.TrimSpace(*s)
}
