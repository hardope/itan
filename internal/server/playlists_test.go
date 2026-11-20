package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
)

func TestPlaylists(t *testing.T) {
	srv, s, x := testServer(t)
	ctx := context.Background()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	addItem := func(hash, title string, missing bool) int64 {
		t.Helper()
		var id int64
		if err := s.db.QueryRowContext(ctx, `INSERT INTO items(content_hash, size_bytes, title, duration_ms) VALUES (?, 1, ?, 60000) RETURNING id`,
			hash, title).Scan(&id); err != nil {
			t.Fatal(err)
		}
		var gone any
		if missing {
			gone = 1
		}
		mustExec(`INSERT INTO files(item_id, library_id, rel_path, size_bytes, mtime, missing_since) VALUES (?, 1, ?, 1, 0, ?)`,
			id, title+".mp4", gone)
		return id
	}
	y, z := addItem("ph1:y", "y", false), addItem("ph1:z", "z", false)
	w := addItem("ph1:w", "w", true) // on a drive that isn't plugged in

	c := client(t)
	decode := func(method, url, body string, want int) playlistDetail {
		t.Helper()
		code, body := call(t, c, method, url, body)
		if code != want {
			t.Fatalf("status %d, want %d: %s", code, want, body)
		}
		var d playlistDetail
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		return d
	}
	shown := func(d playlistDetail) []int64 {
		var ids []int64
		for _, it := range d.Items {
			ids = append(ids, it.ID)
		}
		return ids
	}
	order := func(id int64) []int64 {
		t.Helper()
		ids, err := playlistOrder(ctx, s.db, id)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}

	if code, body := call(t, c, "POST", srv.URL+"/api/playlists", `{"title": "  "}`); code != http.StatusBadRequest {
		t.Errorf("playlist without a name: %d %s", code, body)
	}
	// Repeats and videos that don't exist are skipped.
	d := decode("POST", srv.URL+"/api/playlists", fmt.Sprintf(`{"title": " Road trip ", "items": [%d, %d, %d, 999]}`, x, y, x), http.StatusCreated)
	if d.Title != "Road trip" || !slices.Equal(shown(d), []int64{x, y}) || d.Next == nil || d.Next.ID != x || d.NextVerb != "Play" {
		t.Fatalf("created: %+v %v", d.playlistJSON, shown(d))
	}
	url := fmt.Sprintf("%s/api/playlists/%d", srv.URL, d.ID)

	// Adding goes at the end; a video already in it stays where it is.
	// The missing one is kept but not shown.
	d = decode("POST", url+"/items", fmt.Sprintf(`{"items": [%d, %d, %d]}`, w, z, y), http.StatusOK)
	if !slices.Equal(shown(d), []int64{x, y, z}) || !slices.Equal(order(d.ID), []int64{x, y, w, z}) || d.Count != 3 || d.DurationMS != 120000 {
		t.Fatalf("after adding: shown %v, order %v, %+v", shown(d), order(d.ID), d.playlistJSON)
	}

	// Reordering what the page shows leaves the missing video in its place.
	d = decode("PATCH", url, fmt.Sprintf(`{"order": [%d, %d, %d, 999]}`, z, y, x), http.StatusOK)
	if !slices.Equal(order(d.ID), []int64{z, y, w, x}) || !slices.Equal(shown(d), []int64{z, y, x}) {
		t.Fatalf("after reordering: order %v, shown %v", order(d.ID), shown(d))
	}
	if d.CoverID == nil || *d.CoverID != z {
		t.Errorf("cover = %v, want the first video shown", d.CoverID)
	}

	// Next: after the last one watched, skipping the missing one.
	mustExec(`INSERT INTO play_state(profile_id, item_id, completed, last_played_at) VALUES (1, ?, 1, 200)`, y)
	mustExec(`INSERT INTO play_state(profile_id, item_id, completed, last_played_at) VALUES (1, ?, 1, 100)`, z)
	d = decode("GET", url, "", http.StatusOK)
	if d.Next == nil || d.Next.ID != x || d.NextVerb != "Continue" || d.Watched != 2 {
		t.Errorf("next = %+v (%s), watched %d", d.Next, d.NextVerb, d.Watched)
	}

	// The picker asks which playlists have these videos.
	_, body := call(t, c, "GET", fmt.Sprintf("%s/api/playlists?items=%d,%d,bad", srv.URL, x, 999), "")
	var list []playlistJSON
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list) != 1 || list[0].Contains == nil || *list[0].Contains != 1 {
		t.Fatalf("list with items: %v %s", err, body)
	}
	_, body = call(t, c, "GET", srv.URL+"/api/playlists", "")
	var plain []playlistJSON
	if err := json.Unmarshal([]byte(body), &plain); err != nil || len(plain) != 1 || plain[0].Contains != nil {
		t.Fatalf("plain list: %v %s", err, body)
	}

	// Taking one out closes the gap.
	d = decode("DELETE", url+"/items", fmt.Sprintf(`{"items": [%d]}`, y), http.StatusOK)
	if !slices.Equal(order(d.ID), []int64{z, w, x}) {
		t.Fatalf("after removing: %v", order(d.ID))
	}
	var positions []int
	rows, _ := s.db.QueryContext(ctx, `SELECT position FROM collection_items WHERE collection_id = ? ORDER BY position`, d.ID)
	for rows.Next() {
		var p int
		rows.Scan(&p)
		positions = append(positions, p)
	}
	rows.Close()
	if !slices.Equal(positions, []int{1, 2, 3}) {
		t.Errorf("positions = %v", positions)
	}

	if code, body := call(t, c, "PATCH", url, `{"title": ""}`); code != http.StatusBadRequest {
		t.Errorf("emptying the name: %d %s", code, body)
	}
	d = decode("PATCH", url, `{"title": "Trip", "description": "  "}`, http.StatusOK)
	if d.Title != "Trip" || d.Description != nil {
		t.Errorf("renamed: %q %v", d.Title, d.Description)
	}
	if code, _ := call(t, c, "PATCH", srv.URL+"/api/playlists/999", `{"title": "x"}`); code != http.StatusNotFound {
		t.Errorf("editing a playlist that isn't there: %d", code)
	}

	// With a PIN, other devices can play a playlist but not change it.
	call(t, c, "PUT", srv.URL+"/api/auth/pin", `{"pin": "2468"}`)
	guest := client(t)
	if code, _ := call(t, guest, "GET", url, ""); code != http.StatusOK {
		t.Errorf("guest can't see the playlist: %d", code)
	}
	for _, r := range [][3]string{{"POST", srv.URL + "/api/playlists", `{"title": "Mine"}`}, {"POST", url + "/items", `{"items": [1]}`},
		{"DELETE", url + "/items", `{"items": [1]}`}, {"PATCH", url, `{"title": "Mine"}`}, {"DELETE", url, ""}} {
		if code, _ := call(t, guest, r[0], r[1], r[2]); code != http.StatusUnauthorized {
			t.Errorf("guest %s %s = %d", r[0], r[1], code)
		}
	}

	if code, _ := call(t, c, "DELETE", url, ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := call(t, c, "GET", url, ""); code != http.StatusNotFound {
		t.Errorf("deleted playlist still there: %d", code)
	}
	var items int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items`).Scan(&items)
	if items != 4 {
		t.Errorf("deleting the playlist removed videos: %d left", items)
	}
}
