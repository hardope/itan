package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hardope/itan/internal/db"
)

// A library inside another library's folder has that folder to itself: its
// videos get its kind, aren't listed twice, and keep their edits when they
// move in.
func TestNestedLibrary(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	itemAt := func(rel string) (id int64, lib, kind string) {
		t.Helper()
		err := d.QueryRowContext(ctx, `
			SELECT i.id, l.name, i.kind FROM files f JOIN items i ON i.id = f.item_id JOIN libraries l ON l.id = f.library_id
			 WHERE f.rel_path = ? AND f.missing_since IS NULL`, filepath.FromSlash(rel)).Scan(&id, &lib, &kind)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		return id, lib, kind
	}
	files := func() int {
		var n int
		d.QueryRowContext(ctx, `SELECT COUNT(*) FROM files`).Scan(&n)
		return n
	}
	sc := New(d, filepath.Join(root, "no-ffprobe"))
	scan := func() {
		t.Helper()
		// Scans time files to the second: make the last one older.
		exec(`UPDATE files SET last_seen_at = last_seen_at - 60`)
		if err := sc.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}

	exec(`INSERT INTO libraries(id, name, root_path, default_kind) VALUES (1, 'Main', ?, 'movie')`, root)
	write("film.mp4", "film")
	write("song.mp4", "song")
	scan()
	song, _, _ := itemAt("song.mp4")
	exec(`UPDATE items SET kind = 'music_video', title = 'Zion' WHERE id = ?`, song)
	exec(`INSERT INTO item_field_sources(item_id, field, source) VALUES (?, 'title', 'user'), (?, 'kind', 'user')
		ON CONFLICT(item_id, field) DO UPDATE SET source = 'user'`, song, song)

	// Make a Music folder its own library, move the song in, add a new one.
	music := filepath.Join(root, "Music")
	if err := os.Mkdir(music, 0o755); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO libraries(id, name, root_path, default_kind) VALUES (2, 'Music', ?, 'music_video')`, music)
	if err := os.Rename(filepath.Join(root, "song.mp4"), filepath.Join(music, "song.mp4")); err != nil {
		t.Fatal(err)
	}
	write("Music/new.mp4", "new song")
	write("new film.mp4", "new film")
	scan()

	if id, lib, kind := itemAt("song.mp4"); id != song || lib != "Music" || kind != "music_video" {
		t.Errorf("moved song: item %d in %s as %s; want item %d in Music", id, lib, kind, song)
	}
	var title string
	d.QueryRowContext(ctx, `SELECT title FROM items WHERE id = ?`, song).Scan(&title)
	if title != "Zion" {
		t.Errorf("moved song lost its title: %q", title)
	}
	if _, lib, kind := itemAt("new.mp4"); lib != "Music" || kind != "music_video" {
		t.Errorf("new song in %s as %s; want Music, music_video", lib, kind)
	}
	if _, lib, kind := itemAt("new film.mp4"); lib != "Main" || kind != "movie" {
		t.Errorf("new film in %s as %s; want Main, movie", lib, kind)
	}
	// The song's old place is forgotten rather than kept as missing (it's
	// in Music now), and Main lists nothing in Music.
	if n := files(); n != 4 {
		t.Errorf("%d file rows, want 4 (one per file)", n)
	}
	if st := sc.Status(); st.Seen != 4 {
		t.Errorf("scan saw %d files, want 4", st.Seen)
	}

	// Without the Music library, Main finds those videos again, edits intact.
	exec(`DELETE FROM libraries WHERE id = 2`)
	scan()
	if id, lib, _ := itemAt("Music/song.mp4"); id != song || lib != "Main" {
		t.Errorf("after removing Music: item %d in %s", id, lib)
	}
	var present int
	d.QueryRowContext(ctx, `SELECT COUNT(*) FROM files WHERE missing_since IS NULL`).Scan(&present)
	if present != 4 {
		t.Errorf("%d files present, want 4", present)
	}
}

// Titles that came from file names follow the names (when a file is
// renamed, or reading names improves); titles someone set don't.
func TestRetitle(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	root := t.TempDir()
	for name, content := range map[string]string{
		"YTDown.com_YouTube_Media_abcdefghijk_Tesla-Before-Elon-The-Untold-Story_001_1080p.mp4": "tesla",
		"Edited-Latest-SnapYT.App.mp4": "edited",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO libraries(id, name, root_path) VALUES (1, 'Main', ?)`, root); err != nil {
		t.Fatal(err)
	}
	sc := New(d, filepath.Join(root, "no-ffprobe"))
	scan := func() {
		t.Helper()
		d.ExecContext(ctx, `UPDATE files SET last_seen_at = last_seen_at - 60`)
		if err := sc.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	title := func(content string) string {
		t.Helper()
		var title string
		hash, _, err := ContentHash(filepath.Join(root, map[string]string{
			"tesla": "Tesla Before Elon.mp4", "edited": "Edited-Latest-SnapYT.App.mp4"}[content]))
		if err != nil {
			t.Fatal(err)
		}
		d.QueryRowContext(ctx, `SELECT title FROM items WHERE content_hash = ?`, hash).Scan(&title)
		return title
	}
	scan()
	// As if an older version had named them, and someone edited one.
	d.ExecContext(ctx, `UPDATE items SET title = 'YTDown com YouTube Media abcdefghijk Tesla'`)
	d.ExecContext(ctx, `UPDATE item_field_sources SET source = 'user'
		WHERE field = 'title' AND item_id = (SELECT item_id FROM files WHERE rel_path LIKE 'Edited%')`)
	if err := os.Rename(filepath.Join(root, "YTDown.com_YouTube_Media_abcdefghijk_Tesla-Before-Elon-The-Untold-Story_001_1080p.mp4"),
		filepath.Join(root, "Tesla Before Elon.mp4")); err != nil {
		t.Fatal(err)
	}
	scan()
	if got := title("tesla"); got != "Tesla Before Elon" {
		t.Errorf("title from the file name: %q, want it to follow the rename", got)
	}
	if got := title("edited"); got != "YTDown com YouTube Media abcdefghijk Tesla" {
		t.Errorf("edited title changed to %q", got)
	}
}
