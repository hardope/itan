package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/hardope/itan/internal/db"
)

// presentEpisodes selects a series' episodes that have a file on disk:
// columns series_id, season, episode, item_id.
const presentEpisodes = `
	SELECT sr.id AS series_id, se.position AS season, ci.position AS episode, ci.item_id
	  FROM collections sr
	  JOIN collections se ON se.parent_id = sr.id AND se.kind = 'season'
	  JOIN collection_items ci ON ci.collection_id = se.id
	 WHERE sr.kind = 'series'
	   AND EXISTS (SELECT 1 FROM files f WHERE f.item_id = ci.item_id AND f.missing_since IS NULL)`

// seriesCards builds the library cards for series: title, years, counts,
// the first episode as cover, and the genres its episodes share most.
func (s *Server) seriesCards(ctx context.Context, ids []int64) (map[int64]*itemSummary, error) {
	out := map[int64]*itemSummary{}
	if len(ids) == 0 {
		return out, nil
	}
	list := idList(ids)
	err := eachRow(ctx, s.db, `
		SELECT e.series_id, c.title, COUNT(*), COUNT(DISTINCT e.season), MIN(i.year), MAX(i.year),
		       SUM(COALESCE(ps.completed, 0)), SUM(COALESCE(i.duration_ms, 0)),
		       (SELECT x.item_id FROM (`+presentEpisodes+`) x WHERE x.series_id = e.series_id
		         ORDER BY x.season, x.episode LIMIT 1)
		  FROM (`+presentEpisodes+`) e
		  JOIN collections c ON c.id = e.series_id
		  JOIN items i ON i.id = e.item_id
		  LEFT JOIN play_state ps ON ps.item_id = e.item_id AND ps.profile_id = ?
		 WHERE e.series_id IN (`+list+`)
		 GROUP BY e.series_id`, []any{profileID}, func(rows *sql.Rows) error {
		c := &itemSummary{Type: "series", Kind: "series", Tags: []tagJSON{}, Show: &showInfo{}}
		var yearMin, yearMax sql.NullInt64
		var duration int64
		if err := rows.Scan(&c.ID, &c.Title, &c.Show.Episodes, &c.Show.Seasons, &yearMin, &yearMax,
			&c.Show.Watched, &duration, &c.Show.CoverID); err != nil {
			return err
		}
		if yearMin.Valid {
			y := int(yearMin.Int64)
			c.Year = &y
			if yearMax.Int64 != yearMin.Int64 {
				c.Show.YearEnd = int(yearMax.Int64)
			}
		}
		c.Completed = c.Show.Watched == c.Show.Episodes
		out[c.ID] = c
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Tags most of a series' episodes have: genres first, at most four.
	type tagCount struct {
		t tagJSON
		n int
	}
	counts := map[int64][]tagCount{}
	err = eachRow(ctx, s.db, `
		SELECT e.series_id, t.namespace, t.slug, t.name, COALESCE(t.color, ''), COUNT(*)
		  FROM (`+presentEpisodes+`) e
		  JOIN item_tags it ON it.item_id = e.item_id AND it.removed = 0
		  JOIN tags t ON t.id = it.tag_id
		 WHERE e.series_id IN (`+list+`)
		 GROUP BY e.series_id, t.id`, nil, func(rows *sql.Rows) error {
		var id int64
		var tc tagCount
		var slug string
		if err := rows.Scan(&id, &tc.t.Namespace, &slug, &tc.t.Name, &tc.t.Color, &tc.n); err != nil {
			return err
		}
		tc.t.Ref = tc.t.Namespace + ":" + slug
		counts[id] = append(counts[id], tc)
		return nil
	})
	for id, tcs := range counts {
		c := out[id]
		if c == nil {
			continue
		}
		sort.Slice(tcs, func(i, j int) bool {
			if (tcs[i].t.Namespace == "genre") != (tcs[j].t.Namespace == "genre") {
				return tcs[i].t.Namespace == "genre"
			}
			if tcs[i].n != tcs[j].n {
				return tcs[i].n > tcs[j].n
			}
			return tcs[i].t.Name < tcs[j].t.Name
		})
		for _, tc := range tcs[:min(4, len(tcs))] {
			if tc.n*2 >= c.Show.Episodes { // on at least half the episodes
				c.Tags = append(c.Tags, tc.t)
			}
		}
	}
	return out, err
}

// showInfo is the series part of a series card.
type showInfo struct {
	Episodes int   `json:"episodes"`
	Seasons  int   `json:"seasons"`
	Watched  int   `json:"watched"`
	YearEnd  int   `json:"year_end,omitempty"`
	CoverID  int64 `json:"cover_id"` // first episode, for the thumbnail
}

type seasonJSON struct {
	Number   int            `json:"number"`
	Episodes []*itemSummary `json:"episodes"`
}

type seriesDetail struct {
	*itemSummary
	Description *string      `json:"description"`
	Seasons     []seasonJSON `json:"seasons"`
	// Next is what "Play" plays: the episode in progress, or the one after
	// the last one watched, or the first.
	Next     *itemSummary `json:"next"`
	NextVerb string       `json:"next_verb"` // "Play", "Resume", "Continue"
}

func (s *Server) getSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	ctx := r.Context()
	cards, err := s.seriesCards(ctx, []int64{id})
	if err != nil {
		serverError(w, r, err)
		return
	}
	card := cards[id]
	if card == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	d := &seriesDetail{itemSummary: card, Seasons: []seasonJSON{}}
	if err := s.db.QueryRowContext(ctx, `SELECT description FROM collections WHERE id = ?`, id).Scan(&d.Description); err != nil {
		serverError(w, r, err)
		return
	}
	episodes, err := s.querySummaries(ctx, `
		WHERE m.series_id = ? AND EXISTS (SELECT 1 FROM files f WHERE f.item_id = i.id AND f.missing_since IS NULL)
		ORDER BY m.season, m.episode, i.id`, profileID, id)
	if err != nil {
		serverError(w, r, err)
		return
	}
	for _, ep := range episodes {
		if n := len(d.Seasons); n == 0 || d.Seasons[n-1].Number != ep.Series.Season {
			d.Seasons = append(d.Seasons, seasonJSON{Number: ep.Series.Season})
		}
		last := &d.Seasons[len(d.Seasons)-1]
		last.Episodes = append(last.Episodes, ep)
	}

	// The most recently played episode decides what's next.
	lastPlayed := map[int64]int64{}
	err = eachRow(ctx, s.db, `SELECT item_id, COALESCE(last_played_at, 0) FROM play_state WHERE profile_id = ?
		AND item_id IN (SELECT item_id FROM (`+presentEpisodes+`) WHERE series_id = ?)`, []any{profileID, id},
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
	d.Next, d.NextVerb = nextEpisode(episodes, lastPlayed)
	writeJSON(w, http.StatusOK, d)
}

// nextEpisode picks what to play from episodes in order.
func nextEpisode(episodes []*itemSummary, lastPlayed map[int64]int64) (*itemSummary, string) {
	if len(episodes) == 0 {
		return nil, ""
	}
	latest, latestAt := -1, int64(0)
	for i, ep := range episodes {
		if at, ok := lastPlayed[ep.ID]; ok && at >= latestAt && (ep.PositionMS > 0 || ep.Completed) {
			latest, latestAt = i, at
		}
	}
	switch {
	case latest < 0:
		return episodes[0], "Play"
	case !episodes[latest].Completed:
		return episodes[latest], "Resume"
	case latest+1 < len(episodes):
		return episodes[latest+1], "Continue"
	default:
		return episodes[0], "Watch again"
	}
}

// updateSeries renames a series or sets its description:
// {"title": "...", "description": "..."}
func (s *Server) updateSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		Title       *string `json:"title"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ctx := r.Context()
	if body.Title != nil {
		t := strings.TrimSpace(*body.Title)
		if t == "" {
			writeError(w, http.StatusBadRequest, "The title can't be empty.")
			return
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE collections SET title = ?, updated_at = unixepoch() WHERE id = ? AND kind = 'series'`, t, id); err != nil {
			serverError(w, r, err)
			return
		}
	}
	if body.Description != nil {
		var desc any = strings.TrimSpace(*body.Description)
		if desc == "" {
			desc = nil
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE collections SET description = ?, updated_at = unixepoch() WHERE id = ? AND kind = 'series'`, desc, id); err != nil {
			serverError(w, r, err)
			return
		}
	}
	s.recs.invalidate()
	s.getSeries(w, r)
}

// deleteSeries splits a series back into separate videos. Its episodes are
// marked as set by hand, so the next scan doesn't group them again.
func (s *Server) deleteSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	ctx := r.Context()
	err := db.Tx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO item_field_sources(item_id, field, source, updated_at)
			SELECT ci.item_id, 'series', 'user', unixepoch()
			  FROM collection_items ci JOIN collections se ON se.id = ci.collection_id AND se.kind = 'season'
			 WHERE se.parent_id = ?
			ON CONFLICT(item_id, field) DO UPDATE SET source = 'user', updated_at = unixepoch()`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM collections WHERE id = ? AND kind = 'series'`, id)
		return err
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	s.recs.invalidate()
	w.WriteHeader(http.StatusNoContent)
}

// listSeries returns every series' id and title, for pickers.
func (s *Server) listSeries(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	}
	out := []entry{}
	err := eachRow(r.Context(), s.db, `SELECT id, title FROM collections WHERE kind = 'series' ORDER BY title COLLATE NOCASE`, nil,
		func(rows *sql.Rows) error {
			var e entry
			if err := rows.Scan(&e.ID, &e.Title); err != nil {
				return err
			}
			out = append(out, e)
			return nil
		})
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func idList(ids []int64) string {
	if len(ids) == 0 {
		return "NULL"
	}
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(s, ",")
}
