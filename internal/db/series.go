package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Series are collections of kind 'series' with child 'season' collections
// (position = season number); episodes are a season's items (position =
// episode number). An episode is in at most one season.

// EnsureSeries returns the series for a show name, creating it if needed.
// It matches the slug it was created with (so a renamed series still
// collects new episodes of the show) or its current title.
func EnsureSeries(ctx context.Context, q Execer, name string) (int64, error) {
	key := Slugify(name)
	if key == "" {
		return 0, fmt.Errorf("series name %q has no letters or digits", name)
	}
	var id int64
	err := q.QueryRowContext(ctx, `
		SELECT id FROM collections
		 WHERE kind = 'series' AND (match_key = ? OR lower(title) = lower(?))
		 ORDER BY match_key = ? DESC, id LIMIT 1`, key, name, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	err = q.QueryRowContext(ctx, `
		INSERT INTO collections(uid, kind, title, match_key) VALUES (?, 'series', ?, ?) RETURNING id`,
		NewUID(), name, key).Scan(&id)
	return id, err
}

// EnsureSeason returns a series' season by number, creating it if needed.
func EnsureSeason(ctx context.Context, q Execer, seriesID int64, number int) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM collections WHERE kind = 'season' AND parent_id = ? AND position = ?`,
		seriesID, number).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	err = q.QueryRowContext(ctx, `
		INSERT INTO collections(uid, kind, title, parent_id, position) VALUES (?, 'season', ?, ?, ?) RETURNING id`,
		NewUID(), fmt.Sprintf("Season %d", number), seriesID, number).Scan(&id)
	return id, err
}

// SetEpisode puts an item in a season as episode number (0 means after the
// last one), taking it out of any other season.
func SetEpisode(ctx context.Context, q Execer, itemID, seasonID int64, number int) error {
	if err := LeaveSeries(ctx, q, itemID); err != nil {
		return err
	}
	if number <= 0 {
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM collection_items WHERE collection_id = ?`,
			seasonID).Scan(&number); err != nil {
			return err
		}
	}
	_, err := q.ExecContext(ctx, `INSERT INTO collection_items(collection_id, item_id, position) VALUES (?, ?, ?)`,
		seasonID, itemID, number)
	return err
}

// LeaveSeries takes an item out of its season, if it's in one.
func LeaveSeries(ctx context.Context, q Execer, itemID int64) error {
	_, err := q.ExecContext(ctx, `
		DELETE FROM collection_items
		 WHERE item_id = ? AND collection_id IN (SELECT id FROM collections WHERE kind = 'season')`, itemID)
	return err
}

// PruneSeries removes seasons without episodes and series without seasons.
func PruneSeries(ctx context.Context, q Execer) error {
	if _, err := q.ExecContext(ctx, `
		DELETE FROM collections WHERE kind = 'season'
		   AND NOT EXISTS (SELECT 1 FROM collection_items ci WHERE ci.collection_id = collections.id)`); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `
		DELETE FROM collections WHERE kind = 'series'
		   AND NOT EXISTS (SELECT 1 FROM collections s WHERE s.parent_id = collections.id)`)
	return err
}
