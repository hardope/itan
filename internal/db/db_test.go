package db

import (
	"context"
	"testing"
)

// A version 1 database is upgraded in place to the current version.
func TestMigrateFromV1(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	// Turn it back into a v1 database with some data in it: undo what
	// versions 2 and 3 add.
	for _, q := range []string{
		`DROP TABLE pending_imports`,
		`DROP TABLE edit_sessions`,
		`DROP INDEX collections_match_idx`,
		`DROP INDEX collections_parent_idx`,
		`DROP INDEX collection_items_item_idx`,
		`ALTER TABLE collections DROP COLUMN match_key`,
		`INSERT INTO libraries(name, root_path) VALUES ('L', '/x')`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := d.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()

	d, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var v, libs int
	d.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v)
	d.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraries`).Scan(&libs)
	if v != schemaVersion || libs != 1 {
		t.Fatalf("version %d, %d libraries", v, libs)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO pending_imports(content_hash, size_bytes, payload) VALUES ('h', 1, '{}')`); err != nil {
		t.Errorf("pending_imports missing after migration: %v", err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO edit_sessions(token_hash) VALUES ('t')`); err != nil {
		t.Errorf("edit_sessions missing after migration: %v", err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO collections(uid, kind, title, match_key) VALUES ('u', 'series', 'S', 's')`); err != nil {
		t.Errorf("collections.match_key missing after migration: %v", err)
	}
}
