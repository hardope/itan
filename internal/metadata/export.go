package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hardope/itan/internal/db"
)

// Export builds a whole-library export. Every item is included, even ones
// whose files are currently missing: their metadata is still worth keeping.
func Export(ctx context.Context, database *sql.DB, includeHistory bool, generator string) (*Library, error) {
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() // read-only: nothing to commit

	lib := &Library{
		Format:     libraryFormat,
		Version:    formatVersion,
		ExportedAt: time.Now().UTC().Truncate(time.Second),
		Generator:  generator,
		Scope:      Scope{IncludeHistory: includeHistory, Libraries: []string{}},
		Libraries:  []LibraryInfo{},
	}

	rows, err := tx.QueryContext(ctx, `SELECT name, default_kind, folder_tags FROM libraries ORDER BY name`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l LibraryInfo
		if err := rows.Scan(&l.Name, &l.DefaultKind, &l.FolderTags); err != nil {
			rows.Close()
			return nil, err
		}
		lib.Libraries = append(lib.Libraries, l)
		lib.Scope.Libraries = append(lib.Scope.Libraries, l.Name)
	}
	rows.Close()

	if lib.Tags, err = loadTagDefs(ctx, tx, nil); err != nil {
		return nil, err
	}
	if lib.Items, _, err = loadItems(ctx, tx, nil); err != nil {
		return nil, err
	}
	if lib.Collections, err = loadCollections(ctx, tx); err != nil {
		return nil, err
	}
	if includeHistory {
		if lib.Profiles, err = loadProfiles(ctx, tx); err != nil {
			return nil, err
		}
	}
	return lib, nil
}

// loadItems builds item objects for the given items, or all of them if ids
// is nil. It also returns each item's local id, in the same order.
func loadItems(ctx context.Context, q db.Execer, ids []int64) ([]Item, []int64, error) {
	where, itemFilter := "", ""
	if ids != nil {
		where, itemFilter = "WHERE i.id IN ("+idList(ids)+")", " WHERE item_id IN ("+idList(ids)+")"
	}

	byID := map[int64]*Item{}
	var order []int64
	rows, err := q.QueryContext(ctx, `
		SELECT i.id, i.content_hash, i.size_bytes, `+strings.Join(prefixed("i.", exportFields), ", ")+`,
		       i.external_ids, i.added_at, i.updated_at, i.duration_ms, i.width, i.height, i.video_codec
		  FROM items i `+where+` ORDER BY i.id`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, added, updated int64
		var it Item
		var extIDs sql.NullString
		var tech Technical
		values := make([]any, len(exportFields))
		dest := []any{&id, &it.Hash, &it.Size}
		for i := range values {
			dest = append(dest, &values[i])
		}
		dest = append(dest, &extIDs, &added, &updated, &tech.DurationMS, &tech.Width, &tech.Height, &tech.VideoCodec)
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return nil, nil, err
		}
		it.Fields = map[string]Field{}
		for i, name := range exportFields {
			if v := normalizeValue(values[i]); v != nil {
				it.Fields[name] = Field{Value: v, Source: "filename"} // real source filled in below
			}
		}
		it.Tags = []ItemTag{}
		if extIDs.Valid && extIDs.String != "" {
			json.Unmarshal([]byte(extIDs.String), &it.ExternalIDs)
		}
		it.AddedAt, it.UpdatedAt = unixTime(added), unixTime(updated)
		if tech != (Technical{}) {
			it.Technical = &tech
		}
		byID[id] = &it
		order = append(order, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	err = eachRow(ctx, q, `SELECT item_id, field, source FROM item_field_sources`+itemFilter, nil, func(r *sql.Rows) error {
		var id int64
		var field, source string
		if err := r.Scan(&id, &field, &source); err != nil {
			return err
		}
		if it := byID[id]; it != nil {
			if f, ok := it.Fields[field]; ok {
				f.Source = source
				it.Fields[field] = f
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	err = eachRow(ctx, q, `
		SELECT it.item_id, t.namespace || ':' || t.slug, it.source, it.removed
		  FROM item_tags it JOIN tags t ON t.id = it.tag_id`+strings.Replace(itemFilter, "item_id", "it.item_id", 1)+`
		 ORDER BY t.namespace, t.slug`, nil, func(r *sql.Rows) error {
		var id int64
		var t ItemTag
		if err := r.Scan(&id, &t.Ref, &t.Source, &t.Removed); err != nil {
			return err
		}
		if it := byID[id]; it != nil {
			it.Tags = append(it.Tags, t)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	err = eachRow(ctx, q, `
		SELECT f.item_id, l.name, f.rel_path FROM files f JOIN libraries l ON l.id = f.library_id`+
		strings.Replace(itemFilter, "item_id", "f.item_id", 1)+`
		 ORDER BY f.missing_since IS NOT NULL, f.last_seen_at DESC`, nil, func(r *sql.Rows) error {
		var id int64
		var p PathHint
		if err := r.Scan(&id, &p.Library, &p.Path); err != nil {
			return err
		}
		p.Path = filepath.ToSlash(p.Path) // the file format uses /, like URLs
		if it := byID[id]; it != nil {
			it.Paths = append(it.Paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	items := make([]Item, 0, len(order))
	for _, id := range order {
		items = append(items, *byID[id])
	}
	return items, order, nil
}

// loadTagDefs returns tag definitions: all of them, or (refs != nil) those
// in refs plus their ancestors, so a sidecar is self-contained.
func loadTagDefs(ctx context.Context, q db.Execer, refs map[string]bool) ([]TagDef, error) {
	all := map[string]TagDef{}
	var order []string
	err := eachRow(ctx, q, `
		SELECT t.namespace || ':' || t.slug, t.name, p.namespace || ':' || p.slug, t.color
		  FROM tags t LEFT JOIN tags p ON p.id = t.parent_id
		 ORDER BY t.namespace, t.slug`, nil, func(r *sql.Rows) error {
		var d TagDef
		if err := r.Scan(&d.Ref, &d.Name, &d.Parent, &d.Color); err != nil {
			return err
		}
		all[d.Ref] = d
		order = append(order, d.Ref)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if refs != nil {
		// Pull in ancestors.
		for ref := range refs {
			for p := all[ref].Parent; p != nil && !refs[*p]; p = all[*p].Parent {
				refs[*p] = true
			}
		}
	}
	defs := []TagDef{}
	for _, ref := range order {
		if refs == nil || refs[ref] {
			defs = append(defs, all[ref])
		}
	}
	return defs, nil
}

func loadCollections(ctx context.Context, q db.Execer) ([]Collection, error) {
	byID := map[int64]*Collection{}
	var order []int64
	err := eachRow(ctx, q, `
		SELECT c.id, c.uid, c.kind, c.title, c.description, p.uid, c.position
		  FROM collections c LEFT JOIN collections p ON p.id = c.parent_id
		 ORDER BY c.parent_id IS NOT NULL, c.id`, nil, func(r *sql.Rows) error {
		var id int64
		c := Collection{Items: []CollectionItem{}}
		if err := r.Scan(&id, &c.UID, &c.Kind, &c.Title, &c.Description, &c.Parent, &c.Position); err != nil {
			return err
		}
		byID[id] = &c
		order = append(order, id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = eachRow(ctx, q, `
		SELECT ci.collection_id, i.content_hash, ci.position
		  FROM collection_items ci JOIN items i ON i.id = ci.item_id
		 ORDER BY ci.collection_id, ci.position`, nil, func(r *sql.Rows) error {
		var id int64
		var ci CollectionItem
		if err := r.Scan(&id, &ci.Hash, &ci.Position); err != nil {
			return err
		}
		if c := byID[id]; c != nil {
			c.Items = append(c.Items, ci)
		}
		return nil
	})
	out := []Collection{}
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, err
}

func loadProfiles(ctx context.Context, q db.Execer) ([]Profile, error) {
	byID := map[int64]*Profile{}
	var order []int64
	err := eachRow(ctx, q, `SELECT id, uid, name FROM profiles ORDER BY id`, nil, func(r *sql.Rows) error {
		var id int64
		p := Profile{State: []PlayState{}}
		if err := r.Scan(&id, &p.UID, &p.Name); err != nil {
			return err
		}
		byID[id] = &p
		order = append(order, id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = eachRow(ctx, q, `
		SELECT ps.profile_id, i.content_hash, ps.position_ms, ps.completed, ps.play_count,
		       ps.favorite, ps.last_played_at
		  FROM play_state ps JOIN items i ON i.id = ps.item_id
		 ORDER BY ps.profile_id, ps.last_played_at DESC`, nil, func(r *sql.Rows) error {
		var id int64
		var lastPlayed sql.NullInt64
		var s PlayState
		if err := r.Scan(&id, &s.Hash, &s.PositionMS, &s.Completed, &s.PlayCount, &s.Favorite, &lastPlayed); err != nil {
			return err
		}
		if lastPlayed.Valid {
			s.LastPlayedAt = unixTime(lastPlayed.Int64)
		}
		if p := byID[id]; p != nil {
			p.State = append(p.State, s)
		}
		return nil
	})
	out := []Profile{}
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, err
}

// BuildSidecar returns the per-video sidecar object for one item. Sidecars
// are no longer written (see FolderFileName); this remains for tests and tools.
func BuildSidecar(ctx context.Context, q db.Execer, itemID int64) (*Sidecar, error) {
	items, _, err := loadItems(ctx, q, []int64{itemID})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	it := items[0]
	it.Paths = nil // the sidecar's location is the path
	refs := map[string]bool{}
	for _, t := range it.Tags {
		refs[t.Ref] = true
	}
	defs, err := loadTagDefs(ctx, q, refs)
	if err != nil {
		return nil, err
	}
	return &Sidecar{Format: sidecarFormat, Version: formatVersion, Item: it, TagDefs: defs}, nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// normalizeValue turns driver values into JSON-friendly ones.
func normalizeValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(x)
	case string:
		if x == "" {
			return nil
		}
		return x
	case float64:
		if x == float64(int64(x)) {
			return int64(x)
		}
		return x
	default:
		return x
	}
}

func unixTime(sec int64) *time.Time {
	if sec == 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

func prefixed(prefix string, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = prefix + n
	}
	return out
}

func eachRow(ctx context.Context, q db.Execer, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
