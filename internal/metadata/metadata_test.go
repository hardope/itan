package metadata

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hardope/itan/internal/db"
)

var ctx = context.Background()

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func exec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.ExecContext(ctx, q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func addLibrary(t *testing.T, d *sql.DB, name, root string) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRowContext(ctx, `INSERT INTO libraries(name, root_path) VALUES (?, ?) RETURNING id`,
		name, root).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// addItem creates an item as a scan would: auto-detected title, one file.
func addItem(t *testing.T, d *sql.DB, libID int64, hash string, size int64, rel, title string) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRowContext(ctx, `INSERT INTO items(content_hash, size_bytes, title) VALUES (?, ?, ?) RETURNING id`,
		hash, size, title).Scan(&id); err != nil {
		t.Fatal(err)
	}
	exec(t, d, `INSERT INTO item_field_sources(item_id, field, source) VALUES (?, 'title', 'filename')`, id)
	// Stored like the scanner does, with the OS separator.
	exec(t, d, `INSERT INTO files(item_id, library_id, rel_path, size_bytes, mtime) VALUES (?, ?, ?, ?, 0)`, id, libID, filepath.FromSlash(rel), size)
	return id
}

func setField(t *testing.T, d *sql.DB, id int64, field string, value any, source string, updated int64) {
	t.Helper()
	exec(t, d, `UPDATE items SET `+field+` = ? WHERE id = ?`, value, id)
	exec(t, d, `INSERT INTO item_field_sources(item_id, field, source, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(item_id, field) DO UPDATE SET source = excluded.source, updated_at = excluded.updated_at`,
		id, field, source, updated)
}

func addTag(t *testing.T, d *sql.DB, itemID int64, ref, source string, removed bool) {
	t.Helper()
	tagID, err := db.EnsureTag(ctx, d, ref)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, d, `INSERT INTO item_tags(item_id, tag_id, source, removed) VALUES (?, ?, ?, ?)
		ON CONFLICT(item_id, tag_id) DO UPDATE SET source = excluded.source, removed = excluded.removed`,
		itemID, tagID, source, removed)
}

func field(t *testing.T, d *sql.DB, id int64, name string) (value any, source string) {
	t.Helper()
	d.QueryRowContext(ctx, `SELECT `+name+` FROM items WHERE id = ?`, id).Scan(&value)
	source = "filename"
	d.QueryRowContext(ctx, `SELECT source FROM item_field_sources WHERE item_id = ? AND field = ?`, id, name).Scan(&source)
	return normalizeValue(value), source
}

// tagState returns "live", "removed" or "absent" for a tag on an item.
func tagState(t *testing.T, d *sql.DB, itemID int64, ns, slug string) string {
	t.Helper()
	var removed bool
	err := d.QueryRowContext(ctx, `SELECT it.removed FROM item_tags it JOIN tags t ON t.id = it.tag_id
		WHERE it.item_id = ? AND t.namespace = ? AND t.slug = ?`, itemID, ns, slug).Scan(&removed)
	switch {
	case err == sql.ErrNoRows:
		return "absent"
	case err != nil:
		t.Fatal(err)
	case removed:
		return "removed"
	}
	return "live"
}

// roundTrip exports src and imports the result into dst.
func roundTrip(t *testing.T, src, dst *sql.DB, mode string, dryRun bool) *Report {
	t.Helper()
	lib, err := Export(ctx, src, true, "test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(lib)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ReadLibrary(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Import(ctx, dst, parsed, mode, dryRun)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestRoundTrip(t *testing.T) {
	src, dst := openDB(t), openDB(t)

	srcLib := addLibrary(t, src, "Docs", "/a/docs")
	a := addItem(t, src, srcLib, "ph1:aaaa", 100, "Nature/planet.mkv", "planet")
	setField(t, src, a, "title", "Planet Earth II", "user", 1000)
	setField(t, src, a, "rating", 9, "user", 1000)
	setField(t, src, a, "year", 2016, "filename", 1000)
	addTag(t, src, a, "tag:nature", "folder", false)
	addTag(t, src, a, "tag:4k", "filename", true) // user removed this auto tag
	addTag(t, src, a, "genre:Documentary", "user", false)
	exec(t, src, `INSERT INTO collections(uid, kind, title) VALUES ('c1', 'playlist', 'Sunday')`)
	exec(t, src, `INSERT INTO collection_items(collection_id, item_id, position) VALUES (1, ?, 1)`, a)
	exec(t, src, `INSERT INTO profiles(uid, name) VALUES ('p1', 'Ope')`)
	exec(t, src, `INSERT INTO play_state(profile_id, item_id, position_ms, play_count, favorite, last_played_at)
		VALUES (1, ?, 5000, 2, 1, 1700000000)`, a)

	// The other install found the same file at a different path, with auto metadata.
	dstLib := addLibrary(t, dst, "Documentaries", "/b/videos")
	b := addItem(t, dst, dstLib, "ph1:aaaa", 100, "planet.mkv", "planet")
	addTag(t, dst, b, "tag:4k", "filename", false) // the dst scan auto-detected it

	rep := roundTrip(t, src, dst, ModeMerge, false)
	if rep.Matched["hash"] != 1 || rep.Pending != 0 {
		t.Fatalf("matching: %+v", rep)
	}
	if v, s := field(t, dst, b, "title"); v != "Planet Earth II" || s != "user" {
		t.Errorf("title = %v (%s)", v, s)
	}
	if v, s := field(t, dst, b, "rating"); v != int64(9) || s != "user" {
		t.Errorf("rating = %v (%s)", v, s)
	}
	if v, _ := field(t, dst, b, "year"); v != int64(2016) {
		t.Errorf("year = %v", v)
	}
	if got := tagState(t, dst, b, "tag", "nature"); got != "live" {
		t.Errorf("nature tag = %s", got)
	}
	if got := tagState(t, dst, b, "tag", "4k"); got != "removed" {
		t.Errorf("4k tag = %s, want removed (tombstone beats auto tag)", got)
	}
	if got := tagState(t, dst, b, "genre", "documentary"); got != "live" {
		t.Errorf("documentary genre = %s", got)
	}
	var n int
	dst.QueryRowContext(ctx, `SELECT COUNT(*) FROM collection_items ci JOIN collections c ON c.id = ci.collection_id
		WHERE c.uid = 'c1' AND ci.item_id = ?`, b).Scan(&n)
	if n != 1 {
		t.Errorf("collection membership missing")
	}
	var pos, plays int64
	var fav bool
	dst.QueryRowContext(ctx, `SELECT ps.position_ms, ps.play_count, ps.favorite FROM play_state ps
		JOIN profiles p ON p.id = ps.profile_id WHERE p.uid = 'p1' AND ps.item_id = ?`, b).Scan(&pos, &plays, &fav)
	if pos != 5000 || plays != 2 || !fav {
		t.Errorf("play state = %d %d %v", pos, plays, fav)
	}
	// Search index updated with the new title.
	dst.QueryRowContext(ctx, `SELECT COUNT(*) FROM items_fts WHERE items_fts MATCH '"earth"'`).Scan(&n)
	if n != 1 {
		t.Errorf("search index not updated")
	}

	// Importing the same file again changes nothing.
	if rep := roundTrip(t, src, dst, ModeMerge, false); rep.FieldsUpdated != 0 || rep.ItemTagsAdded != 0 || rep.TagsCreated != 0 {
		t.Errorf("second import not idempotent: %+v", rep)
	}
}

func TestMergeRules(t *testing.T) {
	src, dst := openDB(t), openDB(t)
	srcLib, dstLib := addLibrary(t, src, "L", "/a"), addLibrary(t, dst, "L", "/b")
	a := addItem(t, src, srcLib, "ph1:bbbb", 200, "x.mkv", "x")
	b := addItem(t, dst, dstLib, "ph1:bbbb", 200, "x.mkv", "x")
	old, recent := time.Now().Add(-time.Hour).Unix(), time.Now().Unix()

	// Both sides edited the title; the local edit is newer.
	setField(t, src, a, "title", "Their title", "user", old)
	exec(t, src, `UPDATE items SET updated_at = ? WHERE id = ?`, old, a)
	setField(t, dst, b, "title", "My title", "user", recent)
	// Incoming auto-detected year vs a local user year.
	setField(t, src, a, "year", 1999, "filename", old)
	setField(t, dst, b, "year", 2001, "user", recent)
	// Incoming user description where local is auto.
	setField(t, dst, b, "description", "auto text", "scraper", recent)
	setField(t, src, a, "description", "my notes", "user", old)

	addTag(t, src, a, "tag:keep", "filename", true) // incoming tombstone...
	addTag(t, dst, b, "tag:keep", "user", false)    // ...vs a tag the local user added: user wins
	addTag(t, src, a, "tag:gone", "folder", false)  // incoming auto tag...
	addTag(t, dst, b, "tag:gone", "folder", true)   // ...vs a local tombstone: tombstone wins
	addTag(t, src, a, "tag:back", "user", false)    // incoming user tag...
	addTag(t, dst, b, "tag:back", "folder", true)   // ...vs a local tombstone: user wins

	rep := roundTrip(t, src, dst, ModeMerge, false)
	if v, _ := field(t, dst, b, "title"); v != "My title" {
		t.Errorf("newer local user title lost: %v", v)
	}
	if v, _ := field(t, dst, b, "year"); v != int64(2001) {
		t.Errorf("local user year overwritten by auto value: %v", v)
	}
	if v, s := field(t, dst, b, "description"); v != "my notes" || s != "user" {
		t.Errorf("incoming user description not taken over auto: %v (%s)", v, s)
	}
	if rep.FieldsKept != 2 {
		t.Errorf("fields kept = %d, want 2", rep.FieldsKept)
	}
	for slug, want := range map[string]string{"keep": "live", "gone": "removed", "back": "live"} {
		if got := tagState(t, dst, b, "tag", slug); got != want {
			t.Errorf("tag %s = %s, want %s", slug, got, want)
		}
	}

	// Replace mode: the file wins every conflict.
	roundTrip(t, src, dst, ModeReplace, false)
	if v, _ := field(t, dst, b, "title"); v != "Their title" {
		t.Errorf("replace: title = %v", v)
	}
	if v, _ := field(t, dst, b, "year"); v != int64(1999) {
		t.Errorf("replace: year = %v", v)
	}
	if got := tagState(t, dst, b, "tag", "keep"); got != "removed" {
		t.Errorf("replace: tag keep = %s", got)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	src, dst := openDB(t), openDB(t)
	a := addItem(t, src, addLibrary(t, src, "L", "/a"), "ph1:cccc", 300, "y.mkv", "y")
	setField(t, src, a, "title", "Edited", "user", 1000)
	addItem(t, src, 1, "ph1:dddd", 400, "missing.mkv", "missing") // will be pending
	b := addItem(t, dst, addLibrary(t, dst, "L", "/b"), "ph1:cccc", 300, "y.mkv", "y")

	rep := roundTrip(t, src, dst, ModeMerge, true)
	if !rep.DryRun || rep.FieldsUpdated != 1 || rep.Pending != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if v, _ := field(t, dst, b, "title"); v != "y" {
		t.Errorf("dry run changed the title to %v", v)
	}
	var n int
	dst.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_imports`).Scan(&n)
	if n != 0 {
		t.Errorf("dry run stored %d pending imports", n)
	}
}

func TestPendingAppliedWhenFileAppears(t *testing.T) {
	src, dst := openDB(t), openDB(t)
	srcLib := addLibrary(t, src, "Movies", "/a")
	a := addItem(t, src, srcLib, "ph1:eeee", 500, "Sci-Fi/film.mkv", "film")
	setField(t, src, a, "title", "The Film", "user", 1000)
	addTag(t, src, a, "mood:tense", "user", false)
	exec(t, src, `INSERT INTO profiles(uid, name) VALUES ('p1', 'Ope')`)
	exec(t, src, `INSERT INTO play_state(profile_id, item_id, position_ms, last_played_at) VALUES (1, ?, 7000, 1700000000)`, a)
	reencoded := addItem(t, src, srcLib, "ph1:ffff", 600, "Sci-Fi/other.mkv", "other")
	setField(t, src, reencoded, "title", "Other Film", "user", 1000)

	dstLib := addLibrary(t, dst, "Movies", "/b")
	rep := roundTrip(t, src, dst, ModeMerge, false)
	if rep.Pending != 2 {
		t.Fatalf("pending = %d, want 2", rep.Pending)
	}

	// Later the drive is mounted and a scan finds the same file.
	b := addItem(t, dst, dstLib, "ph1:eeee", 500, "film.mkv", "film")
	if ok, err := ApplyPending(ctx, dst, b, "ph1:eeee", 500, "Movies", "film.mkv"); err != nil || !ok {
		t.Fatalf("ApplyPending = %v, %v", ok, err)
	}
	if v, _ := field(t, dst, b, "title"); v != "The Film" {
		t.Errorf("title = %v", v)
	}
	if got := tagState(t, dst, b, "mood", "tense"); got != "live" {
		t.Errorf("mood tag = %s", got)
	}
	var pos int64
	dst.QueryRowContext(ctx, `SELECT position_ms FROM play_state WHERE item_id = ?`, b).Scan(&pos)
	if pos != 7000 {
		t.Errorf("watch state not applied: %d", pos)
	}

	// A re-encoded file (different hash and size) is matched by library and path.
	c := addItem(t, dst, dstLib, "ph1:9999", 650, "Sci-Fi/other.mkv", "other")
	if ok, _ := ApplyPending(ctx, dst, c, "ph1:9999", 650, "Movies", "Sci-Fi/other.mkv"); !ok {
		t.Fatal("path fallback did not match")
	}
	if v, _ := field(t, dst, c, "title"); v != "Other Film" {
		t.Errorf("title = %v", v)
	}
	var n int
	dst.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_imports`).Scan(&n)
	if n != 0 {
		t.Errorf("%d pending imports left", n)
	}
}

func TestSidecarRoundTrip(t *testing.T) {
	root := t.TempDir()
	src, dst := openDB(t), openDB(t)
	lib := addLibrary(t, src, "L", root)
	exec(t, src, `UPDATE libraries SET write_sidecars = 1 WHERE id = ?`, lib)
	a := addItem(t, src, lib, "ph1:1234", 700, "clip.mp4", "clip")
	setField(t, src, a, "title", "Beach Day", "user", 1000)
	db.EnsureTag(ctx, src, "genre:Home Video")
	exec(t, src, `UPDATE tags SET color = '#ff0000' WHERE slug = 'home-video'`)
	addTag(t, src, a, "genre:home-video", "user", false)

	sidecar := filepath.Join(root, "clip.mp4"+SidecarExt)
	writeSidecar(t, src, a, sidecar)
	data, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	var sc Sidecar
	json.Unmarshal(data, &sc)
	if sc.Format != sidecarFormat || sc.Item.Paths != nil || len(sc.TagDefs) != 1 {
		t.Fatalf("sidecar = %s", data)
	}

	// Copied to another machine and re-encoded (different hash): still applies.
	b := addItem(t, dst, addLibrary(t, dst, "Other", "/x"), "ph1:5678", 800, "clip.mp4", "clip")
	if ok, err := ApplySidecar(ctx, dst, b, sidecar); err != nil || !ok {
		t.Fatalf("ApplySidecar = %v, %v", ok, err)
	}
	if v, _ := field(t, dst, b, "title"); v != "Beach Day" {
		t.Errorf("title = %v", v)
	}
	var color string
	dst.QueryRowContext(ctx, `SELECT color FROM tags WHERE namespace = 'genre' AND slug = 'home-video'`).Scan(&color)
	if color != "#ff0000" {
		t.Errorf("tag definition not carried: color %q", color)
	}
	if ok, _ := ApplySidecar(ctx, dst, b, filepath.Join(root, "none.meta.json")); ok {
		t.Error("missing sidecar reported as applied")
	}
}

// writeSidecar writes a per-video sidecar as earlier versions did.
func writeSidecar(t *testing.T, d *sql.DB, itemID int64, path string) {
	t.Helper()
	sc, err := BuildSidecar(ctx, d, itemID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(sc, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadLibraryRejectsBadFiles(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      "hello",
		"wrong format":  `{"format":"something-else","version":"1.0"}`,
		"newer version": `{"format":"itan-library","version":"2.0"}`,
	} {
		if _, err := ReadLibrary(bytes.NewReader([]byte(body))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The example in the docs parses.
	f, err := os.Open("../../docs/examples/library-export.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lib, err := ReadLibrary(f)
	if err != nil || len(lib.Items) != 2 || len(lib.Profiles) != 1 {
		t.Errorf("docs example: %v (%d items)", err, len(lib.Items))
	}
}
