package server

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// folderEntry is a folder the folder picker can show.
type folderEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// browseFolders lists the folders inside ?path=, for picking a library
// folder without typing its path. Without a path it lists places to start
// from: home, its Movies or Videos folder, and drives.
func (s *Server) browseFolders(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeJSON(w, http.StatusOK, map[string]any{"path": "", "parent": "", "folders": places()})
		return
	}
	if !filepath.IsAbs(path) {
		writeError(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	path = filepath.Clean(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "That folder can't be opened.")
		return
	}
	folders := []folderEntry{}
	for _, e := range entries {
		name := e.Name()
		if hiddenFolder(name) {
			continue
		}
		full := filepath.Join(path, name)
		if !e.IsDir() {
			// Symlinks to folders count (macOS volumes, Docker mounts).
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if st, err := os.Stat(full); err != nil || !st.IsDir() {
				continue
			}
		}
		folders = append(folders, folderEntry{Name: name, Path: full})
	}
	slices.SortFunc(folders, func(a, b folderEntry) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	parent := filepath.Dir(path)
	if parent == path {
		parent = "" // a drive or / itself: back to the places
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "parent": parent, "folders": folders})
}

// hiddenFolder reports folders people don't keep videos in: dot folders
// and Windows' system folders.
func hiddenFolder(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") ||
		name == "System Volume Information" || name == "lost+found"
}

// places are where the folder picker starts.
func places() []folderEntry {
	var out []folderEntry
	add := func(name, path string) {
		if st, err := os.Stat(path); err == nil && st.IsDir() &&
			!slices.ContainsFunc(out, func(p folderEntry) bool { return p.Path == path }) {
			out = append(out, folderEntry{Name: name, Path: path})
		}
	}
	listed := func(dir string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if hiddenFolder(e.Name()) {
				continue
			}
			full := filepath.Join(dir, e.Name())
			// macOS lists the startup disk in /Volumes as a link to /.
			if target, err := filepath.EvalSymlinks(full); err == nil && target == "/" {
				continue
			}
			add(e.Name(), full)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add("Home", home)
		add("Movies", filepath.Join(home, "Movies"))
		add("Videos", filepath.Join(home, "Videos"))
	}
	switch runtime.GOOS {
	case "darwin":
		listed("/Volumes")
	case "windows":
		for d := 'A'; d <= 'Z'; d++ {
			drive := string(d) + `:\`
			add(drive, drive)
		}
	default:
		listed("/media") // Docker mounts, and USB drives on some systems
		if u := os.Getenv("USER"); u != "" {
			listed(filepath.Join("/media", u))
			listed(filepath.Join("/run/media", u))
		}
		listed("/mnt")
		add("Computer", "/")
	}
	if out == nil {
		out = []folderEntry{}
	}
	return out
}
