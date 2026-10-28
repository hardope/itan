package db

import (
	"context"
	"testing"
)

// A version 1 database (before pending_imports existed) is upgraded in place.
func TestMigrateFromV1(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	// Turn it back into a v1 database with some data in it.
	for _, q := range []string{
		`DROP TABLE pending_imports`,
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
}
