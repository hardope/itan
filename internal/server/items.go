package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hardope/itan/internal/db"
)

type tagJSON struct {
	Ref       string `json:"ref"` // "genre:music"
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Source    string `json:"source,omitempty"`
	Color     string `json:"color,omitempty"`
	Count     int    `json:"count,omitempty"`
}

type itemSummary struct {
	Type         string     `json:"type,omitempty"` // "series" for series cards in a grouped list
	ID           int64      `json:"id"`
	Title        string     `json:"title"`
	Year         *int       `json:"year"`
	Kind         string     `json:"kind"`
	DurationMS   *int64     `json:"duration_ms"`
	Quality      *string    `json:"quality"`
	PlaybackMode *string    `json:"playback_mode"`
	PositionMS   int64      `json:"position_ms"`
	Completed    bool       `json:"completed"`
	Tags         []tagJSON  `json:"tags"`
	Reason       string     `json:"reason,omitempty"` // why it was recommended
	Series       *seriesRef `json:"series,omitempty"`
	Show         *showInfo  `json:"show,omitempty"` // series cards only
}

// seriesRef places an episode in its series.
type seriesRef struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
}

// episodeJoin adds alias m: the item's series, season and episode number.
const episodeJoin = `
	LEFT JOIN (SELECT ci.item_id, sr.id AS series_id, sr.title AS series_title,
	                  se.position AS season, ci.position AS episode
	             FROM collection_items ci
	             JOIN collections se ON se.id = ci.collection_id AND se.kind = 'season'
	             JOIN collections sr ON sr.id = se.parent_id AND sr.kind = 'series') m ON m.item_id = i.id`

// Sort orders over the columns of a listing row (see listItems).
var sortOrders = map[string]string{
	"title":  "sort_title COLLATE NOCASE",
	"added":  "added DESC",
	"year":   "year DESC NULLS LAST, sort_title COLLATE NOCASE",
	"rating": "rating DESC NULLS LAST, sort_title COLLATE NOCASE",
	"played": "played DESC NULLS LAST",
}

// listItems supports:
//
//	q=        full-text search (title, description, tag names)
//	tag=      tag ref, repeatable; items must have all of them
//	exclude=  tag ref, repeatable; items must have none of them
//	kind=, quality=
//	shelf=continue   partially watched, most recent first
//	sort=title|added|year|rating|played, limit=, offset=
//	group=0          list episodes one by one
//
// Episodes in a series are grouped into one series card ({"type":
// "series", ...}) that matches if any of its episodes match, except on the
// continue shelf. The X-Total-Count header has the number of matches
// without limit/offset.
func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where := []string{"EXISTS (SELECT 1 FROM files f WHERE f.item_id = i.id AND f.missing_since IS NULL)"}
	args := []any{profileID}

	if text := ftsQuery(q.Get("q")); text != "" {
		where = append(where, "i.id IN (SELECT rowid FROM items_fts WHERE items_fts MATCH ?)")
		args = append(args, text)
	}
	tagCond := `i.id IN (SELECT it.item_id FROM item_tags it JOIN tags t ON t.id = it.tag_id
	                     WHERE it.removed = 0 AND t.namespace = ? AND t.slug = ?)`
	for _, ref := range q["tag"] {
		ns, slug, _ := db.ParseTagRef(ref)
		where = append(where, tagCond)
		args = append(args, ns, slug)
	}
	for _, ref := range q["exclude"] {
		ns, slug, _ := db.ParseTagRef(ref)
		where = append(where, "NOT "+tagCond)
		args = append(args, ns, slug)
	}
	if v := q.Get("kind"); v != "" {
		where = append(where, "i.kind = ?")
		args = append(args, v)
	}
	if v := q.Get("quality"); v != "" {
		where = append(where, "i.quality_label = ?")
		args = append(args, v)
	}
	order := sortOrders["title"]
	if o, ok := sortOrders[q.Get("sort")]; ok {
		order = o
	}
	group := q.Get("group") != "0"
	if q.Get("shelf") == "continue" {
		where = append(where, "ps.position_ms > 0 AND ps.completed = 0")
		order, group = sortOrders["played"], false
	}
	limit := clamp(atoi(q.Get("limit"), 200), 1, 1000)
	offset := max(atoi(q.Get("offset"), 0), 0)

	// One row per item, or per series for grouped episodes, with the
	// columns the sort orders use.
	matched := `SELECT i.id, m.series_id, COALESCE(i.sort_title, i.title) AS sort_title,
	                   i.added_at AS added, i.year, i.rating, ps.last_played_at AS played
	              FROM items i
	              LEFT JOIN play_state ps ON ps.item_id = i.id AND ps.profile_id = ?` + episodeJoin + `
	             WHERE ` + strings.Join(where, " AND ")
	rowsSQL := `SELECT 'item' AS type, id, sort_title, added, year, rating, played FROM matched`
	if group {
		rowsSQL = `SELECT 'item' AS type, id, sort_title, added, year, rating, played FROM matched WHERE series_id IS NULL
		           UNION ALL
		           SELECT 'series', c.id, c.title, MAX(x.added), MIN(x.year), NULL, MAX(x.played)
		             FROM matched x JOIN collections c ON c.id = x.series_id GROUP BY c.id`
	}
	ctx := r.Context()
	type entry struct {
		kind string
		id   int64
	}
	var entries []entry
	err := eachRow(ctx, s.db, fmt.Sprintf(`WITH matched AS (%s), listed AS (%s)
		SELECT type, id FROM listed ORDER BY %s, type, id LIMIT %d OFFSET %d`, matched, rowsSQL, order, limit, offset),
		args, func(rows *sql.Rows) error {
			var e entry
			if err := rows.Scan(&e.kind, &e.id); err != nil {
				return err
			}
			entries = append(entries, e)
			return nil
		})
	if err != nil {
		serverError(w, r, err)
		return
	}
	var total int
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`WITH matched AS (%s), listed AS (%s) SELECT COUNT(*) FROM listed`,
		matched, rowsSQL), args...).Scan(&total); err != nil {
		serverError(w, r, err)
		return
	}

	var itemIDs, seriesIDs []int64
	for _, e := range entries {
		if e.kind == "series" {
			seriesIDs = append(seriesIDs, e.id)
		} else {
			itemIDs = append(itemIDs, e.id)
		}
	}
	items, err := s.summariesByID(ctx, itemIDs)
	if err != nil {
		serverError(w, r, err)
		return
	}
	series, err := s.seriesCards(ctx, seriesIDs)
	if err != nil {
		serverError(w, r, err)
		return
	}
	out := make([]*itemSummary, 0, len(entries))
	for _, e := range entries {
		var x *itemSummary
		if e.kind == "series" {
			x = series[e.id]
		} else {
			x = items[e.id]
		}
		if x != nil {
			out = append(out, x)
		}
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	writeJSON(w, http.StatusOK, out)
}

// summariesByID loads item cards by id.
func (s *Server) summariesByID(ctx context.Context, ids []int64) (map[int64]*itemSummary, error) {
	out := map[int64]*itemSummary{}
	if len(ids) == 0 {
		return out, nil
	}
	items, err := s.querySummaries(ctx, `WHERE i.id IN (`+idList(ids)+`)`, profileID)
	for _, it := range items {
		out[it.ID] = it
	}
	return out, err
}

// querySummaries loads item cards. tail is the WHERE/ORDER clause; the
// first argument must be the profile id.
func (s *Server) querySummaries(ctx context.Context, tail string, args ...any) ([]*itemSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.title, i.year, i.kind, i.duration_ms, i.quality_label, i.playback_mode,
		       COALESCE(ps.position_ms, 0), COALESCE(ps.completed, 0),
		       m.series_id, m.series_title, m.season, m.episode
		  FROM items i
		  LEFT JOIN play_state ps ON ps.item_id = i.id AND ps.profile_id = ?`+episodeJoin+`
		 `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []*itemSummary{}
	byID := map[int64]*itemSummary{}
	for rows.Next() {
		it := &itemSummary{Tags: []tagJSON{}}
		var seriesID, season, episode sql.NullInt64
		var seriesTitle sql.NullString
		if err := rows.Scan(&it.ID, &it.Title, &it.Year, &it.Kind, &it.DurationMS, &it.Quality,
			&it.PlaybackMode, &it.PositionMS, &it.Completed, &seriesID, &seriesTitle, &season, &episode); err != nil {
			return nil, err
		}
		if seriesID.Valid {
			it.Series = &seriesRef{ID: seriesID.Int64, Title: seriesTitle.String, Season: int(season.Int64), Episode: int(episode.Int64)}
		}
		items = append(items, it)
		byID[it.ID] = it
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, s.attachTags(ctx, byID)
}

func (s *Server) attachTags(ctx context.Context, byID map[int64]*itemSummary) error {
	if len(byID) == 0 {
		return nil
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT it.item_id, t.namespace, t.slug, t.name, COALESCE(t.color, '')
		  FROM item_tags it JOIN tags t ON t.id = it.tag_id
		 WHERE it.removed = 0 AND it.item_id IN (`+strings.Join(ids, ",")+`)
		 ORDER BY t.namespace = 'genre' DESC, t.name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t tagJSON
		var slug string
		if err := rows.Scan(&id, &t.Namespace, &slug, &t.Name, &t.Color); err != nil {
			return err
		}
		t.Ref = t.Namespace + ":" + slug
		byID[id].Tags = append(byID[id].Tags, t)
	}
	return rows.Err()
}

type itemDetail struct {
	ID            int64             `json:"id"`
	ContentHash   string            `json:"content_hash"`
	SizeBytes     int64             `json:"size_bytes"`
	Title         string            `json:"title"`
	SortTitle     *string           `json:"sort_title"`
	OriginalTitle *string           `json:"original_title"`
	Description   *string           `json:"description"`
	Year          *int              `json:"year"`
	Kind          string            `json:"kind"`
	Rating        *int              `json:"rating"`
	Language      *string           `json:"language"`
	Container     *string           `json:"container"`
	DurationMS    *int64            `json:"duration_ms"`
	Bitrate       *int64            `json:"bitrate"`
	Width         *int              `json:"width"`
	Height        *int              `json:"height"`
	VideoCodec    *string           `json:"video_codec"`
	HDR           *string           `json:"hdr"`
	Quality       *string           `json:"quality"`
	PlaybackMode  *string           `json:"playback_mode"`
	ProbeError    *string           `json:"probe_error"`
	PositionMS    int64             `json:"position_ms"`
	Completed     bool              `json:"completed"`
	Favorite      bool              `json:"favorite"`
	Series        *seriesRef        `json:"series"`
	Sources       map[string]string `json:"sources"` // field -> where its value came from
	Tags          []tagJSON         `json:"tags"`
	Streams       []streamJSON      `json:"streams"`
	Files         []fileJSON        `json:"files"`
}

type streamJSON struct {
	Type     string  `json:"type"`
	Codec    *string `json:"codec"`
	Language *string `json:"language"`
	Title    *string `json:"title"`
	Channels *int    `json:"channels"`
	Default  bool    `json:"default"`
}

type fileJSON struct {
	Library string `json:"library"`
	Path    string `json:"path"`
	Missing bool   `json:"missing"`
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	d, err := s.loadItem(r.Context(), id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) loadItem(ctx context.Context, id int64) (*itemDetail, error) {
	d := &itemDetail{Sources: map[string]string{}, Tags: []tagJSON{}, Streams: []streamJSON{}, Files: []fileJSON{}}
	err := s.db.QueryRowContext(ctx, `
		SELECT i.id, i.content_hash, i.size_bytes, i.title, i.sort_title, i.original_title,
		       i.description, i.year, i.kind, i.rating, i.language, i.container, i.duration_ms,
		       i.bitrate, i.width, i.height, i.video_codec, i.hdr, i.quality_label,
		       i.playback_mode, i.probe_error,
		       COALESCE(ps.position_ms, 0), COALESCE(ps.completed, 0), COALESCE(ps.favorite, 0)
		  FROM items i
		  LEFT JOIN play_state ps ON ps.item_id = i.id AND ps.profile_id = ?
		 WHERE i.id = ?`, profileID, id).Scan(
		&d.ID, &d.ContentHash, &d.SizeBytes, &d.Title, &d.SortTitle, &d.OriginalTitle,
		&d.Description, &d.Year, &d.Kind, &d.Rating, &d.Language, &d.Container, &d.DurationMS,
		&d.Bitrate, &d.Width, &d.Height, &d.VideoCodec, &d.HDR, &d.Quality,
		&d.PlaybackMode, &d.ProbeError, &d.PositionMS, &d.Completed, &d.Favorite)
	if err != nil {
		return nil, err
	}
	var ref seriesRef
	var season, episode sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT m.series_id, m.series_title, m.season, m.episode FROM items i`+episodeJoin+`
		WHERE i.id = ? AND m.series_id IS NOT NULL`, id).Scan(&ref.ID, &ref.Title, &season, &episode)
	if err == nil {
		ref.Season, ref.Episode = int(season.Int64), int(episode.Int64)
		d.Series = &ref
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	if err := eachRow(ctx, s.db, `SELECT field, source FROM item_field_sources WHERE item_id = ?`, []any{id},
		func(rows *sql.Rows) error {
			var f, src string
			if err := rows.Scan(&f, &src); err != nil {
				return err
			}
			d.Sources[f] = src
			return nil
		}); err != nil {
		return nil, err
	}

	if err := eachRow(ctx, s.db, `
		SELECT t.namespace, t.slug, t.name, it.source, COALESCE(t.color, '')
		  FROM item_tags it JOIN tags t ON t.id = it.tag_id
		 WHERE it.item_id = ? AND it.removed = 0
		 ORDER BY t.namespace = 'genre' DESC, t.name`, []any{id},
		func(rows *sql.Rows) error {
			var t tagJSON
			var slug string
			if err := rows.Scan(&t.Namespace, &slug, &t.Name, &t.Source, &t.Color); err != nil {
				return err
			}
			t.Ref = t.Namespace + ":" + slug
			d.Tags = append(d.Tags, t)
			return nil
		}); err != nil {
		return nil, err
	}

	if err := eachRow(ctx, s.db, `
		SELECT type, codec, language, title, channels, is_default FROM streams
		 WHERE item_id = ? ORDER BY stream_index`, []any{id},
		func(rows *sql.Rows) error {
			var st streamJSON
			if err := rows.Scan(&st.Type, &st.Codec, &st.Language, &st.Title, &st.Channels, &st.Default); err != nil {
				return err
			}
			d.Streams = append(d.Streams, st)
			return nil
		}); err != nil {
		return nil, err
	}

	err = eachRow(ctx, s.db, `
		SELECT l.name, f.rel_path, f.missing_since IS NOT NULL FROM files f
		  JOIN libraries l ON l.id = f.library_id WHERE f.item_id = ?`, []any{id},
		func(rows *sql.Rows) error {
			var f fileJSON
			if err := rows.Scan(&f.Library, &f.Path, &f.Missing); err != nil {
				return err
			}
			d.Files = append(d.Files, f)
			return nil
		})
	return d, err
}

// editableFields maps JSON keys accepted by PATCH to their column.
var editableFields = map[string]bool{
	"title": true, "sort_title": true, "original_title": true, "description": true,
	"year": true, "kind": true, "rating": true, "language": true,
}

// updateItem applies user edits. Body example:
//
//	{"title": "...", "year": 2016, "add_tags": ["genre:Music"], "remove_tags": ["tag:4k"],
//	 "series": {"title": "Show", "season": 1, "episode": 3}}
//
// "series": null takes the item out of its series. Episode 0 or missing
// means after the last one.
//
// Every field set here is recorded with source 'user', so rescans and
// imports of auto-detected data never overwrite it.
func (s *Server) updateItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	var addTags, removeTags []string
	var series *seriesEdit
	_, editsSeries := body["series"]
	if editsSeries {
		if err := json.Unmarshal(body["series"], &series); err != nil {
			writeError(w, http.StatusBadRequest, `series must be null or {"title": "...", "season": 1, "episode": 2}`)
			return
		}
	}
	if raw, ok := body["add_tags"]; ok {
		if err := json.Unmarshal(raw, &addTags); err != nil {
			writeError(w, http.StatusBadRequest, "add_tags must be a list of strings")
			return
		}
	}
	if raw, ok := body["remove_tags"]; ok {
		if err := json.Unmarshal(raw, &removeTags); err != nil {
			writeError(w, http.StatusBadRequest, "remove_tags must be a list of strings")
			return
		}
	}

	ctx := r.Context()
	err := db.Tx(ctx, s.db, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM items WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		for field, raw := range body {
			if !editableFields[field] {
				continue
			}
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return badRequest(field + ": invalid value")
			}
			if str, ok := v.(string); ok && strings.TrimSpace(str) == "" {
				v = nil
			}
			if field == "title" && v == nil {
				return badRequest("title cannot be empty")
			}
			// field is whitelisted above, safe to interpolate.
			if _, err := tx.ExecContext(ctx, `UPDATE items SET `+field+` = ? WHERE id = ?`, v, id); err != nil {
				if strings.Contains(err.Error(), "CHECK constraint") {
					return badRequest(field + ": value not allowed")
				}
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO item_field_sources(item_id, field, source, updated_at) VALUES (?, ?, 'user', unixepoch())
				ON CONFLICT(item_id, field) DO UPDATE SET source = 'user', updated_at = unixepoch()`,
				id, field); err != nil {
				return err
			}
		}
		for _, ref := range addTags {
			tagID, err := db.EnsureTag(ctx, tx, ref)
			if err != nil {
				return err
			}
			if tagID == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO item_tags(item_id, tag_id, source) VALUES (?, ?, 'user')
				ON CONFLICT(item_id, tag_id) DO UPDATE SET removed = 0, source = 'user'`,
				id, tagID); err != nil {
				return err
			}
		}
		for _, ref := range removeTags {
			ns, slug, _ := db.ParseTagRef(ref)
			sel := `(SELECT id FROM tags WHERE namespace = ? AND slug = ?)`
			// Tags the user added are simply deleted; auto-detected ones keep a
			// tombstone so the next scan doesn't add them back.
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM item_tags WHERE item_id = ? AND source = 'user' AND tag_id = `+sel,
				id, ns, slug); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE item_tags SET removed = 1 WHERE item_id = ? AND tag_id = `+sel,
				id, ns, slug); err != nil {
				return err
			}
		}
		if editsSeries {
			if err := setSeries(ctx, tx, id, series); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE items SET updated_at = unixepoch() WHERE id = ?`, id); err != nil {
			return err
		}
		return db.ReindexItem(ctx, tx, id)
	})
	if br, ok := err.(badRequest); ok {
		writeError(w, http.StatusBadRequest, string(br))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	s.recs.invalidate()
	s.folders.itemChanged(id)
	s.getItem(w, r)
}

type seriesEdit struct {
	Title   string `json:"title"`
	Season  int    `json:"season"`
	Episode int    `json:"episode"`
}

// setSeries puts an item in a series by hand (or takes it out, for nil or
// an empty title). Automatic grouping leaves it alone from then on.
func setSeries(ctx context.Context, tx *sql.Tx, itemID int64, e *seriesEdit) error {
	if e == nil || strings.TrimSpace(e.Title) == "" {
		if err := db.LeaveSeries(ctx, tx, itemID); err != nil {
			return err
		}
	} else {
		if e.Season < 0 || e.Episode < 0 || e.Season > 1000 || e.Episode > 100000 {
			return badRequest("season and episode must be positive numbers")
		}
		seriesID, err := db.EnsureSeries(ctx, tx, strings.TrimSpace(e.Title))
		if err != nil {
			return badRequest(err.Error())
		}
		seasonID, err := db.EnsureSeason(ctx, tx, seriesID, max(e.Season, 1))
		if err != nil {
			return err
		}
		if err := db.SetEpisode(ctx, tx, itemID, seasonID, e.Episode); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO item_field_sources(item_id, field, source, updated_at) VALUES (?, 'series', 'user', unixepoch())
		ON CONFLICT(item_id, field) DO UPDATE SET source = 'user', updated_at = unixepoch()`, itemID); err != nil {
		return err
	}
	return db.PruneSeries(ctx, tx)
}

type badRequest string

func (b badRequest) Error() string { return string(b) }

// saveProgress records the resume position. Body: {"position_ms": 123000}
func (s *Server) saveProgress(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		PositionMS int64 `json:"position_ms"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.PositionMS < 0 {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var duration sql.NullInt64
	if err := s.db.QueryRowContext(r.Context(), `SELECT duration_ms FROM items WHERE id = ?`, id).Scan(&duration); err != nil {
		serverError(w, r, err)
		return
	}
	// Past 90% counts as watched; for long videos (credits), so does being
	// within the last 2 minutes.
	completed := duration.Valid && duration.Int64 > 0 &&
		(body.PositionMS >= duration.Int64*9/10 ||
			(duration.Int64 > 20*60_000 && duration.Int64-body.PositionMS < 120_000))
	pos := body.PositionMS
	if completed {
		pos = 0
	}
	now := time.Now().Unix()
	_, err := s.db.ExecContext(r.Context(), `
		INSERT INTO play_state(profile_id, item_id, position_ms, completed, play_count, last_played_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, item_id) DO UPDATE SET
			position_ms = excluded.position_ms,
			play_count = play_count + (excluded.completed AND NOT completed),
			completed = completed OR excluded.completed,
			last_played_at = excluded.last_played_at`,
		profileID, id, pos, completed, boolInt(completed), now)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"position_ms": pos, "completed": completed})
}

func (s *Server) listTags(w http.ResponseWriter, r *http.Request) {
	tags := []tagJSON{}
	err := eachRow(r.Context(), s.db, `
		SELECT t.namespace, t.slug, t.name, COALESCE(t.color, ''), COUNT(it.item_id) AS n
		  FROM tags t
		  JOIN item_tags it ON it.tag_id = t.id AND it.removed = 0
		 WHERE EXISTS (SELECT 1 FROM files f WHERE f.item_id = it.item_id AND f.missing_since IS NULL)
		 GROUP BY t.id
		 ORDER BY t.namespace = 'genre' DESC, n DESC, t.name`, nil,
		func(rows *sql.Rows) error {
			var t tagJSON
			var slug string
			if err := rows.Scan(&t.Namespace, &slug, &t.Name, &t.Color, &t.Count); err != nil {
				return err
			}
			t.Ref = t.Namespace + ":" + slug
			tags = append(tags, t)
			return nil
		})
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

// ftsQuery turns free text into a safe FTS5 query: every word must match,
// as a prefix ("plan ear" finds "Planet Earth").
func ftsQuery(s string) string {
	var terms []string
	for _, word := range strings.Fields(s) {
		word = strings.ReplaceAll(word, `"`, "")
		if word != "" {
			terms = append(terms, `"`+word+`"*`)
		}
	}
	return strings.Join(terms, " ")
}

func eachRow(ctx context.Context, q db.Execer, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func clamp(n, lo, hi int) int { return max(lo, min(n, hi)) }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
