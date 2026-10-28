package metadata

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/hardope/itan/internal/db"
)

const (
	ModeMerge   = "merge"   // the rules in docs/export-format.md
	ModeReplace = "replace" // incoming wins every conflict
)

// Report says what an import did (or, for a dry run, would do).
type Report struct {
	Mode               string         `json:"mode"`
	DryRun             bool           `json:"dry_run"`
	Items              int            `json:"items"`
	Matched            map[string]int `json:"matched"` // by "hash", "path", "name"
	Pending            int            `json:"pending"` // stored until the file shows up
	FieldsUpdated      int            `json:"fields_updated"`
	FieldsKept         int            `json:"fields_kept"` // local edits kept over different incoming values
	TagsCreated        int            `json:"tags_created"`
	ItemTagsAdded      int            `json:"item_tags_added"`
	ItemTagsRemoved    int            `json:"item_tags_removed"`
	CollectionsCreated int            `json:"collections_created"`
	ProfilesCreated    int            `json:"profiles_created"`
	StatesUpdated      int            `json:"states_updated"`
	Warnings           []string       `json:"warnings"`
}

func (r *Report) warn(format string, args ...any) {
	if len(r.Warnings) < 100 {
		r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
	}
}

// ReadLibrary decodes an export file (plain or gzipped JSON) and checks its
// format and major version.
func ReadLibrary(r io.Reader) (*Library, error) {
	br := bufio.NewReader(r)
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("read gzip: %w", err)
		}
		defer gz.Close()
		r = gz
	} else {
		r = br
	}
	var lib Library
	if err := json.NewDecoder(r).Decode(&lib); err != nil {
		return nil, fmt.Errorf("not a valid export file: %w", err)
	}
	if lib.Format != libraryFormat {
		return nil, fmt.Errorf("not a itan library export (format %q)", lib.Format)
	}
	if err := checkVersion(lib.Version); err != nil {
		return nil, err
	}
	return &lib, nil
}

func checkVersion(v string) error {
	if major, _, _ := strings.Cut(v, "."); major != formatMajor {
		return fmt.Errorf("unsupported format version %q (this build reads %s.x)", v, formatMajor)
	}
	return nil
}

// Import applies an export to the database. With dryRun nothing is written:
// the whole import runs in a transaction that is rolled back.
func Import(ctx context.Context, database *sql.DB, lib *Library, mode string, dryRun bool) (*Report, error) {
	if mode != ModeMerge && mode != ModeReplace {
		return nil, fmt.Errorf("unknown import mode %q", mode)
	}
	rep := &Report{Mode: mode, DryRun: dryRun, Items: len(lib.Items), Matched: map[string]int{}, Warnings: []string{}}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() // no-op after Commit

	im := newImporter(ctx, tx, mode, rep, lib.Tags)
	if err := im.ensureTagDefs(); err != nil {
		return nil, err
	}

	// Collection memberships and watch state per item, so pending items carry them.
	memberships := map[string][]pendingMembership{}
	for _, c := range lib.Collections {
		for _, ci := range c.Items {
			memberships[ci.Hash] = append(memberships[ci.Hash], pendingMembership{UID: c.UID, Position: ci.Position})
		}
	}
	states := map[string][]pendingState{}
	for _, p := range lib.Profiles {
		for _, s := range p.State {
			states[s.Hash] = append(states[s.Hash], pendingState{ProfileUID: p.UID, ProfileName: p.Name, State: s})
		}
	}

	localByHash := map[string]int64{}
	for _, it := range lib.Items {
		id, how, err := im.match(it)
		if err != nil {
			return nil, err
		}
		if id == 0 {
			payload := pendingPayload{Item: it, TagDefs: im.defsFor(it), Collections: memberships[it.Hash], State: states[it.Hash]}
			if err := im.storePending(payload); err != nil {
				return nil, err
			}
			rep.Pending++
			continue
		}
		rep.Matched[how]++
		localByHash[it.Hash] = id
		if err := im.applyItem(id, it); err != nil {
			return nil, fmt.Errorf("item %s: %w", it.Hash, err)
		}
	}

	if err := im.importCollections(lib.Collections, localByHash); err != nil {
		return nil, err
	}
	for _, p := range lib.Profiles {
		profileID, err := im.ensureProfile(p.UID, p.Name)
		if err != nil {
			return nil, err
		}
		for _, s := range p.State {
			if id := localByHash[s.Hash]; id != 0 {
				if err := im.mergeState(profileID, id, s); err != nil {
					return nil, err
				}
			}
		}
	}

	if dryRun {
		return rep, nil // deferred Rollback discards everything
	}
	return rep, tx.Commit()
}

// ApplyPending applies stored metadata to a newly scanned item if any pending
// import matches it (by hash, then path, then file name and size).
func ApplyPending(ctx context.Context, database *sql.DB, itemID int64, hash string, size int64, libName, relPath string) (bool, error) {
	var rowID int64
	var raw string
	err := database.QueryRowContext(ctx, `
		SELECT id, payload FROM pending_imports
		 WHERE content_hash = ?
		    OR (library_name = ? AND rel_path = ?)
		    OR (file_name = ? AND size_bytes = ?)
		 ORDER BY content_hash = ? DESC, (library_name = ? AND rel_path = ?) DESC
		 LIMIT 1`,
		hash, libName, relPath, path.Base(relPath), size, hash, libName, relPath).Scan(&rowID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var p pendingPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return false, fmt.Errorf("pending import %d: %w", rowID, err)
	}
	err = db.Tx(ctx, database, func(tx *sql.Tx) error {
		im := newImporter(ctx, tx, ModeMerge, &Report{}, p.TagDefs)
		if err := im.ensureTagDefs(); err != nil {
			return err
		}
		if err := im.applyItem(itemID, p.Item); err != nil {
			return err
		}
		for _, m := range p.Collections {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO collection_items(collection_id, item_id, position)
				SELECT id, ?, ? FROM collections WHERE uid = ?
				ON CONFLICT DO NOTHING`, itemID, m.Position, m.UID); err != nil {
				return err
			}
		}
		for _, s := range p.State {
			profileID, err := im.ensureProfile(s.ProfileUID, s.ProfileName)
			if err != nil {
				return err
			}
			if err := im.mergeState(profileID, itemID, s.State); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM pending_imports WHERE id = ?`, rowID)
		return err
	})
	return err == nil, err
}

// ApplySidecar applies <video>.meta.json, if present, to an item. Per the
// spec it applies even if the hash inside doesn't match (the file was
// re-encoded): the sidecar sits next to the file.
func ApplySidecar(ctx context.Context, database *sql.DB, itemID int64, sidecarPath string) (bool, error) {
	data, err := os.ReadFile(sidecarPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var sc Sidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return false, fmt.Errorf("%s: %w", sidecarPath, err)
	}
	if sc.Format != sidecarFormat {
		return false, fmt.Errorf("%s: not a itan sidecar (format %q)", sidecarPath, sc.Format)
	}
	if err := checkVersion(sc.Version); err != nil {
		return false, fmt.Errorf("%s: %w", sidecarPath, err)
	}
	err = db.Tx(ctx, database, func(tx *sql.Tx) error {
		im := newImporter(ctx, tx, ModeMerge, &Report{}, sc.TagDefs)
		if err := im.ensureTagDefs(); err != nil {
			return err
		}
		return im.applyItem(itemID, sc.Item)
	})
	return err == nil, err
}

// ---- importer ----

type importer struct {
	ctx    context.Context
	tx     *sql.Tx
	mode   string
	rep    *Report
	defs   map[string]TagDef
	order  []string
	tagIDs map[string]int64
}

func newImporter(ctx context.Context, tx *sql.Tx, mode string, rep *Report, defs []TagDef) *importer {
	im := &importer{ctx: ctx, tx: tx, mode: mode, rep: rep, defs: map[string]TagDef{}, tagIDs: map[string]int64{}}
	for _, d := range defs {
		im.defs[d.Ref] = d
		im.order = append(im.order, d.Ref)
	}
	return im
}

func (im *importer) replace() bool { return im.mode == ModeReplace }

// ensureTagDefs creates every defined tag (exports carry the full
// vocabulary, including unused tags), then links parents.
func (im *importer) ensureTagDefs() error {
	for _, ref := range im.order {
		if _, err := im.tagID(ref); err != nil {
			return err
		}
	}
	for _, ref := range im.order {
		d := im.defs[ref]
		if d.Parent == nil {
			continue
		}
		id, err := im.tagID(ref)
		if err != nil || id == 0 {
			return err
		}
		parentID, err := im.tagID(*d.Parent)
		if err != nil || parentID == 0 {
			return err
		}
		cond := " AND parent_id IS NULL"
		if im.replace() {
			cond = ""
		}
		if _, err := im.tx.ExecContext(im.ctx, `UPDATE tags SET parent_id = ? WHERE id = ? AND id != ?`+cond, parentID, id, parentID); err != nil {
			return err
		}
	}
	return nil
}

// tagID returns the local id for a ref, creating the tag from its
// definition (or from the ref alone) if needed. 0 means an invalid ref.
func (im *importer) tagID(ref string) (int64, error) {
	if id, ok := im.tagIDs[ref]; ok {
		return id, nil
	}
	ns, slug, ok := strings.Cut(ref, ":")
	if !ok || slug == "" || db.Slugify(slug) != slug {
		im.rep.warn("skipped invalid tag ref %q", ref)
		im.tagIDs[ref] = 0
		return 0, nil
	}
	if parsedNS, _, _ := db.ParseTagRef(ref); parsedNS != ns {
		im.rep.warn("skipped tag %q: unknown namespace %q", ref, ns)
		im.tagIDs[ref] = 0
		return 0, nil
	}

	var id int64
	err := im.tx.QueryRowContext(im.ctx, `SELECT id FROM tags WHERE namespace = ? AND slug = ?`, ns, slug).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		d, hasDef := im.defs[ref]
		if !hasDef || strings.TrimSpace(d.Name) == "" {
			if id, err = db.EnsureTag(im.ctx, im.tx, ref); err != nil {
				return 0, err
			}
		} else if err = im.tx.QueryRowContext(im.ctx,
			`INSERT INTO tags(namespace, slug, name, color) VALUES (?, ?, ?, ?) RETURNING id`,
			ns, slug, d.Name, d.Color).Scan(&id); err != nil {
			return 0, err
		}
		im.rep.TagsCreated++
	case err != nil:
		return 0, err
	default:
		if d, hasDef := im.defs[ref]; hasDef {
			// Fill in a missing color; replace mode also takes the name.
			if d.Color != nil {
				if _, err := im.tx.ExecContext(im.ctx, `UPDATE tags SET color = ? WHERE id = ? AND (color IS NULL OR ?)`,
					*d.Color, id, im.replace()); err != nil {
					return 0, err
				}
			}
			if im.replace() && strings.TrimSpace(d.Name) != "" {
				if _, err := im.tx.ExecContext(im.ctx, `UPDATE tags SET name = ? WHERE id = ?`, d.Name, id); err != nil {
					return 0, err
				}
			}
		}
	}
	im.tagIDs[ref] = id
	return id, nil
}

// defsFor returns the tag definitions an item uses, plus their ancestors.
func (im *importer) defsFor(it Item) []TagDef {
	seen := map[string]bool{}
	var out []TagDef
	var add func(ref string)
	add = func(ref string) {
		d, ok := im.defs[ref]
		if !ok || seen[ref] {
			return
		}
		seen[ref] = true
		out = append(out, d)
		if d.Parent != nil {
			add(*d.Parent)
		}
	}
	for _, t := range it.Tags {
		add(t.Ref)
	}
	return out
}

// match finds the local item for an exported one: by hash and size, then by
// library name and path, then by file name and size (if unambiguous).
func (im *importer) match(it Item) (int64, string, error) {
	var id int64
	err := im.tx.QueryRowContext(im.ctx,
		`SELECT id FROM items WHERE content_hash = ? AND size_bytes = ?`, it.Hash, it.Size).Scan(&id)
	if err == nil {
		return id, "hash", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", err
	}
	for _, p := range it.Paths {
		err := im.tx.QueryRowContext(im.ctx, `
			SELECT f.item_id FROM files f JOIN libraries l ON l.id = f.library_id
			 WHERE l.name = ? AND f.rel_path = ? LIMIT 1`, p.Library, p.Path).Scan(&id)
		if err == nil {
			return id, "path", nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, "", err
		}
	}
	for _, p := range it.Paths {
		name := path.Base(p.Path)
		var ids []int64
		err := eachRow(im.ctx, im.tx, `
			SELECT DISTINCT item_id FROM files
			 WHERE size_bytes = ? AND (rel_path = ? OR rel_path LIKE ? ESCAPE '\')`,
			[]any{it.Size, name, "%/" + escapeLike(name)}, func(r *sql.Rows) error {
				var id int64
				if err := r.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
				return nil
			})
		if err != nil {
			return 0, "", err
		}
		if len(ids) == 1 {
			return ids[0], "name", nil
		}
	}
	return 0, "", nil
}

func (im *importer) storePending(p pendingPayload) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	var lib, rel, name any
	if len(p.Item.Paths) > 0 {
		lib, rel, name = p.Item.Paths[0].Library, p.Item.Paths[0].Path, path.Base(p.Item.Paths[0].Path)
	}
	_, err = im.tx.ExecContext(im.ctx, `
		INSERT INTO pending_imports(content_hash, size_bytes, library_name, rel_path, file_name, payload)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(content_hash) DO UPDATE SET
			size_bytes = excluded.size_bytes, library_name = excluded.library_name,
			rel_path = excluded.rel_path, file_name = excluded.file_name,
			payload = excluded.payload, created_at = unixepoch()`,
		p.Item.Hash, p.Item.Size, lib, rel, name, string(data))
	return err
}

// applyItem merges one exported item into local item id.
func (im *importer) applyItem(id int64, it Item) error {
	changed := false
	updatedAt := time.Now().Unix()
	if it.UpdatedAt != nil {
		updatedAt = it.UpdatedAt.Unix()
	}

	for _, name := range exportFields {
		f, ok := it.Fields[name]
		if !ok || f.Value == nil {
			continue
		}
		value := normalizeValue(f.Value)
		switch value.(type) {
		case string, int64, float64:
		default:
			im.rep.warn("%s: field %s has an unsupported value, skipped", it.Hash, name)
			continue
		}
		if sourcePriority[f.Source] == 0 {
			im.rep.warn("%s: field %s has unknown source %q, skipped", it.Hash, name, f.Source)
			continue
		}
		if s, isStr := value.(string); name == "title" && (!isStr || strings.TrimSpace(s) == "") {
			continue
		}

		var local any
		// name comes from exportFields, safe to interpolate.
		if err := im.tx.QueryRowContext(im.ctx, `SELECT `+name+` FROM items WHERE id = ?`, id).Scan(&local); err != nil {
			return err
		}
		localSource, localUpdated := "filename", int64(0)
		err := im.tx.QueryRowContext(im.ctx,
			`SELECT source, updated_at FROM item_field_sources WHERE item_id = ? AND field = ?`,
			id, name).Scan(&localSource, &localUpdated)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		same := fmt.Sprint(normalizeValue(local)) == fmt.Sprint(value)

		var take bool
		switch {
		case im.replace():
			take = true
		case localSource != "user":
			take = sourcePriority[f.Source] >= sourcePriority[localSource]
		case f.Source == "user":
			take = updatedAt > localUpdated
		}
		if !take {
			if !same {
				im.rep.FieldsKept++
			}
			continue
		}
		if same && localSource == f.Source {
			continue
		}
		if _, err := im.tx.ExecContext(im.ctx, `UPDATE items SET `+name+` = ? WHERE id = ?`, value, id); err != nil {
			if strings.Contains(err.Error(), "constraint") {
				im.rep.warn("%s: field %s value %v not allowed, skipped", it.Hash, name, value)
				continue
			}
			return err
		}
		if _, err := im.tx.ExecContext(im.ctx, `
			INSERT INTO item_field_sources(item_id, field, source, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(item_id, field) DO UPDATE SET source = excluded.source, updated_at = excluded.updated_at`,
			id, name, f.Source, updatedAt); err != nil {
			return err
		}
		if !same {
			im.rep.FieldsUpdated++
		}
		changed = true
	}

	for _, t := range it.Tags {
		c, err := im.applyTag(id, t)
		if err != nil {
			return err
		}
		changed = changed || c
	}

	if len(it.ExternalIDs) > 0 {
		c, err := im.mergeExternalIDs(id, it.ExternalIDs)
		if err != nil {
			return err
		}
		changed = changed || c
	}

	if !changed {
		return nil
	}
	if _, err := im.tx.ExecContext(im.ctx, `UPDATE items SET updated_at = unixepoch() WHERE id = ?`, id); err != nil {
		return err
	}
	return db.ReindexItem(im.ctx, im.tx, id)
}

// applyTag merges one tag. Tags merge as a union; a tombstone beats an
// auto-detected tag; a user-added tag beats a tombstone.
func (im *importer) applyTag(itemID int64, t ItemTag) (bool, error) {
	tagID, err := im.tagID(t.Ref)
	if err != nil || tagID == 0 {
		return false, err
	}
	src := t.Source
	if sourcePriority[src] == 0 {
		src = "user"
	}

	var localSource string
	var localRemoved bool
	err = im.tx.QueryRowContext(im.ctx, `SELECT source, removed FROM item_tags WHERE item_id = ? AND tag_id = ?`,
		itemID, tagID).Scan(&localSource, &localRemoved)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := im.tx.ExecContext(im.ctx,
			`INSERT INTO item_tags(item_id, tag_id, source, removed) VALUES (?, ?, ?, ?)`,
			itemID, tagID, src, t.Removed); err != nil {
			return false, err
		}
		if !t.Removed {
			im.rep.ItemTagsAdded++
		}
		return true, nil // a new tombstone matters too: it blocks the tag on rescans
	}
	if err != nil {
		return false, err
	}

	set := func(removed bool) (bool, error) {
		_, err := im.tx.ExecContext(im.ctx, `UPDATE item_tags SET removed = ?, source = ? WHERE item_id = ? AND tag_id = ?`,
			removed, src, itemID, tagID)
		return err == nil, err
	}
	switch {
	case t.Removed && !localRemoved && (im.replace() || localSource != "user"):
		im.rep.ItemTagsRemoved++
		return set(true)
	case !t.Removed && localRemoved && (im.replace() || src == "user"):
		im.rep.ItemTagsAdded++
		return set(false)
	case !t.Removed && !localRemoved && src == "user" && localSource != "user":
		return set(false) // same tag, stronger provenance
	}
	return false, nil
}

func (im *importer) mergeExternalIDs(itemID int64, incoming map[string]string) (bool, error) {
	var raw sql.NullString
	if err := im.tx.QueryRowContext(im.ctx, `SELECT external_ids FROM items WHERE id = ?`, itemID).Scan(&raw); err != nil {
		return false, err
	}
	local := map[string]string{}
	if raw.Valid && raw.String != "" {
		json.Unmarshal([]byte(raw.String), &local)
	}
	changed := false
	for k, v := range incoming {
		if old, ok := local[k]; (!ok || im.replace()) && old != v {
			local[k] = v
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	data, _ := json.Marshal(local)
	_, err := im.tx.ExecContext(im.ctx, `UPDATE items SET external_ids = ? WHERE id = ?`, string(data), itemID)
	return err == nil, err
}

func (im *importer) importCollections(cols []Collection, localByHash map[string]int64) error {
	ids := map[string]int64{}
	for _, c := range cols {
		var id int64
		err := im.tx.QueryRowContext(im.ctx, `SELECT id FROM collections WHERE uid = ?`, c.UID).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			err = im.tx.QueryRowContext(im.ctx, `
				INSERT INTO collections(uid, kind, title, description, position) VALUES (?, ?, ?, ?, ?)
				RETURNING id`, c.UID, c.Kind, c.Title, c.Description, c.Position).Scan(&id)
			if err != nil {
				if strings.Contains(err.Error(), "constraint") {
					im.rep.warn("collection %q: invalid (kind %q), skipped", c.Title, c.Kind)
					continue
				}
				return err
			}
			im.rep.CollectionsCreated++
		case err != nil:
			return err
		case im.replace():
			if _, err := im.tx.ExecContext(im.ctx, `
				UPDATE collections SET title = ?, description = ?, position = ?, updated_at = unixepoch()
				 WHERE id = ?`, c.Title, c.Description, c.Position, id); err != nil {
				return err
			}
		}
		ids[c.UID] = id
	}
	for _, c := range cols {
		id := ids[c.UID]
		if id == 0 {
			continue
		}
		if c.Parent != nil && ids[*c.Parent] != 0 {
			cond := " AND parent_id IS NULL"
			if im.replace() {
				cond = ""
			}
			if _, err := im.tx.ExecContext(im.ctx, `UPDATE collections SET parent_id = ? WHERE id = ?`+cond,
				ids[*c.Parent], id); err != nil {
				return err
			}
		}
		for _, ci := range c.Items {
			itemID := localByHash[ci.Hash]
			if itemID == 0 {
				continue // pending items carry their memberships
			}
			if _, err := im.tx.ExecContext(im.ctx, `
				INSERT INTO collection_items(collection_id, item_id, position) VALUES (?, ?, ?)
				ON CONFLICT(collection_id, item_id) DO UPDATE SET position = excluded.position`,
				id, itemID, ci.Position); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureProfile finds a profile by uid, else by name, else creates it.
func (im *importer) ensureProfile(uid, name string) (int64, error) {
	var id int64
	err := im.tx.QueryRowContext(im.ctx, `SELECT id FROM profiles WHERE uid = ?`, uid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = im.tx.QueryRowContext(im.ctx, `SELECT id FROM profiles WHERE name = ?`, name).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = im.tx.QueryRowContext(im.ctx,
			`INSERT INTO profiles(uid, name) VALUES (?, ?) RETURNING id`, uid, name).Scan(&id)
		if err == nil {
			im.rep.ProfilesCreated++
		}
	}
	return id, err
}

// mergeState combines watch state: watched/favorite stick, play counts take
// the larger, and the resume point comes from whichever was played last.
func (im *importer) mergeState(profileID, itemID int64, s PlayState) error {
	var lastPlayed any
	if s.LastPlayedAt != nil {
		lastPlayed = s.LastPlayedAt.Unix()
	}
	newer := "COALESCE(excluded.last_played_at, 0) > COALESCE(play_state.last_played_at, 0)"
	if im.replace() {
		newer = "1"
	}
	_, err := im.tx.ExecContext(im.ctx, `
		INSERT INTO play_state(profile_id, item_id, position_ms, completed, play_count, favorite, last_played_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(profile_id, item_id) DO UPDATE SET
			position_ms    = CASE WHEN `+newer+` THEN excluded.position_ms ELSE play_state.position_ms END,
			last_played_at = CASE WHEN `+newer+` THEN excluded.last_played_at ELSE play_state.last_played_at END,
			completed      = play_state.completed OR excluded.completed,
			favorite       = play_state.favorite OR excluded.favorite,
			play_count     = MAX(play_state.play_count, excluded.play_count)`,
		profileID, itemID, s.PositionMS, s.Completed, s.PlayCount, s.Favorite, lastPlayed)
	if err == nil {
		im.rep.StatesUpdated++
	}
	return err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
