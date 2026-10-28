// Package db opens the SQLite database and applies the schema.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

const schemaVersion = 2

// migrations[n] upgrades a version n-1 database to version n. schema.sql
// always holds the full current schema for fresh installs.
var migrations = map[int]string{
	2: `
		CREATE TABLE pending_imports (
		    id              INTEGER PRIMARY KEY,
		    content_hash    TEXT    NOT NULL UNIQUE,
		    size_bytes      INTEGER NOT NULL,
		    library_name    TEXT,
		    rel_path        TEXT,
		    file_name       TEXT,
		    payload         TEXT    NOT NULL,
		    created_at      INTEGER NOT NULL DEFAULT (unixepoch())
		);
		CREATE INDEX pending_imports_path_idx ON pending_imports(library_name, rel_path);
		CREATE INDEX pending_imports_name_idx ON pending_imports(file_name, size_bytes);`,
}

// Open opens (creating if needed) the database in dataDir and migrates it.
func Open(ctx context.Context, dataDir string) (*sql.DB, error) {
	path := filepath.Join(dataDir, "itan.db")
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	switch {
	case v == 0:
		if _, err := db.ExecContext(ctx, schema); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		return nil
	case v > schemaVersion:
		return fmt.Errorf("database schema version %d is newer than this build supports (%d)", v, schemaVersion)
	}
	for next := v + 1; next <= schemaVersion; next++ {
		err := Tx(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, migrations[next]); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", next))
			return err
		})
		if err != nil {
			return fmt.Errorf("migrate to schema version %d: %w", next, err)
		}
	}
	return nil
}

// Tx runs fn in a transaction, committing on success.
func Tx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ReindexItem rewrites an item's full-text search row from its current
// title, description and tags. Call after any change to those.
func ReindexItem(ctx context.Context, q Execer, itemID int64) error {
	var title, orig, desc, tags sql.NullString
	err := q.QueryRowContext(ctx, `
		SELECT i.title, i.original_title, i.description,
		       (SELECT group_concat(t.name, ' ') FROM item_tags it
		          JOIN tags t ON t.id = it.tag_id
		         WHERE it.item_id = i.id AND it.removed = 0)
		  FROM items i WHERE i.id = ?`, itemID).Scan(&title, &orig, &desc, &tags)
	if err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM items_fts WHERE rowid = ?`, itemID); err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO items_fts(rowid, title, original_title, description, tags) VALUES (?, ?, ?, ?, ?)`,
		itemID, title.String, orig.String, desc.String, tags.String)
	return err
}
