package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
	writeJSON(w, http.StatusOK, libs)
}

// updateLibrary changes library settings. Body (all optional):
//
//	{"write_sidecars": true, "folder_tags": false, "default_kind": "documentary"}
//
// Turning write_sidecars on writes a sidecar for every item a user has
// edited, so existing edits are covered too.
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
		if failed, err := s.writeEditedSidecars(r, id); err != nil {
			serverError(w, r, err)
			return
		} else if failed > 0 {
			writeError(w, http.StatusConflict, fmt.Sprintf(
				"sidecars are on, but %d couldn't be written. Is the folder read-only (for example a :ro Docker mount)?", failed))
			return
		}
	}
	s.listLibraries(w, r)
}

// writeEditedSidecars writes sidecars for every user-edited item with a file
// in the library. Returns how many items failed.
func (s *Server) writeEditedSidecars(r *http.Request, libraryID int64) (int, error) {
	var ids []int64
	err := eachRow(r.Context(), s.db, `
		SELECT DISTINCT f.item_id FROM files f
		 WHERE f.library_id = ? AND f.missing_since IS NULL
		   AND (EXISTS (SELECT 1 FROM item_field_sources s WHERE s.item_id = f.item_id AND s.source = 'user')
		     OR EXISTS (SELECT 1 FROM item_tags t WHERE t.item_id = f.item_id AND (t.source = 'user' OR t.removed = 1)))`,
		[]any{libraryID}, func(rows *sql.Rows) error {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
			return nil
		})
	if err != nil {
		return 0, err
	}
	failed := 0
	for _, id := range ids {
		if err := metadata.WriteSidecars(r.Context(), s.db, id); err != nil {
			log.Printf("item %d: %v", id, err)
			failed++
		}
	}
	return failed, nil
}

// addLibrary registers a folder and starts a scan. Body:
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
	if strings.TrimSpace(body.Name) == "" {
		body.Name = filepath.Base(body.Path)
	}
	if body.DefaultKind == "" {
		body.DefaultKind = "video"
	}
	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO libraries(name, root_path, default_kind) VALUES (?, ?, ?)`,
		strings.TrimSpace(body.Name), body.Path, body.DefaultKind)
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
