package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// The folder picker lists folders only, hidden ones left out, sorted
// without regard to case, and starts from places when given no path.
func TestBrowseFolders(t *testing.T) {
	srv, _, _ := testServer(t)
	dir := t.TempDir()
	for _, name := range []string{"b", "A", ".hidden", "$RECYCLE.BIN"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "film.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := client(t)
	list := func(path string) (int, struct {
		Path, Parent string
		Folders      []folderEntry
	}) {
		t.Helper()
		var res struct {
			Path, Parent string
			Folders      []folderEntry
		}
		code, body := call(t, c, "GET", srv.URL+"/api/folders?path="+url.QueryEscape(path), "")
		if code == http.StatusOK {
			if err := json.Unmarshal([]byte(body), &res); err != nil {
				t.Fatal(err)
			}
		}
		return code, res
	}

	code, res := list(dir)
	if code != http.StatusOK {
		t.Fatalf("listing %s: %d", dir, code)
	}
	var names []string
	for _, f := range res.Folders {
		names = append(names, f.Name)
	}
	if len(names) != 2 || names[0] != "A" || names[1] != "b" {
		t.Errorf("folders = %v, want [A b]", names)
	}
	if res.Folders[0].Path != filepath.Join(dir, "A") || res.Parent != filepath.Dir(dir) {
		t.Errorf("paths: %+v, parent %q", res.Folders[0], res.Parent)
	}

	if code, _ := list("relative/path"); code != http.StatusBadRequest {
		t.Errorf("relative path: %d, want 400", code)
	}
	if code, _ := list(filepath.Join(dir, "film.mp4")); code != http.StatusBadRequest {
		t.Errorf("a file: %d, want 400", code)
	}
	if code, res := list(""); code != http.StatusOK || res.Folders == nil || res.Path != "" {
		t.Errorf("places: %d %+v", code, res)
	}
}
