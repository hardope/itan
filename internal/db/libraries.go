package db

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
)

// Libraries can be nested: a library inside another library's folder owns
// that folder. Its videos are scanned only there, so they get its default
// kind, and the outer library skips it. The outer library's folder file
// still covers it, with paths relative to the outer folder.

// Nested is a library inside another one.
type Nested struct {
	ID  int64
	Rel string // its folder relative to the outer library's, OS separators
}

// NestedLibraries returns the enabled libraries inside library id's folder.
func NestedLibraries(ctx context.Context, q Execer, id int64) ([]Nested, error) {
	roots, err := libraryRoots(ctx, q)
	if err != nil {
		return nil, err
	}
	var out []Nested
	for other, root := range roots {
		if rel, ok := Within(roots[id], root); ok && other != id {
			out = append(out, Nested{ID: other, Rel: rel})
		}
	}
	return out, nil
}

// ContainingLibraries returns library id and the enabled libraries whose
// folders contain it.
func ContainingLibraries(ctx context.Context, q Execer, id int64) ([]int64, error) {
	roots, err := libraryRoots(ctx, q)
	if err != nil {
		return nil, err
	}
	out := []int64{id}
	for other, root := range roots {
		if _, ok := Within(root, roots[id]); ok && other != id {
			out = append(out, other)
		}
	}
	return out, nil
}

func libraryRoots(ctx context.Context, q Execer) (map[int64]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, root_path FROM libraries WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roots := map[int64]string{}
	for rows.Next() {
		var id int64
		var root string
		if err := rows.Scan(&id, &root); err != nil {
			return nil, err
		}
		roots[id] = root
	}
	return roots, rows.Err()
}

// Within reports whether path is inside the folder root (and not root
// itself), and returns it relative to root.
func Within(root, path string) (string, bool) {
	if root == "" || path == "" {
		return "", false
	}
	root, path = filepath.Clean(root), filepath.Clean(path)
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if len(path) <= len(prefix) || !SamePath(path[:len(prefix)], prefix) {
		return "", false
	}
	return path[len(prefix):], true
}

// SamePath compares paths the way the file system does: ignoring case on
// macOS and Windows, whose disks usually are case-insensitive.
func SamePath(a, b string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
