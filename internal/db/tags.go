package db

import (
	"context"
	"regexp"
	"strings"
)

var namespaces = map[string]bool{"genre": true, "tag": true, "person": true, "mood": true, "source": true}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns "Live Concert!" into "live-concert".
func Slugify(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// ParseTagRef splits "genre:Music" into ("genre", "music", "Music").
// Without a known namespace prefix the namespace is "tag".
func ParseTagRef(ref string) (namespace, slug, name string) {
	ref = strings.TrimSpace(ref)
	namespace, name = "tag", ref
	if ns, rest, ok := strings.Cut(ref, ":"); ok && namespaces[strings.ToLower(ns)] {
		namespace, name = strings.ToLower(ns), strings.TrimSpace(rest)
	}
	return namespace, Slugify(name), name
}

// EnsureTag returns the id of the tag for ref, creating it if needed.
// Returns 0 if ref has no usable characters.
func EnsureTag(ctx context.Context, q Execer, ref string) (int64, error) {
	ns, slug, name := ParseTagRef(ref)
	if slug == "" {
		return 0, nil
	}
	// Title-case display names that arrive all lowercase (from folder names).
	if name == strings.ToLower(name) {
		name = titleCase(name)
	}
	var id int64
	err := q.QueryRowContext(ctx, `
		INSERT INTO tags(namespace, slug, name) VALUES (?, ?, ?)
		ON CONFLICT(namespace, slug) DO UPDATE SET slug = excluded.slug
		RETURNING id`, ns, slug, name).Scan(&id)
	return id, err
}

func titleCase(s string) string {
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(s))
	for i, w := range words {
		r := []rune(w)
		words[i] = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	return strings.Join(words, " ")
}
