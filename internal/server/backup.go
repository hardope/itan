package server

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/hardope/itan/internal/metadata"
)

const maxImportBytes = 512 << 20

// exportLibrary downloads all metadata. Query: history=1 includes watch
// state; gzip=1 compresses the file.
func (s *Server) exportLibrary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lib, err := metadata.Export(r.Context(), s.db, q.Get("history") == "1", "itan")
	if err != nil {
		serverError(w, r, err)
		return
	}
	name := "itan-export-" + time.Now().Format("2006-01-02") + ".json"
	out := http.ResponseWriter(w)
	var enc *json.Encoder
	if q.Get("gzip") == "1" {
		name += ".gz"
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		gz := gzip.NewWriter(out)
		defer gz.Close()
		enc = json.NewEncoder(gz)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		enc = json.NewEncoder(out)
	}
	enc.SetIndent("", "  ")
	if err := enc.Encode(lib); err != nil {
		log.Printf("export: %v", err)
	}
}

// importLibrary applies an uploaded export file (the raw request body,
// plain or gzipped). Query: mode=merge|replace, dry_run=1 to preview.
func (s *Server) importLibrary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mode := q.Get("mode")
	if mode == "" {
		mode = metadata.ModeMerge
	}
	if mode != metadata.ModeMerge && mode != metadata.ModeReplace {
		writeError(w, http.StatusBadRequest, "mode must be merge or replace")
		return
	}
	lib, err := metadata.ReadLibrary(http.MaxBytesReader(w, r.Body, maxImportBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "file is too large")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rep, err := metadata.Import(r.Context(), s.db, lib, mode, q.Get("dry_run") == "1")
	if err != nil {
		serverError(w, r, err)
		return
	}
	if !rep.DryRun {
		log.Printf("import (%s): %d items, %d matched, %d pending, %d fields updated",
			mode, rep.Items, rep.Matched["hash"]+rep.Matched["path"]+rep.Matched["name"], rep.Pending, rep.FieldsUpdated)
	}
	writeJSON(w, http.StatusOK, rep)
}

// writeSidecars refreshes an item's .meta.json files after a user edit.
// Failures (for example a read-only mount) are logged, not returned: the
// edit itself is saved in the database either way.
func (s *Server) writeSidecars(r *http.Request, itemID int64) {
	if err := metadata.WriteSidecars(r.Context(), s.db, itemID); err != nil {
		log.Printf("item %d: %v", itemID, err)
	}
}
