package db

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestWithin(t *testing.T) {
	j := filepath.Join
	sep := string(filepath.Separator)
	root := j(sep, "Volumes", "Drive")
	cases := []struct {
		root, path, rel string
		ok              bool
	}{
		{root, j(root, "Music"), "Music", true},
		{root, j(root, "Music", "Gospel"), j("Music", "Gospel"), true},
		{root + sep, j(root, "Music"), "Music", true},
		{root, root, "", false},
		{root, root + "2", "", false}, // a sibling with a longer name
		{j(root, "Music"), root, "", false},
		{sep, j(sep, "Media"), "Media", true},
		{"Music", j("Music", "a.mp4"), "a.mp4", true}, // relative paths too
		{"", j(root, "Music"), "", false},
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		cases = append(cases, struct {
			root, path, rel string
			ok              bool
		}{j(sep, "volumes", "drive"), j(root, "Music"), "Music", true})
	}
	for _, c := range cases {
		rel, ok := Within(c.root, c.path)
		if rel != c.rel || ok != c.ok {
			t.Errorf("Within(%q, %q) = %q, %v; want %q, %v", c.root, c.path, rel, ok, c.rel, c.ok)
		}
	}
}
