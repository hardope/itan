package server

import (
	"context"
	"database/sql"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/hardope/itan/internal/db"
	"github.com/hardope/itan/internal/metadata"
)

// folderWriter keeps folder files (.itan.json) up to date after edits.
// Writes are batched: a burst of edits (a title, then tags, then a rating)
// becomes one write a couple of seconds after the last one.
type folderWriter struct {
	db    *sql.DB
	delay time.Duration

	mu    sync.Mutex
	dirty map[int64]bool // library ids
	timer *time.Timer
}

func newFolderWriter(database *sql.DB) *folderWriter {
	return &folderWriter{db: database, delay: 2 * time.Second, dirty: map[int64]bool{}}
}

// itemChanged schedules a write for each library that keeps a folder file
// and has the item, in its folder or in a library inside it.
func (fw *folderWriter) itemChanged(itemID int64) {
	ctx := context.Background()
	keep, err := fw.ids(ctx, `SELECT id FROM libraries WHERE write_sidecars = 1`)
	if err != nil || len(keep) == 0 {
		return
	}
	has, err := fw.ids(ctx, `SELECT DISTINCT library_id FROM files WHERE item_id = ?`, itemID)
	if err != nil {
		return
	}
	var ids []int64
	for _, lib := range has {
		around, err := db.ContainingLibraries(ctx, fw.db, lib)
		if err != nil {
			log.Printf("folder files: %v", err)
			return
		}
		for _, id := range around {
			if slices.Contains(keep, id) {
				ids = append(ids, id)
			}
		}
	}
	fw.schedule(ids)
}

// allChanged schedules a write for every library that keeps one.
func (fw *folderWriter) allChanged() {
	if ids, err := fw.ids(context.Background(), `SELECT id FROM libraries WHERE write_sidecars = 1`); err == nil {
		fw.schedule(ids)
	}
}

func (fw *folderWriter) ids(ctx context.Context, query string, args ...any) ([]int64, error) {
	var ids []int64
	err := eachRow(ctx, fw.db, query, args, func(rows *sql.Rows) error {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	})
	if err != nil {
		log.Printf("folder files: %v", err)
	}
	return ids, err
}

func (fw *folderWriter) schedule(ids []int64) {
	if len(ids) == 0 {
		return
	}
	fw.mu.Lock()
	defer fw.mu.Unlock()
	for _, id := range ids {
		fw.dirty[id] = true
	}
	if fw.timer == nil {
		fw.timer = time.AfterFunc(fw.delay, fw.flush)
	} else {
		fw.timer.Reset(fw.delay)
	}
}

// flush writes the folder files of every library with pending changes.
// The edits are in the database already; a failed write (say, a read-only
// drive) is logged and retried after the next edit or scan.
func (fw *folderWriter) flush() {
	fw.mu.Lock()
	ids := fw.dirty
	fw.dirty = map[int64]bool{}
	fw.mu.Unlock()
	for id := range ids {
		res, err := metadata.WriteFolderFile(context.Background(), fw.db, id)
		if err != nil {
			log.Printf("library %d: %v", id, err)
		} else if res.Read && res.Changes > 0 {
			log.Printf("library %d: took in %d changes made elsewhere before saving %s", id, res.Changes, metadata.FolderFileName)
		}
	}
}

// Close writes anything still pending, for a clean shutdown.
func (fw *folderWriter) Close() {
	fw.mu.Lock()
	if fw.timer != nil {
		fw.timer.Stop()
	}
	fw.mu.Unlock()
	fw.flush()
}
