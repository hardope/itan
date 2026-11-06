package metadata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hardope/itan/internal/db"
)

// The folder file is one hidden file at a library's root that holds the
// edits for the videos in that folder, so they travel with the drive. It is
// a library export (same format, importable by hand) limited to the edited
// videos in the folder, without watch history. Paths in it are relative to
// the folder, and reading it matches videos within that folder only, so
// the library can have a different name on every machine. Libraries inside
// the folder are covered too: the file is the same with or without them.
//
// It replaces the per-video .meta.json sidecars of earlier versions: those
// are still read for new files, and folded into the folder file (then
// removed) when a library keeps one.
const FolderFileName = ".itan.json"

// One writer at a time: the scanner and edits both write folder files.
var folderMu sync.Mutex

// FolderResult says what SyncFolderFile did.
type FolderResult struct {
	Read     bool // the file had changed since last time and was imported
	Changes  int  // fields and tags the file changed here
	Written  bool // the file was rewritten
	Migrated int  // per-video sidecars folded in and removed
	Damaged  bool // the file couldn't be read and was moved aside
}

type folderLib struct {
	id          int64
	name, root  string
	defaultKind string
	folderTags  bool
	keep        bool // libraries.write_sidecars: keep a folder file
}

func (fl folderLib) path() string { return filepath.Join(fl.root, FolderFileName) }

func loadFolderLib(ctx context.Context, q db.Execer, id int64) (folderLib, error) {
	fl := folderLib{id: id}
	err := q.QueryRowContext(ctx, `SELECT name, root_path, default_kind, folder_tags, write_sidecars FROM libraries WHERE id = ?`,
		id).Scan(&fl.name, &fl.root, &fl.defaultKind, &fl.folderTags, &fl.keep)
	return fl, err
}

// BuildFolderFile returns the folder file for a library: every video in it
// that has edits (fields, added tags, removed tags).
func BuildFolderFile(ctx context.Context, q db.Execer, libraryID int64) (*Library, error) {
	fl, err := loadFolderLib(ctx, q, libraryID)
	if err != nil {
		return nil, err
	}
	return buildFolderFile(ctx, q, fl)
}

func buildFolderFile(ctx context.Context, q db.Execer, fl folderLib) (*Library, error) {
	nested, err := db.NestedLibraries(ctx, q, fl.id)
	if err != nil {
		return nil, err
	}
	type file struct {
		id   int64
		path string
	}
	var files []file
	for _, lib := range append([]db.Nested{{ID: fl.id}}, nested...) {
		err := eachRow(ctx, q, `
			SELECT f.item_id, f.rel_path FROM files f
			 WHERE f.library_id = ? AND f.missing_since IS NULL
			   AND (EXISTS (SELECT 1 FROM item_field_sources s WHERE s.item_id = f.item_id AND s.source = 'user')
			     OR EXISTS (SELECT 1 FROM item_tags t WHERE t.item_id = f.item_id AND (t.source = 'user' OR t.removed = 1)))`,
			[]any{lib.ID}, func(r *sql.Rows) error {
				var f file
				var rel string
				if err := r.Scan(&f.id, &rel); err != nil {
					return err
				}
				f.path = filepath.ToSlash(filepath.Join(lib.Rel, rel))
				files = append(files, f)
				return nil
			})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	paths := map[int64]string{}
	var ids []int64
	for _, f := range files {
		if _, dup := paths[f.id]; !dup {
			ids = append(ids, f.id)
			paths[f.id] = f.path
		}
	}

	items, itemIDs, err := loadItems(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	byID := map[int64]Item{}
	inFile := map[string]bool{}
	refs := map[string]bool{}
	var latest time.Time
	for i, it := range items {
		byID[itemIDs[i]] = it
		inFile[it.Hash] = true
		for _, t := range it.Tags {
			refs[t.Ref] = true
		}
		if it.UpdatedAt != nil && it.UpdatedAt.After(latest) {
			latest = *it.UpdatedAt
		}
	}
	// In path order, so the file reads (and diffs) like the folder.
	ordered := make([]Item, 0, len(ids))
	for _, id := range ids {
		it := byID[id]
		it.Paths = []PathHint{{Library: fl.name, Path: paths[id]}}
		ordered = append(ordered, it)
	}

	defs, err := loadTagDefs(ctx, q, refs)
	if err != nil {
		return nil, err
	}
	cols, err := loadCollections(ctx, q)
	if err != nil {
		return nil, err
	}

	return &Library{
		Format:  libraryFormat,
		Version: formatVersion,
		// The newest edit, not the time of writing: unchanged edits give an
		// identical file, so it isn't rewritten for nothing.
		ExportedAt:  latest.UTC(),
		Generator:   "itan folder file",
		Scope:       Scope{Libraries: []string{fl.name}},
		Libraries:   []LibraryInfo{{Name: fl.name, DefaultKind: fl.defaultKind, FolderTags: fl.folderTags}},
		Tags:        defs,
		Items:       ordered,
		Collections: collectionsFor(cols, inFile),
	}, nil
}

// collectionsFor keeps the collections that contain one of the items (with
// only those items), plus their parents.
func collectionsFor(all []Collection, items map[string]bool) []Collection {
	byUID := map[string]Collection{}
	for _, c := range all {
		byUID[c.UID] = c
	}
	keep := map[string]bool{}
	for _, c := range all {
		for _, ci := range c.Items {
			if items[ci.Hash] {
				for uid := &c.UID; uid != nil && !keep[*uid]; uid = byUID[*uid].Parent {
					keep[*uid] = true
				}
				break
			}
		}
	}
	out := []Collection{}
	for _, c := range all {
		if !keep[c.UID] {
			continue
		}
		members := []CollectionItem{}
		for _, ci := range c.Items {
			if items[ci.Hash] {
				members = append(members, ci)
			}
		}
		c.Items = members
		out = append(out, c)
	}
	return out
}

// WriteFolderFile brings the folder file up to date after edits here: it
// first takes in anything another install changed in the file since this
// one last read it (so their edits aren't overwritten), then writes it if
// it would change. Libraries that don't keep a file are left alone.
func WriteFolderFile(ctx context.Context, database *sql.DB, libraryID int64) (FolderResult, error) {
	return syncFolder(ctx, database, libraryID, false)
}

// SyncFolderFile brings a library and its folder file in line, as the
// scanner does after each scan and the settings page when the option is
// turned on:
//
//  1. If the file changed since this install last read or wrote it
//     (another machine edited it, or the drive was just added here), import
//     it. The usual merge rules apply: the newer edit wins, auto-detected
//     values never replace edits. This happens whether or not the library
//     keeps a file.
//  2. If the library keeps a file: fold in per-video sidecars from earlier
//     versions, write the file, then remove those sidecars.
//
// A file that can't be read is left alone rather than overwritten, unless
// it's damaged (not JSON at all) and the library keeps a file: then it's
// kept aside as .itan.json.damaged and a new one written.
func SyncFolderFile(ctx context.Context, database *sql.DB, libraryID int64) (FolderResult, error) {
	return syncFolder(ctx, database, libraryID, true)
}

func syncFolder(ctx context.Context, database *sql.DB, libraryID int64, withSidecars bool) (FolderResult, error) {
	folderMu.Lock()
	defer folderMu.Unlock()
	var res FolderResult
	fl, err := loadFolderLib(ctx, database, libraryID)
	if err != nil {
		return res, err
	}
	if !fl.keep && !withSidecars {
		return res, nil
	}

	// Changes are detected by content, not modification time: FAT drives
	// only store times to 2 seconds.
	current, err := os.ReadFile(fl.path())
	switch {
	case err == nil:
		if seen, _ := seenFolderFile(ctx, database, fl.id); seen != digest(current) {
			lib, err := ReadLibrary(bytes.NewReader(current))
			if errors.Is(err, ErrUnreadable) && fl.keep {
				// Nothing in it can be read, and the library has every
				// edit: keep it for a look, and write a new one.
				aside := fl.path() + ".damaged"
				if err := os.Rename(fl.path(), aside); err != nil {
					return res, fmt.Errorf("%s: damaged, and can't be moved aside: %w", fl.path(), err)
				}
				log.Printf("%s couldn't be read (%v); kept it as %s and writing a new one", fl.path(), err, filepath.Base(aside))
				res.Damaged = true
				current = nil
				break
			}
			if err != nil {
				return res, fmt.Errorf("%s: %w", fl.path(), err)
			}
			// Paths in the file are relative to this folder, whatever it
			// was called where the file was written.
			for i := range lib.Items {
				for j := range lib.Items[i].Paths {
					lib.Items[i].Paths[j].Library = fl.name
				}
			}
			rep, err := importWith(ctx, database, lib, importOptions{mode: ModeMerge, libraryID: fl.id})
			if err != nil {
				return res, fmt.Errorf("%s: %w", fl.path(), err)
			}
			res.Read = true
			res.Changes = rep.FieldsUpdated + rep.ItemTagsAdded + rep.ItemTagsRemoved
			if err := rememberFolderFile(ctx, database, fl.id, current); err != nil {
				return res, err
			}
		}
	case errors.Is(err, os.ErrNotExist):
		current = nil
	default:
		return res, err
	}

	if !fl.keep {
		return res, nil
	}
	var sidecars []string
	if withSidecars {
		if sidecars, err = foldSidecars(ctx, database, fl); err != nil {
			return res, err
		}
	}

	lib, err := buildFolderFile(ctx, database, fl)
	if err != nil {
		return res, err
	}
	data, err := json.MarshalIndent(lib, "", "  ")
	if err != nil {
		return res, err
	}
	data = append(data, '\n')
	if !bytes.Equal(current, data) {
		if err := writeFileAtomic(fl.path(), data); err != nil {
			return res, fmt.Errorf("write %s: %w", fl.path(), err)
		}
		res.Written = true
		// Our own write isn't news: don't import it next time.
		if err := rememberFolderFile(ctx, database, fl.id, data); err != nil {
			return res, err
		}
	}

	// The folder file now has everything the sidecars had.
	for _, p := range sidecars {
		if err := os.Remove(p); err == nil {
			res.Migrated++
		}
	}
	return res, nil
}

func readFolderFile(path string) (*Library, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	lib, err := ReadLibrary(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return lib, nil
}

// foldSidecars applies the per-video sidecars in a library (merge rules, so
// newer edits in them still count) and returns the ones that applied.
func foldSidecars(ctx context.Context, database *sql.DB, fl folderLib) ([]string, error) {
	type file struct {
		itemID int64
		path   string
	}
	var files []file
	err := eachRow(ctx, database, `SELECT item_id, rel_path FROM files WHERE library_id = ? AND missing_since IS NULL`,
		[]any{fl.id}, func(r *sql.Rows) error {
			var f file
			var rel string
			if err := r.Scan(&f.itemID, &rel); err != nil {
				return err
			}
			f.path = filepath.Join(fl.root, rel) + SidecarExt
			files = append(files, f)
			return nil
		})
	if err != nil {
		return nil, err
	}
	var applied []string
	for _, f := range files {
		ok, err := ApplySidecar(ctx, database, f.itemID, f.path)
		if err != nil {
			log.Printf("%v (kept)", err) // not ours, or damaged: leave it alone
			continue
		}
		if ok {
			applied = append(applied, f.path)
		}
	}
	return applied, nil
}

// digest identifies a version of a folder file. The digest of the version
// this install last read or wrote is kept in the settings table.
func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func folderSettingKey(libraryID int64) string {
	return "folder_file_seen:" + strconv.FormatInt(libraryID, 10)
}

func seenFolderFile(ctx context.Context, q db.Execer, libraryID int64) (string, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, folderSettingKey(libraryID)).Scan(&raw)
	if err != nil {
		return "", err
	}
	var s string
	err = json.Unmarshal([]byte(raw), &s)
	return s, err
}

func rememberFolderFile(ctx context.Context, q db.Execer, libraryID int64, data []byte) error {
	raw, _ := json.Marshal(digest(data))
	_, err := q.ExecContext(ctx, `
		INSERT INTO settings(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, folderSettingKey(libraryID), string(raw))
	return err
}

func idList(ids []int64) string {
	if len(ids) == 0 {
		return "NULL"
	}
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(s, ",")
}
