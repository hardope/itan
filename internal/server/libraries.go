package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hardope/itan/internal/db"
	"github.com/hardope/itan/internal/metadata"
	"github.com/hardope/itan/internal/scanner"
)

type libraryJSON struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	DefaultKind   string `json:"default_kind"`
	FolderTags    bool   `json:"folder_tags"`
	WriteSidecars bool   `json:"write_sidecars"`
	Items         int    `json:"items"`
	LastScanAt    *int64 `json:"last_scan_at"`
	// Inside names the library whose folder this one is in (the closest),
	// which then skips this folder; Contains names libraries inside this one.
	Inside     string   `json:"inside,omitempty"`
	InsideKeep bool     `json:"inside_keeps_file,omitempty"` // a library around it keeps a folder file, which covers this one
	Contains   []string `json:"contains,omitempty"`
}

func (s *Server) listLibraries(w http.ResponseWriter, r *http.Request) {
	libs := []libraryJSON{}
	err := eachRow(r.Context(), s.db, `
		SELECT l.id, l.name, l.root_path, l.default_kind, l.folder_tags, l.write_sidecars, l.last_scan_at,
		       (SELECT COUNT(DISTINCT item_id) FROM files f WHERE f.library_id = l.id AND f.missing_since IS NULL)
		  FROM libraries l ORDER BY l.name`, nil,
		func(rows *sql.Rows) error {
			var l libraryJSON
			if err := rows.Scan(&l.ID, &l.Name, &l.Path, &l.DefaultKind, &l.FolderTags, &l.WriteSidecars, &l.LastScanAt, &l.Items); err != nil {
				return err
			}
			libs = append(libs, l)
			return nil
		})
	if err != nil {
		serverError(w, r, err)
		return
	}
	for i := range libs {
		var inside *libraryJSON
		for j := range libs {
			if _, ok := db.Within(libs[j].Path, libs[i].Path); ok {
				libs[i].InsideKeep = libs[i].InsideKeep || libs[j].WriteSidecars
				if inside == nil || len(libs[j].Path) > len(inside.Path) {
					inside = &libs[j]
				}
			}
		}
		if inside != nil {
			libs[i].Inside = inside.Name
			inside.Contains = append(inside.Contains, libs[i].Name)
		}
	}
	writeJSON(w, http.StatusOK, libs)
}

// updateLibrary changes library settings. Body (all optional):
//
//	{"write_sidecars": true, "folder_tags": false, "default_kind": "documentary"}
//
// write_sidecars keeps a folder file (.itan.json, see metadata) with the
// library's edits. Turning it on writes the file right away, folding in any
// per-video sidecars; if the folder can't be written it's turned back off.
func (s *Server) updateLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	var body struct {
		WriteSidecars *bool   `json:"write_sidecars"`
		FolderTags    *bool   `json:"folder_tags"`
		DefaultKind   *string `json:"default_kind"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	ctx := r.Context()
	set := func(col string, v any) error {
		res, err := s.db.ExecContext(ctx, `UPDATE libraries SET `+col+` = ? WHERE id = ?`, v, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return nil
	}
	var err error
	if body.FolderTags != nil {
		err = set("folder_tags", *body.FolderTags)
	}
	if err == nil && body.DefaultKind != nil {
		err = set("default_kind", *body.DefaultKind)
	}
	if err == nil && body.WriteSidecars != nil {
		err = set("write_sidecars", *body.WriteSidecars)
	}
	if err != nil {
		serverError(w, r, err)
		return
	}

	if body.WriteSidecars != nil && *body.WriteSidecars {
		res, err := metadata.SyncFolderFile(ctx, s.db, id)
		if err != nil {
			log.Printf("library %d: %v", id, err)
			s.db.ExecContext(ctx, `UPDATE libraries SET write_sidecars = 0 WHERE id = ?`, id)
			msg := fmt.Sprintf("Couldn't save edits in this folder: %v", err)
			if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS) {
				msg = "This folder is read-only (for example a :ro Docker mount), so edits can't be saved in it."
			}
			writeError(w, http.StatusConflict, msg)
			return
		}
		if res.Migrated > 0 {
			log.Printf("library %d: moved %d .meta.json files into %s", id, res.Migrated, metadata.FolderFileName)
		}
	}
	s.recs.invalidate() // folder_tags or default_kind may change what scans add
	s.listLibraries(w, r)
}

// addLibrary registers a folder and starts a scan. The folder can be inside
// another library's (see db.NestedLibraries). Body:
//
//	{"name": "Documentaries", "path": "/media/docs", "default_kind": "documentary"}
func (s *Server) addLibrary(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Path        string `json:"path"`
		DefaultKind string `json:"default_kind"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	body.Path = strings.TrimSpace(body.Path)
	if !filepath.IsAbs(body.Path) {
		writeError(w, http.StatusBadRequest, "path must be absolute")
		return
	}
	body.Path = filepath.Clean(body.Path)
	if st, err := os.Stat(body.Path); err != nil || !st.IsDir() {
		writeError(w, http.StatusBadRequest, "path is not a readable folder on the server")
		return
	}
	var taken bool
	err := eachRow(r.Context(), s.db, `SELECT root_path FROM libraries`, nil, func(rows *sql.Rows) error {
		var root string
		if err := rows.Scan(&root); err != nil {
			return err
		}
		taken = taken || db.SamePath(root, body.Path)
		return nil
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	if taken {
		writeError(w, http.StatusConflict, "that folder is already a library")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		body.Name = filepath.Base(body.Path)
	}
	if body.DefaultKind == "" {
		body.DefaultKind = "video"
	}
	// A folder that already has a folder file (the drive was used with
	// another install) keeps it up to date here too.
	_, statErr := os.Stat(filepath.Join(body.Path, metadata.FolderFileName))
	_, err = s.db.ExecContext(r.Context(),
		`INSERT INTO libraries(name, root_path, default_kind, write_sidecars) VALUES (?, ?, ?, ?)`,
		strings.TrimSpace(body.Name), body.Path, body.DefaultKind, statErr == nil)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "that folder is already a library")
			return
		}
		serverError(w, r, err)
		return
	}
	s.scanInBackground()
	s.listLibraries(w, r)
}

// deleteLibrary forgets a library and its file locations. Item metadata is
// kept: if the same files show up in another library, their edits return.
func (s *Server) deleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad id")
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM libraries WHERE id = ?`, id); err != nil {
		serverError(w, r, err)
		return
	}
	s.recs.invalidate()
	s.listLibraries(w, r)
}

func (s *Server) scanStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.scanner.Status())
}

func (s *Server) startScan(w http.ResponseWriter, r *http.Request) {
	if s.scanner.Status().Running {
		writeError(w, http.StatusConflict, "scan already running")
		return
	}
	s.scanInBackground()
	writeJSON(w, http.StatusAccepted, s.scanner.Status())
}

func (s *Server) scanInBackground() {
	go func() {
		if err := s.scanner.Scan(context.Background()); err != nil && !errors.Is(err, scanner.ErrRunning) {
			log.Printf("scan: %v", err)
		}
	}()
}
