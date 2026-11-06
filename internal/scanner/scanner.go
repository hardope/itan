// Package scanner walks library folders, identifies video files by content
// hash, and records what it can auto-detect about them.
package scanner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hardope/itan/internal/db"
	"github.com/hardope/itan/internal/metadata"
)

var videoExts = map[string]bool{
	".mp4": true, ".m4v": true, ".mkv": true, ".webm": true, ".mov": true,
	".avi": true, ".wmv": true, ".flv": true, ".ts": true, ".m2ts": true,
	".mpg": true, ".mpeg": true, ".3gp": true, ".ogv": true,
}

var ErrRunning = errors.New("scan already running")

type Status struct {
	Running    bool      `json:"running"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Current    string    `json:"current,omitempty"`
	Seen       int       `json:"seen"`
	Added      int       `json:"added"`
	Missing    int       `json:"missing"`
	Errors     []string  `json:"errors"`
}

type Scanner struct {
	db      *sql.DB
	ffprobe string

	mu     sync.Mutex
	status Status
	again  bool // a scan was asked for during this one: go round again

	onLibrary func(name string) // tests: called as each library's scan starts
}

func New(database *sql.DB, ffprobe string) *Scanner {
	return &Scanner{db: database, ffprobe: ffprobe, status: Status{Errors: []string{}}}
}

func (s *Scanner) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	st.Errors = append([]string(nil), s.status.Errors...)
	return st
}

func (s *Scanner) update(fn func(*Status)) {
	s.mu.Lock()
	fn(&s.status)
	s.mu.Unlock()
}

func (s *Scanner) addError(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Print("scan: ", msg)
	s.update(func(st *Status) {
		if len(st.Errors) < 50 {
			st.Errors = append(st.Errors, msg)
		}
	})
}

type library struct {
	id          int64
	name, root  string
	defaultKind string
	folderTags  bool
}

// Scan scans every enabled library, those inside other libraries first.
// Only one scan runs at a time.
// Scan scans every library. While one scan runs, another returns
// ErrRunning, and the running one goes round again when it's done, so
// what changed meanwhile (a library added, say) isn't left for later.
func (s *Scanner) Scan(ctx context.Context) error {
	s.mu.Lock()
	if s.status.Running {
		s.again = true
		s.mu.Unlock()
		return ErrRunning
	}
	for {
		s.status = Status{Running: true, StartedAt: time.Now(), Errors: []string{}}
		s.again = false
		s.mu.Unlock()
		err := s.scan(ctx)
		s.mu.Lock()
		if !s.again || err != nil || ctx.Err() != nil {
			s.status.Running, s.status.Current, s.status.FinishedAt = false, "", time.Now()
			s.again = false
			s.mu.Unlock()
			return err
		}
	}
}

func (s *Scanner) scan(ctx context.Context) error {
	libs, err := s.libraries(ctx)
	if err != nil {
		return err
	}
	for _, lib := range libs {
		if s.onLibrary != nil {
			s.onLibrary(lib.name)
		}
		if err := s.scanLibrary(ctx, lib); err != nil {
			s.addError("library %q: %v", lib.name, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if n, err := s.retitle(ctx); err != nil {
		s.addError("%v", err)
	} else if n > 0 {
		log.Printf("scan: retitled %d videos from their file names", n)
	}
	if n, err := s.forgetMoved(ctx); err != nil {
		s.addError("%v", err)
	} else if n > 0 {
		log.Printf("scan: forgot %d old places of moved or renamed files", n)
	}
	s.retryProbes(ctx)
	st := s.Status()
	log.Printf("scan: done in %s: %d files, %d new, %d missing, %d errors",
		time.Since(st.StartedAt).Round(time.Millisecond), st.Seen, st.Added, st.Missing, len(st.Errors))
	return nil
}

// retitle keeps titles and years that came from file names in step with
// the names, after a file is renamed or when reading names improves.
// Titles and years someone set, or that came from the file itself, stay.
func (s *Scanner) retitle(ctx context.Context) (int, error) {
	type named struct {
		id      int64
		title   string
		year    sql.NullInt64
		yearSrc sql.NullString
		rel     string
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.title, i.year, ys.source,
		       (SELECT f.rel_path FROM files f WHERE f.item_id = i.id AND f.missing_since IS NULL ORDER BY f.id LIMIT 1)
		  FROM items i
		  JOIN item_field_sources ts ON ts.item_id = i.id AND ts.field = 'title' AND ts.source = 'filename'
		  LEFT JOIN item_field_sources ys ON ys.item_id = i.id AND ys.field = 'year'`)
	if err != nil {
		return 0, err
	}
	var items []named
	for rows.Next() {
		var n named
		var rel sql.NullString
		if err := rows.Scan(&n.id, &n.title, &n.year, &n.yearSrc, &rel); err != nil {
			rows.Close()
			return 0, err
		}
		if rel.Valid {
			n.rel = rel.String
			items = append(items, n)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	changed := 0
	for _, n := range items {
		title, year, _ := nameTitle(n.rel)
		setTitle := title != n.title
		setYear := year > 0 && int64(year) != n.year.Int64 && (!n.yearSrc.Valid || n.yearSrc.String == "filename")
		if !setTitle && !setYear {
			continue
		}
		err := db.Tx(ctx, s.db, func(tx *sql.Tx) error {
			if setTitle {
				if _, err := tx.ExecContext(ctx, `UPDATE items SET title = ? WHERE id = ?`, title, n.id); err != nil {
					return err
				}
			}
			if setYear {
				if _, err := tx.ExecContext(ctx, `UPDATE items SET year = ? WHERE id = ?`, year, n.id); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO item_field_sources(item_id, field, source) VALUES (?, 'year', 'filename')
					ON CONFLICT(item_id, field) DO NOTHING`, n.id); err != nil {
					return err
				}
			}
			return db.ReindexItem(ctx, tx, n.id)
		})
		if err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

func (s *Scanner) libraries(ctx context.Context) ([]library, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, root_path, default_kind, folder_tags FROM libraries WHERE enabled = 1
		  ORDER BY length(root_path) DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var libs []library
	for rows.Next() {
		var l library
		if err := rows.Scan(&l.id, &l.name, &l.root, &l.defaultKind, &l.folderTags); err != nil {
			return nil, err
		}
		libs = append(libs, l)
	}
	return libs, rows.Err()
}

func (s *Scanner) scanLibrary(ctx context.Context, lib library) error {
	// An unmounted drive must not mark every file missing.
	if st, err := os.Stat(lib.root); err != nil || !st.IsDir() {
		return fmt.Errorf("root %s is not reachable: %v", lib.root, err)
	}
	started := time.Now().Unix()

	// Folders that are libraries of their own are scanned there.
	nested, err := db.NestedLibraries(ctx, s.db, lib.id)
	if err != nil {
		return err
	}
	ownLibrary := func(path string) bool {
		for _, n := range nested {
			if db.SamePath(path, filepath.Join(lib.root, n.Rel)) {
				return true
			}
		}
		return false
	}

	err = filepath.WalkDir(lib.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			s.addError("%s: %v", path, err)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.HasPrefix(d.Name(), ".") && path != lib.root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && path != lib.root && ownLibrary(path) {
			return filepath.SkipDir
		}
		if d.IsDir() || !videoExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		rel, _ := filepath.Rel(lib.root, path)
		s.update(func(st *Status) { st.Current, st.Seen = rel, st.Seen+1 })
		if err := s.scanFile(ctx, lib, path, rel, d); err != nil {
			s.addError("%s: %v", rel, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if n, err := s.handOver(ctx, lib, nested); err != nil {
		return err
	} else if n > 0 {
		log.Printf("scan: %s: %d files are now in libraries of their own", lib.name, n)
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE files SET missing_since = ?
		 WHERE library_id = ? AND last_seen_at < ? AND missing_since IS NULL`,
		started, lib.id, started)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	s.update(func(st *Status) { st.Missing += int(n) })

	if n, err := s.groupEpisodes(ctx, lib); err != nil {
		s.addError("grouping episodes: %v", err)
	} else if n > 0 {
		log.Printf("scan: %s: grouped %d episodes into series", lib.name, n)
	}

	// Read the folder's .itan.json if it changed (edits from another
	// machine), and keep it up to date if the library keeps one.
	if fr, err := metadata.SyncFolderFile(ctx, s.db, lib.id); err != nil {
		s.addError("%v", err)
	} else {
		if fr.Read && fr.Changes > 0 {
			log.Printf("scan: %s: applied %d changes from %s", lib.name, fr.Changes, metadata.FolderFileName)
		}
		if fr.Migrated > 0 {
			log.Printf("scan: %s: moved %d .meta.json files into %s", lib.name, fr.Migrated, metadata.FolderFileName)
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE libraries SET last_scan_at = ? WHERE id = ?`, time.Now().Unix(), lib.id)
	return err
}

// forgetMoved drops the old places of files that were moved or renamed:
// missing files whose video is still there somewhere else.
func (s *Scanner) forgetMoved(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM files WHERE missing_since IS NOT NULL
		   AND EXISTS (SELECT 1 FROM files o WHERE o.item_id = files.item_id AND o.missing_since IS NULL)`)
	if err != nil {
		return 0, fmt.Errorf("forget moved files: %w", err)
	}
	return res.RowsAffected()
}

// handOver forgets a library's files that are inside libraries of its own:
// those libraries have them (they're scanned first), under the same items.
func (s *Scanner) handOver(ctx context.Context, lib library, nested []db.Nested) (int, error) {
	if len(nested) == 0 {
		return 0, nil
	}
	type moved struct {
		file, item int64
		folder     string // the inner library's folder, relative to lib's
	}
	var files []moved
	rows, err := s.db.QueryContext(ctx, `SELECT id, item_id, rel_path FROM files WHERE library_id = ?`, lib.id)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var m moved
		var rel string
		if err := rows.Scan(&m.file, &m.item, &rel); err != nil {
			rows.Close()
			return 0, err
		}
		for _, n := range nested {
			if _, ok := db.Within(n.Rel, rel); ok {
				m.folder = n.Rel
				files = append(files, m)
				break
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(files) == 0 {
		return 0, err
	}
	err = db.Tx(ctx, s.db, func(tx *sql.Tx) error {
		for _, m := range files {
			if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE id = ?`, m.file); err != nil {
				return err
			}
			// Tags lib gave the video for the folder's name ("Music") go:
			// folder tags come from folders inside the library a video is in.
			for _, t := range FolderTags(filepath.Join(m.folder, "video")) {
				ns, slug, _ := db.ParseTagRef(t)
				if _, err := tx.ExecContext(ctx, `
					DELETE FROM item_tags WHERE item_id = ? AND source = 'folder' AND removed = 0
					   AND tag_id = (SELECT id FROM tags WHERE namespace = ? AND slug = ?)`, m.item, ns, slug); err != nil {
					return err
				}
			}
			if err := db.ReindexItem(ctx, tx, m.item); err != nil {
				return err
			}
		}
		return nil
	})
	return len(files), err
}

func (s *Scanner) scanFile(ctx context.Context, lib library, path, rel string, d fs.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return err
	}
	now := time.Now().Unix()

	// Unchanged since last scan: just mark it seen, skip hashing.
	var fileID, oldSize, oldMtime int64
	err = s.db.QueryRowContext(ctx,
		`SELECT id, size_bytes, mtime FROM files WHERE library_id = ? AND rel_path = ?`,
		lib.id, rel).Scan(&fileID, &oldSize, &oldMtime)
	if err == nil && oldSize == info.Size() && oldMtime == info.ModTime().Unix() {
		_, err = s.db.ExecContext(ctx,
			`UPDATE files SET last_seen_at = ?, missing_since = NULL WHERE id = ?`, now, fileID)
		return err
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	hash, size, err := ContentHash(path)
	if err != nil {
		return fmt.Errorf("hash: %w", err)
	}

	var itemID int64
	isNew := false
	err = s.db.QueryRowContext(ctx, `SELECT id FROM items WHERE content_hash = ?`, hash).Scan(&itemID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// New content. Probe outside the transaction: it can take a while.
		probe, perr := RunProbe(ctx, s.ffprobe, path)
		if itemID, err = s.createItem(ctx, lib, rel, hash, size, probe, perr); err != nil {
			return err
		}
		isNew = true
		s.update(func(st *Status) { st.Added++ })
	case err != nil:
		return err
	}

	err = db.Tx(ctx, s.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO files(item_id, library_id, rel_path, size_bytes, mtime, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(library_id, rel_path) DO UPDATE SET
				item_id = excluded.item_id, size_bytes = excluded.size_bytes,
				mtime = excluded.mtime, last_seen_at = excluded.last_seen_at,
				missing_since = NULL`,
			itemID, lib.id, rel, size, info.ModTime().Unix(), now)
		if err != nil {
			return err
		}
		if lib.folderTags {
			for _, t := range FolderTags(rel) {
				tagID, err := db.EnsureTag(ctx, tx, t)
				if err != nil || tagID == 0 {
					return err
				}
				// OR IGNORE: never resurrect a tag the user removed (removed = 1).
				if _, err := tx.ExecContext(ctx,
					`INSERT OR IGNORE INTO item_tags(item_id, tag_id, source) VALUES (?, ?, 'folder')`,
					itemID, tagID); err != nil {
					return err
				}
			}
		}
		return db.ReindexItem(ctx, tx, itemID)
	})
	if err != nil || !isNew {
		return err
	}
	s.applyImported(ctx, lib, itemID, hash, size, path, rel)
	return nil
}

// applyImported brings in metadata that was made elsewhere for a newly
// found file: its sidecar (<video>.meta.json), then any pending import.
func (s *Scanner) applyImported(ctx context.Context, lib library, itemID int64, hash string, size int64, path, rel string) {
	if ok, err := metadata.ApplySidecar(ctx, s.db, itemID, path+metadata.SidecarExt); err != nil {
		s.addError("%s: sidecar: %v", rel, err)
	} else if ok {
		log.Printf("scan: applied sidecar for %s", rel)
	}
	if ok, err := metadata.ApplyPending(ctx, s.db, itemID, hash, size, lib.name, filepath.ToSlash(rel)); err != nil {
		s.addError("%s: pending import: %v", rel, err)
	} else if ok {
		log.Printf("scan: applied imported metadata for %s", rel)
	}
}

// nameTitle is the title and year a file's path gives it, and whether it's
// an episode.
func nameTitle(rel string) (title string, year int, episode bool) {
	parsed := ParseFilename(rel)
	if ep, ok := ParseEpisode(rel); ok {
		// Shown under the series name, so the episode's own title is enough.
		title = ep.Title
		if title == "" {
			title = fmt.Sprintf("Episode %d", ep.Number)
		}
		return title, parsed.Year, true
	}
	return parsed.Title, parsed.Year, false
}

func (s *Scanner) createItem(ctx context.Context, lib library, rel, hash string, size int64, probe *Probe, probeErr error) (int64, error) {
	title, year, episode := nameTitle(rel)
	titleSrc := "filename"
	kind, kindSrc := lib.defaultKind, "folder"
	if episode {
		kind, kindSrc = "episode", "filename"
	}
	if probe != nil && usableEmbeddedTitle(probe.Title) {
		title, titleSrc = probe.Title, "embedded"
	}

	var itemID int64
	err := db.Tx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO items(content_hash, size_bytes, title, year, kind) VALUES (?, ?, ?, ?, ?)`,
			hash, size, title, nullInt(year), kind)
		if err != nil {
			return err
		}
		if itemID, err = res.LastInsertId(); err != nil {
			return err
		}

		sources := map[string]string{"title": titleSrc, "kind": kindSrc}
		if year > 0 {
			sources["year"] = "filename"
		}
		for field, src := range sources {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO item_field_sources(item_id, field, source) VALUES (?, ?, ?)`,
				itemID, field, src); err != nil {
				return err
			}
		}
		return applyProbe(ctx, tx, itemID, probe, probeErr)
	})
	return itemID, err
}

// applyProbe stores technical metadata. It never touches user-editable fields.
func applyProbe(ctx context.Context, tx *sql.Tx, itemID int64, p *Probe, probeErr error) error {
	if probeErr != nil {
		_, err := tx.ExecContext(ctx, `UPDATE items SET probe_error = ? WHERE id = ?`, probeErr.Error(), itemID)
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE items SET container = ?, duration_ms = ?, bitrate = ?, width = ?, height = ?,
		       video_codec = ?, video_profile = ?, frame_rate = ?, hdr = ?,
		       quality_label = ?, playback_mode = ?, probed_at = ?, probe_error = NULL
		 WHERE id = ?`,
		p.Container, nullInt64(p.DurationMS), nullInt64(p.Bitrate), nullInt(p.Width), nullInt(p.Height),
		nullStr(p.VideoCodec), nullStr(p.VideoProfile), p.FrameRate, nullStr(p.HDR),
		nullStr(QualityLabel(p.Width, p.Height)), PlaybackMode(p), time.Now().Unix(), itemID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM streams WHERE item_id = ? AND external_path IS NULL`, itemID); err != nil {
		return err
	}
	for _, st := range p.Streams {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO streams(item_id, stream_index, type, codec, language, title, channels, is_default, is_forced)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			itemID, st.Index, st.Type, st.Codec, nullStr(st.Language), nullStr(st.Title),
			nullInt(st.Channels), st.IsDefault, st.IsForced); err != nil {
			return err
		}
	}
	return nil
}

// retryProbes re-probes items whose earlier probe failed (for example
// ffprobe wasn't installed yet).
func (s *Scanner) retryProbes(ctx context.Context) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, l.root_path, f.rel_path FROM items i
		  JOIN files f ON f.item_id = i.id AND f.missing_since IS NULL
		  JOIN libraries l ON l.id = f.library_id
		 WHERE i.probed_at IS NULL
		 GROUP BY i.id`)
	if err != nil {
		s.addError("retry probes: %v", err)
		return
	}
	type job struct {
		id   int64
		path string
	}
	var jobs []job
	for rows.Next() {
		var j job
		var root, rel string
		if rows.Scan(&j.id, &root, &rel) == nil {
			j.path = filepath.Join(root, rel)
			jobs = append(jobs, j)
		}
	}
	rows.Close()

	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		probe, perr := RunProbe(ctx, s.ffprobe, j.path)
		err := db.Tx(ctx, s.db, func(tx *sql.Tx) error { return applyProbe(ctx, tx, j.id, probe, perr) })
		if err != nil {
			s.addError("probe %s: %v", j.path, err)
		}
		if errors.Is(perr, exec.ErrNotFound) {
			return // ffprobe missing: no point trying the rest
		}
	}
}

func usableEmbeddedTitle(t string) bool {
	l := strings.ToLower(t)
	return len(t) >= 2 && len(t) <= 200 &&
		!strings.Contains(l, "www.") && !strings.Contains(l, ".com") && !strings.Contains(l, ".org") &&
		!videoExts[filepath.Ext(l)]
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// groupEpisodes puts videos whose paths look like episodes (see
// ParseEpisode) into series and seasons. Videos already in a season, and
// videos whose series someone set or removed by hand, are left alone.
func (s *Scanner) groupEpisodes(ctx context.Context, lib library) (int, error) {
	type file struct {
		itemID int64
		rel    string
	}
	var files []file
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.item_id, f.rel_path FROM files f
		 WHERE f.library_id = ? AND f.missing_since IS NULL
		   AND NOT EXISTS (SELECT 1 FROM collection_items ci JOIN collections c ON c.id = ci.collection_id
		                    WHERE ci.item_id = f.item_id AND c.kind = 'season')
		   AND NOT EXISTS (SELECT 1 FROM item_field_sources fs
		                    WHERE fs.item_id = f.item_id AND fs.field = 'series' AND fs.source = 'user')`, lib.id)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var f file
		if err := rows.Scan(&f.itemID, &f.rel); err != nil {
			rows.Close()
			return 0, err
		}
		files = append(files, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	grouped := 0
	for _, f := range files {
		ep, ok := ParseEpisode(f.rel)
		if !ok {
			continue
		}
		err := db.Tx(ctx, s.db, func(tx *sql.Tx) error {
			seriesID, err := db.EnsureSeries(ctx, tx, ep.Show)
			if err != nil {
				return err
			}
			seasonID, err := db.EnsureSeason(ctx, tx, seriesID, ep.Season)
			if err != nil {
				return err
			}
			if err := db.SetEpisode(ctx, tx, f.itemID, seasonID, ep.Number); err != nil {
				return err
			}
			// Only if nobody chose a kind for it.
			_, err = tx.ExecContext(ctx, `
				UPDATE items SET kind = 'episode' WHERE id = ?
				   AND NOT EXISTS (SELECT 1 FROM item_field_sources fs WHERE fs.item_id = items.id AND fs.field = 'kind' AND fs.source = 'user')`,
				f.itemID)
			return err
		})
		if err != nil {
			return grouped, fmt.Errorf("%s: %w", f.rel, err)
		}
		grouped++
	}
	return grouped, nil
}
