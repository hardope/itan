package server

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// "Another moment" steps through the video and back to the default frame,
// a frame from the player is kept as given, and regenerating removes
// every thumbnail. (No ffmpeg here: the frames themselves aren't made.)
func TestThumbnails(t *testing.T) {
	srv, s, id := testServer(t)
	s.thumbs.ffmpeg = filepath.Join(t.TempDir(), "no-ffmpeg")
	ctx := context.Background()
	s.db.ExecContext(ctx, `UPDATE items SET duration_ms = 100000 WHERE id = ?`, id)
	c := client(t)
	url := srv.URL + "/api/items/" + strconv.FormatInt(id, 10) + "/thumbnail"
	at := func() any {
		var v sql.NullInt64
		s.db.QueryRowContext(ctx, `SELECT thumbnail_at_ms FROM items WHERE id = ?`, id).Scan(&v)
		if !v.Valid {
			return nil
		}
		return v.Int64
	}

	for _, want := range []any{int64(25000), int64(40000), int64(55000), int64(70000), int64(85000), nil, int64(25000)} {
		if code, body := call(t, c, "POST", url, `{}`); code != http.StatusUnprocessableEntity {
			t.Fatalf("another moment: %d %s (want 422: there's no ffmpeg)", code, body)
		}
		if got := at(); got != want {
			t.Fatalf("moment %v, want %v", got, want)
		}
	}
	call(t, c, "POST", url, `{"at_ms": 12345}`)
	if got := at(); got != int64(12345) {
		t.Errorf("frame from the player: %v", got)
	}
	call(t, c, "POST", url, `{"at_ms": 500000}`)
	if got := at(); got != int64(99999) {
		t.Errorf("frame past the end: %v, want the last moment", got)
	}

	dir := filepath.Join(s.cfg.DataDir, "thumbs")
	os.MkdirAll(dir, 0o755)
	for _, name := range []string{"ph1_a-w640.jpg", "ph1_b-w640.jpg"} {
		os.WriteFile(filepath.Join(dir, name), []byte("jpg"), 0o644)
	}
	if code, body := call(t, c, "POST", srv.URL+"/api/thumbnails/regenerate", ""); code != http.StatusOK || body != `{"removed":2}`+"\n" {
		t.Errorf("regenerate: %d %s", code, body)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("%d thumbnails left", len(left))
	}
}
