package server

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/hardope/itan/internal/config"
	"github.com/hardope/itan/internal/db"
	"github.com/hardope/itan/internal/scanner"
)

// testServer runs the full handler on a fresh database with one item.
func testServer(t *testing.T) (*httptest.Server, *Server, int64) {
	t.Helper()
	pinIterations = 1000 // keep tests fast
	ctx := context.Background()
	dir := t.TempDir()
	d, err := db.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.ExecContext(ctx, `INSERT INTO profiles(id, uid, name) VALUES (1, 'p', 'Default')`)
	d.ExecContext(ctx, `INSERT INTO libraries(id, name, root_path) VALUES (1, 'L', ?)`, dir)
	var id int64
	d.QueryRowContext(ctx, `INSERT INTO items(content_hash, size_bytes, title) VALUES ('ph1:1', 1, 'x') RETURNING id`).Scan(&id)
	d.ExecContext(ctx, `INSERT INTO files(item_id, library_id, rel_path, size_bytes, mtime) VALUES (?, 1, 'x.mp4', 1, 0)`, id)
	cfg := &config.Config{DataDir: dir, FFmpeg: "ffmpeg", FFprobe: "ffprobe", HWAccel: "none"}
	s := New(d, cfg, scanner.New(d, "ffprobe"))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s, id
}

// client is one browser: it keeps its own cookies.
func client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func call(t *testing.T, c *http.Client, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestEditPIN(t *testing.T) {
	srv, s, id := testServer(t)
	item := srv.URL + "/api/items/" + strconv.FormatInt(id, 10)
	owner, guest := client(t), client(t)

	// No PIN: anyone can edit.
	if code, _ := call(t, guest, "PATCH", item, `{"title": "Open"}`); code != 200 {
		t.Fatalf("edit without PIN = %d", code)
	}
	if code, body := call(t, owner, "PUT", srv.URL+"/api/auth/pin", `{"pin": "12a4"}`); code != 400 {
		t.Fatalf("non-digit PIN accepted: %d %s", code, body)
	}
	if code, _ := call(t, owner, "PUT", srv.URL+"/api/auth/pin", `{"pin": "2468"}`); code != 200 {
		t.Fatal("set PIN failed")
	}

	// The device that set it stays unlocked; others are locked out of edits only.
	if code, _ := call(t, owner, "PATCH", item, `{"title": "Mine"}`); code != 200 {
		t.Errorf("owner edit = %d", code)
	}
	if code, body := call(t, guest, "PATCH", item, `{"title": "Nope"}`); code != 401 || !strings.Contains(body, `"locked":true`) {
		t.Errorf("guest edit = %d %s", code, body)
	}
	if code, _ := call(t, guest, "GET", item, ""); code != 200 {
		t.Errorf("guest can't view: %d", code)
	}
	if code, _ := call(t, guest, "PUT", item+"/favorite", `{"favorite": true}`); code != 200 {
		t.Errorf("guest can't favorite: %d", code)
	}
	for _, path := range []string{"/api/export"} {
		if code, _ := call(t, guest, "GET", srv.URL+path, ""); code != 401 {
			t.Errorf("guest %s = %d", path, code)
		}
	}
	if _, body := call(t, guest, "GET", srv.URL+"/api/auth", ""); body != `{"pin_set":true,"unlocked":false}`+"\n" {
		t.Errorf("guest auth = %s", body)
	}

	// Wrong PINs are limited: five tries, then a pause even for the right one.
	for i := 0; i < 5; i++ {
		if code, _ := call(t, guest, "POST", srv.URL+"/api/auth/unlock", `{"pin": "0000"}`); code != 403 {
			t.Fatalf("wrong PIN #%d = %d", i+1, code)
		}
	}
	if code, body := call(t, guest, "POST", srv.URL+"/api/auth/unlock", `{"pin": "2468"}`); code != 429 || !strings.Contains(body, "Try again in 30 seconds") {
		t.Fatalf("after 5 misses = %d %s", code, body)
	}
	s.pins.succeeded("127.0.0.1") // the wait is over

	if code, _ := call(t, guest, "POST", srv.URL+"/api/auth/unlock", `{"pin": "2468"}`); code != 200 {
		t.Fatal("unlock failed")
	}
	if code, _ := call(t, guest, "PATCH", item, `{"title": "Unlocked"}`); code != 200 {
		t.Errorf("edit after unlock = %d", code)
	}
	call(t, guest, "POST", srv.URL+"/api/auth/lock", "")
	if code, _ := call(t, guest, "PATCH", item, `{"title": "Locked again"}`); code != 401 {
		t.Errorf("edit after lock = %d", code)
	}

	// Changing needs the current PIN and locks every other device.
	call(t, guest, "POST", srv.URL+"/api/auth/unlock", `{"pin": "2468"}`)
	if code, _ := call(t, owner, "PUT", srv.URL+"/api/auth/pin", `{"current": "1111", "pin": "13579"}`); code != 403 {
		t.Errorf("change with wrong current = %d", code)
	}
	if code, _ := call(t, owner, "PUT", srv.URL+"/api/auth/pin", `{"current": "2468", "pin": "13579"}`); code != 200 {
		t.Fatal("change PIN failed")
	}
	if code, _ := call(t, guest, "PATCH", item, `{"title": "Stale session"}`); code != 401 {
		t.Errorf("other device still unlocked after PIN change: %d", code)
	}

	// Removing it opens editing again.
	if code, _ := call(t, owner, "DELETE", srv.URL+"/api/auth/pin", `{"current": "13579"}`); code != 200 {
		t.Fatal("remove PIN failed")
	}
	if code, _ := call(t, guest, "PATCH", item, `{"title": "Open again"}`); code != 200 {
		t.Errorf("edit after PIN removed = %d", code)
	}
}

func TestPinHash(t *testing.T) {
	pinIterations = 1000
	h, err := hashPIN("4321")
	if err != nil {
		t.Fatal(err)
	}
	if !checkPIN(h, "4321") || checkPIN(h, "4322") || checkPIN("garbage", "4321") {
		t.Error("checkPIN wrong")
	}
	h2, _ := hashPIN("4321")
	if h == h2 {
		t.Error("same hash twice: salt missing")
	}
}
