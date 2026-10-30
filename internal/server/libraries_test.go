package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/hardope/itan/internal/metadata"
)

// An edit to a video in a library inside another library is saved in the
// outer library's folder file; Settings shows which library is in which.
func TestNestedLibraries(t *testing.T) {
	srv, s, _ := testServer(t)
	ctx := context.Background()
	root := s.cfg.DataDir // library 1's folder
	music := filepath.Join(root, "Music")
	if err := os.Mkdir(music, 0o755); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`UPDATE libraries SET name = 'Main', write_sidecars = 1 WHERE id = 1`)
	mustExec(`INSERT INTO libraries(id, name, root_path, default_kind) VALUES (2, 'Music', ?, 'music_video')`, music)
	var song int64
	s.db.QueryRowContext(ctx, `INSERT INTO items(content_hash, size_bytes, title) VALUES ('ph1:2', 2, 'song') RETURNING id`).Scan(&song)
	mustExec(`INSERT INTO files(item_id, library_id, rel_path, size_bytes, mtime) VALUES (?, 2, 'song.mp4', 2, 0)`, song)

	c := client(t)
	if code, body := call(t, c, "PATCH", srv.URL+"/api/items/"+strconv.FormatInt(song, 10), `{"title":"Zion"}`); code != http.StatusOK {
		t.Fatalf("edit: %d %s", code, body)
	}
	s.folders.Close() // write now instead of in two seconds
	data, err := os.ReadFile(filepath.Join(root, metadata.FolderFileName))
	if err != nil || !strings.Contains(string(data), `"Music/song.mp4"`) || !strings.Contains(string(data), "Zion") {
		t.Fatalf("Main's folder file lacks the edit: %v\n%s", err, data)
	}

	_, body := call(t, c, "GET", srv.URL+"/api/libraries", "")
	var libs []libraryJSON
	if err := json.Unmarshal([]byte(body), &libs); err != nil {
		t.Fatal(err)
	}
	byName := map[string]libraryJSON{}
	for _, l := range libs {
		byName[l.Name] = l
	}
	if m := byName["Music"]; m.Inside != "Main" || !m.InsideKeep {
		t.Errorf("Music: inside %q, keeps %v", m.Inside, m.InsideKeep)
	}
	if m := byName["Main"]; len(m.Contains) != 1 || m.Contains[0] != "Music" || m.Inside != "" {
		t.Errorf("Main: contains %v, inside %q", m.Contains, m.Inside)
	}

	// The same folder can't be added twice, however it's typed.
	same := music
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		same = filepath.Join(root, "MUSIC")
	}
	b, _ := json.Marshal(map[string]string{"path": same})
	if code, body := call(t, c, "POST", srv.URL+"/api/libraries", string(b)); code != http.StatusConflict {
		t.Errorf("adding %s again: %d %s", same, code, body)
	}
}
