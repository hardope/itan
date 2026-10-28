package metadata

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFolder(t *testing.T, root string) *Library {
	t.Helper()
	lib, err := readFolderFile(filepath.Join(root, FolderFileName))
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

func mustSync(t *testing.T, d *sql.DB, libID int64) FolderResult {
	t.Helper()
	res, err := SyncFolderFile(ctx, d, libID)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The drive goes from one machine to another, where the folder has a
// different library name and one video was renamed.
func TestFolderFileTravelsWithTheDrive(t *testing.T) {
	root := t.TempDir()
	a, b := openDB(t), openDB(t)
	libA := addLibrary(t, a, "Main", root)
	exec(t, a, `UPDATE libraries SET write_sidecars = 1 WHERE id = ?`, libA)
	edited := addItem(t, a, libA, "ph1:aaaa", 100, "Movies/clip one.mp4", "clip one")
	addItem(t, a, libA, "ph1:bbbb", 200, "untouched.mp4", "untouched")
	setField(t, a, edited, "title", "Beach Day", "user", 1000)
	addTag(t, a, edited, "genre:home-video", "user", false)

	if res := mustSync(t, a, libA); !res.Written {
		t.Fatalf("not written: %+v", res)
	}
	file := readFolder(t, root)
	if len(file.Items) != 1 || file.Items[0].Paths[0].Path != "Movies/clip one.mp4" || len(file.Profiles) != 0 {
		t.Fatalf("folder file items = %+v", file.Items)
	}
	if res := mustSync(t, a, libA); res.Written || res.Read {
		t.Errorf("unchanged library rewrote or re-read its own file: %+v", res)
	}

	// Machine B: same files, folder called "Drive", the clip renamed.
	libB := addLibrary(t, b, "Drive", root)
	clip := addItem(t, b, libB, "ph1:aaaa", 100, "Movies/renamed.mp4", "renamed")
	res := mustSync(t, b, libB) // B doesn't keep a file: read only
	if !res.Read || res.Changes != 2 || res.Written {
		t.Fatalf("B sync = %+v", res)
	}
	if v, src := field(t, b, clip, "title"); v != "Beach Day" || src != "user" {
		t.Errorf("title = %v (%s)", v, src)
	}
	if tagState(t, b, clip, "genre", "home-video") != "live" {
		t.Error("tag not applied")
	}
	if res := mustSync(t, b, libB); res.Read {
		t.Error("read again although the file didn't change")
	}
}

// A re-encoded file (new hash) at the same place in the folder still gets
// its edits, whatever the folder is called.
func TestFolderFileMatchesPathWithinFolder(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	a, b := openDB(t), openDB(t)
	libA := addLibrary(t, a, "Main", root)
	exec(t, a, `UPDATE libraries SET write_sidecars = 1 WHERE id = ?`, libA)
	x := addItem(t, a, libA, "ph1:1111", 100, "show/ep1.mkv", "ep1")
	setField(t, a, x, "title", "Pilot", "user", 1000)
	mustSync(t, a, libA)

	// B has a different library with the same relative path: it must not match.
	decoy := addItem(t, b, addLibrary(t, b, "Elsewhere", other), "ph1:3333", 300, "show/ep1.mkv", "decoy")
	libB := addLibrary(t, b, "USB", root)
	reencoded := addItem(t, b, libB, "ph1:2222", 50, "show/ep1.mkv", "ep1")
	mustSync(t, b, libB)
	if v, _ := field(t, b, reencoded, "title"); v != "Pilot" {
		t.Errorf("re-encoded file title = %v", v)
	}
	if v, _ := field(t, b, decoy, "title"); v != "decoy" {
		t.Errorf("file in another library changed: %v", v)
	}
}

// Edits on two machines come together: each keeps its newer edits.
func TestFolderFileMergesBothWays(t *testing.T) {
	root := t.TempDir()
	a, b := openDB(t), openDB(t)
	libA, libB := addLibrary(t, a, "A", root), addLibrary(t, b, "B", root)
	for _, d := range []*sql.DB{a, b} {
		exec(t, d, `UPDATE libraries SET write_sidecars = 1`)
	}
	ia := addItem(t, a, libA, "ph1:aaaa", 100, "x.mp4", "x")
	ib := addItem(t, b, libB, "ph1:aaaa", 100, "x.mp4", "x")

	setField(t, a, ia, "title", "Title from A", "user", 1000)
	mustSync(t, a, libA)
	mustSync(t, b, libB) // B reads A's title, writes it back with nothing new
	setField(t, b, ib, "year", 1999, "user", time.Now().Unix())
	exec(t, b, `UPDATE items SET updated_at = ? WHERE id = ?`, time.Now().Unix()+5, ib)
	mustSync(t, b, libB)
	mustSync(t, a, libA)

	if v, _ := field(t, a, ia, "year"); v != int64(1999) {
		t.Errorf("A year = %v", v)
	}
	if v, _ := field(t, b, ib, "title"); v != "Title from A" {
		t.Errorf("B title = %v", v)
	}
}

// Per-video sidecars from earlier versions are folded in, then removed.
func TestFolderFileReplacesSidecars(t *testing.T) {
	root := t.TempDir()
	d := openDB(t)
	lib := addLibrary(t, d, "L", root)
	x := addItem(t, d, lib, "ph1:aaaa", 100, "a.mp4", "a")
	y := addItem(t, d, lib, "ph1:bbbb", 200, "b.mp4", "b")
	setField(t, d, x, "title", "Old title", "user", 1000)
	writeSidecar(t, d, x, filepath.Join(root, "a.mp4"+SidecarExt))
	// Someone edited y's sidecar on another machine; this install never saw it.
	setField(t, d, y, "title", "Edited elsewhere", "user", 2000)
	writeSidecar(t, d, y, filepath.Join(root, "b.mp4"+SidecarExt))
	setField(t, d, y, "title", "b", "filename", 0)
	exec(t, d, `DELETE FROM item_field_sources WHERE item_id = ?`, y)
	// Not one of ours: left alone.
	os.WriteFile(filepath.Join(root, "notes.mp4"+SidecarExt), []byte(`{"hello": 1}`), 0o644)

	exec(t, d, `UPDATE libraries SET write_sidecars = 1 WHERE id = ?`, lib)
	res := mustSync(t, d, lib)
	if res.Migrated != 2 || !res.Written {
		t.Fatalf("sync = %+v", res)
	}
	for _, name := range []string{"a.mp4", "b.mp4"} {
		if _, err := os.Stat(filepath.Join(root, name+SidecarExt)); !os.IsNotExist(err) {
			t.Errorf("%s sidecar still there", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "notes.mp4"+SidecarExt)); err != nil {
		t.Error("removed a file that wasn't a itan sidecar")
	}
	if v, _ := field(t, d, y, "title"); v != "Edited elsewhere" {
		t.Errorf("sidecar edit lost: %v", v)
	}
	if n := len(readFolder(t, root).Items); n != 2 {
		t.Errorf("folder file has %d items, want 2", n)
	}
}

// A file this build can't read is never overwritten.
func TestFolderFileBadFileKept(t *testing.T) {
	root := t.TempDir()
	d := openDB(t)
	lib := addLibrary(t, d, "L", root)
	exec(t, d, `UPDATE libraries SET write_sidecars = 1 WHERE id = ?`, lib)
	x := addItem(t, d, lib, "ph1:aaaa", 100, "a.mp4", "a")
	setField(t, d, x, "title", "Mine", "user", 1000)
	path := filepath.Join(root, FolderFileName)
	future := `{"format": "itan-library", "version": "9.0", "items": []}`
	os.WriteFile(path, []byte(future), 0o644)

	if _, err := SyncFolderFile(ctx, d, lib); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("err = %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != future {
		t.Error("unreadable file was overwritten")
	}
}

func TestFolderFileIsAnImportableExport(t *testing.T) {
	root := t.TempDir()
	d := openDB(t)
	lib := addLibrary(t, d, "L", root)
	x := addItem(t, d, lib, "ph1:aaaa", 100, "a.mp4", "a")
	setField(t, d, x, "title", "T", "user", 1000)
	built, err := BuildFolderFile(ctx, d, lib)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(built)
	if _, err := ReadLibrary(strings.NewReader(string(data))); err != nil {
		t.Fatalf("folder file isn't a valid export: %v", err)
	}
}

// The drive moves to B, and B saves an edit before scanning: A's edits
// already in the file must survive.
func TestFolderFileWriteKeepsOtherInstallsEdits(t *testing.T) {
	root := t.TempDir()
	a, b := openDB(t), openDB(t)
	libA, libB := addLibrary(t, a, "A", root), addLibrary(t, b, "B", root)
	for _, d := range []*sql.DB{a, b} {
		exec(t, d, `UPDATE libraries SET write_sidecars = 1`)
	}
	ia := addItem(t, a, libA, "ph1:aaaa", 100, "x.mp4", "x")
	addItem(t, a, libA, "ph1:bbbb", 200, "y.mp4", "y")
	addItem(t, b, libB, "ph1:aaaa", 100, "x.mp4", "x")
	ib := addItem(t, b, libB, "ph1:bbbb", 200, "y.mp4", "y")
	mustSync(t, b, libB) // B has seen the (empty) file once

	setField(t, a, ia, "title", "Edited on A", "user", 1000)
	if _, err := WriteFolderFile(ctx, a, libA); err != nil {
		t.Fatal(err)
	}
	setField(t, b, ib, "title", "Edited on B", "user", 2000)
	res, err := WriteFolderFile(ctx, b, libB)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Read || !res.Written {
		t.Fatalf("B write = %+v, want it to read A's version first", res)
	}
	titles := map[string]any{}
	for _, it := range readFolder(t, root).Items {
		titles[it.Paths[0].Path] = it.Fields["title"].Value
	}
	if titles["x.mp4"] != "Edited on A" || titles["y.mp4"] != "Edited on B" {
		t.Fatalf("file titles = %v", titles)
	}
}
