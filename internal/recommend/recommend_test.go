package recommend

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hardope/itan/internal/db"
)

var (
	ctx = context.Background()
	now = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	day = int64(24 * 60 * 60)
)

type lib struct {
	t   *testing.T
	d   *sql.DB
	ids map[string]int64
}

func newLib(t *testing.T) *lib {
	t.Helper()
	d, err := db.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	l := &lib{t: t, d: d, ids: map[string]int64{}}
	l.exec(`INSERT INTO profiles(id, uid, name) VALUES (1, 'p1', 'Default')`)
	l.exec(`INSERT INTO libraries(id, name, root_path) VALUES (1, 'L', '/media')`)
	return l
}

func (l *lib) exec(q string, args ...any) {
	l.t.Helper()
	if _, err := l.d.ExecContext(ctx, q, args...); err != nil {
		l.t.Fatalf("%s: %v", q, err)
	}
}

// add creates an available item with tags ("genre:jazz", "person:x", ...).
func (l *lib) add(title string, tags ...string) int64 {
	l.t.Helper()
	var id int64
	if err := l.d.QueryRowContext(ctx,
		`INSERT INTO items(content_hash, size_bytes, title, duration_ms, added_at) VALUES (?, 1, ?, 600000, ?) RETURNING id`,
		"ph1:"+title, title, now.Unix()-100*day).Scan(&id); err != nil {
		l.t.Fatal(err)
	}
	l.exec(`INSERT INTO files(item_id, library_id, rel_path, size_bytes, mtime) VALUES (?, 1, ?, 1, 0)`, id, title)
	for _, ref := range tags {
		tagID, err := db.EnsureTag(ctx, l.d, ref)
		if err != nil {
			l.t.Fatal(err)
		}
		l.exec(`INSERT INTO item_tags(item_id, tag_id, source) VALUES (?, ?, 'user')`, id, tagID)
	}
	l.ids[title] = id
	return id
}

func (l *lib) watched(title string, daysAgo int64) {
	l.exec(`INSERT INTO play_state(profile_id, item_id, completed, play_count, last_played_at) VALUES (1, ?, 1, 1, ?)`,
		l.ids[title], now.Unix()-daysAgo*day)
}

func (l *lib) model() *Model {
	l.t.Helper()
	m, err := Load(ctx, l.d, 1, now)
	if err != nil {
		l.t.Fatal(err)
	}
	return m
}

func (l *lib) titles(recs []Rec) []string {
	names := map[int64]string{}
	for name, id := range l.ids {
		names[id] = name
	}
	var out []string
	for _, r := range recs {
		out = append(out, names[r.ItemID])
	}
	return out
}

// A mixed library: jazz concerts, nature documentaries, action movies.
func mixed(t *testing.T) *lib {
	l := newLib(t)
	for i := 1; i <= 4; i++ {
		l.add(fmt.Sprintf("jazz%d", i), "genre:music", "tag:jazz", "tag:live")
		l.add(fmt.Sprintf("nature%d", i), "genre:documentary", "tag:nature")
		l.add(fmt.Sprintf("action%d", i), "genre:action", "tag:explosions")
	}
	return l
}

func TestNoHistoryShowsRecentlyAdded(t *testing.T) {
	l := mixed(t)
	l.exec(`UPDATE items SET added_at = ? WHERE id = ?`, now.Unix(), l.ids["nature3"])
	m := l.model()
	if m.HasHistory() || m.ForYou(5, nil) != nil {
		t.Fatal("no history should give no taste recommendations")
	}
	shelves := m.Shelves(5)
	if len(shelves) != 1 || shelves[0].ID != "new" || l.titles(shelves[0].Items)[0] != "nature3" {
		t.Fatalf("shelves = %+v", shelves)
	}
}

func TestForYouFollowsTaste(t *testing.T) {
	l := mixed(t)
	l.watched("jazz1", 2)
	l.watched("jazz2", 5)
	recs := l.model().ForYou(3, nil)
	got := l.titles(recs)
	if len(got) < 2 || !strings.HasPrefix(got[0], "jazz") || !strings.HasPrefix(got[1], "jazz") {
		t.Fatalf("for you = %v, want unwatched jazz first", got)
	}
	for _, name := range got {
		if name == "jazz1" || name == "jazz2" {
			t.Fatalf("recommended something already watched: %v", got)
		}
	}
	if !strings.HasPrefix(recs[0].Reason, "Because you watched jazz") {
		t.Fatalf("reason = %q", recs[0].Reason)
	}
}

func TestLowRatingPushesAway(t *testing.T) {
	l := mixed(t)
	l.watched("jazz1", 2)
	l.watched("action1", 2)
	l.exec(`UPDATE items SET rating = 2 WHERE id = ?`, l.ids["action1"])
	for _, name := range l.titles(l.model().ForYou(5, nil)) {
		if strings.HasPrefix(name, "action") {
			t.Fatalf("recommended %s after a 2/10 action rating", name)
		}
	}
}

func TestFavoriteOutweighsOldViews(t *testing.T) {
	l := mixed(t)
	for _, n := range []string{"action1", "action2", "action3"} {
		l.watched(n, 400) // long ago, decayed
	}
	l.exec(`INSERT INTO play_state(profile_id, item_id, favorite) VALUES (1, ?, 1)`, l.ids["nature1"])
	recs := l.model().ForYou(1, nil)
	if got := l.titles(recs); len(got) != 1 || !strings.HasPrefix(got[0], "nature") {
		t.Fatalf("for you = %v, want nature", got)
	}
	if recs[0].Reason != "Because you liked nature1" {
		t.Fatalf("reason = %q", recs[0].Reason)
	}
}

func TestBailedEarlyCountsAgainst(t *testing.T) {
	l := mixed(t)
	l.watched("jazz1", 1)
	// Two minutes into a 10 minute documentary, 30 days ago.
	l.exec(`INSERT INTO play_state(profile_id, item_id, position_ms, last_played_at) VALUES (1, ?, 30000, ?)`,
		l.ids["nature1"], now.Unix()-30*day)
	m := l.model()
	if w := m.byID[l.ids["nature1"]].weight; w >= 0 {
		t.Fatalf("weight = %v, want negative", w)
	}
	for _, name := range l.titles(m.ForYou(5, nil)) {
		if strings.HasPrefix(name, "nature") {
			t.Fatalf("recommended %s", name)
		}
	}
}

func TestSimilar(t *testing.T) {
	l := mixed(t)
	l.add("jazz-doc", "genre:documentary", "tag:jazz")
	recs := l.model().Similar(l.ids["jazz1"], 4, nil, false)
	got := l.titles(recs)
	if len(got) != 4 || got[0][:4] != "jazz" || got[0] == "jazz-doc" {
		t.Fatalf("similar = %v", got)
	}
	for _, name := range got {
		if strings.HasPrefix(name, "action") {
			t.Fatalf("action is not like jazz: %v", got)
		}
	}
	if !strings.HasPrefix(recs[0].Reason, "Music · ") {
		t.Fatalf("reason = %q", recs[0].Reason)
	}
}

// Rare shared features matter more than common ones: two items by the same
// person beat two items that are merely both "music".
func TestRareFeaturesWin(t *testing.T) {
	l := newLib(t)
	l.add("a", "genre:music", "person:nina-simone")
	l.add("b", "genre:music", "person:nina-simone", "tag:live")
	l.add("c", "genre:music", "tag:live")
	for i := 0; i < 10; i++ {
		l.add(fmt.Sprintf("filler%d", i), "genre:music")
	}
	if got := l.titles(l.model().Similar(l.ids["a"], 1, nil, false)); got[0] != "b" {
		t.Fatalf("most similar to a = %v, want b", got)
	}
}

func TestDiversifyMixesGenres(t *testing.T) {
	l := newLib(t)
	for i := 1; i <= 6; i++ {
		l.add(fmt.Sprintf("jazz%d", i), "genre:music", "tag:jazz", "person:band")
	}
	for i := 1; i <= 3; i++ {
		l.add(fmt.Sprintf("blues%d", i), "genre:music", "tag:blues")
	}
	l.watched("jazz1", 1)
	l.watched("blues1", 1)
	got := l.titles(l.model().ForYou(4, nil))
	blues := 0
	for _, name := range got {
		if strings.HasPrefix(name, "blues") {
			blues++
		}
	}
	if blues == 0 {
		t.Fatalf("for you = %v, want at least one blues pick", got)
	}
}

func TestShelvesDontRepeat(t *testing.T) {
	l := newLib(t)
	for _, g := range []string{"jazz", "nature", "action", "comedy", "horror"} {
		for i := 1; i <= 8; i++ {
			l.add(fmt.Sprintf("%s%d", g, i), "genre:"+g)
		}
	}
	l.watched("jazz1", 1)
	l.watched("nature1", 3)
	shelves := l.model().Shelves(5)
	seen := map[int64]string{}
	var ids []string
	for _, sh := range shelves {
		ids = append(ids, sh.ID)
		if len(sh.Items) < 3 {
			t.Errorf("shelf %s has %d items", sh.ID, len(sh.Items))
		}
		for _, r := range sh.Items {
			if prev, dup := seen[r.ItemID]; dup {
				t.Errorf("item %d on %s and %s", r.ItemID, prev, sh.ID)
			}
			seen[r.ItemID] = sh.ID
		}
	}
	if len(shelves) < 2 || shelves[0].ID != "for-you" {
		t.Fatalf("shelves = %v", ids)
	}
	var different bool
	for _, sh := range shelves {
		different = different || sh.ID == "different"
		if strings.HasPrefix(sh.ID, "because-") && !strings.HasPrefix(sh.Title, "Because you watched") {
			t.Errorf("title = %q", sh.Title)
		}
	}
	if !different {
		t.Errorf("no 'something different' shelf: %v", ids)
	}
}

func TestParentTagCounts(t *testing.T) {
	l := newLib(t)
	l.add("bebop", "tag:bebop")
	l.add("swing", "tag:swing")
	l.add("metal", "tag:metal")
	for i := 0; i < 5; i++ {
		l.add(fmt.Sprintf("x%d", i), "tag:other")
	}
	l.exec(`INSERT INTO tags(namespace, slug, name) VALUES ('genre', 'jazz', 'Jazz')`)
	l.exec(`UPDATE tags SET parent_id = (SELECT id FROM tags WHERE slug = 'jazz') WHERE slug IN ('bebop', 'swing')`)
	recs := l.model().Similar(l.ids["bebop"], 3, nil, false)
	if got := l.titles(recs); len(got) != 1 || got[0] != "swing" {
		t.Fatalf("similar to bebop = %v, want only swing", got)
	}
	if recs[0].Reason != "Jazz" {
		t.Fatalf("reason = %q", recs[0].Reason)
	}
}

func TestMissingFilesExcluded(t *testing.T) {
	l := mixed(t)
	l.watched("jazz1", 1)
	l.exec(`UPDATE files SET missing_since = 1 WHERE item_id IN (?, ?, ?)`, l.ids["jazz2"], l.ids["jazz3"], l.ids["jazz4"])
	for _, name := range l.titles(l.model().ForYou(10, nil)) {
		if strings.HasPrefix(name, "jazz") {
			t.Fatalf("recommended unavailable %s", name)
		}
	}
}

// bigLibrary has 10,000 items with 6 tags each and 200 watched.
func bigLibrary(b *testing.B) *sql.DB {
	d, err := db.Open(ctx, b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { d.Close() })
	tx, _ := d.Begin()
	tx.Exec(`INSERT INTO profiles(id, uid, name) VALUES (1, 'p1', 'Default')`)
	tx.Exec(`INSERT INTO libraries(id, name, root_path) VALUES (1, 'L', '/media')`)
	for i := 0; i < 200; i++ {
		tx.Exec(`INSERT INTO tags(namespace, slug, name) VALUES (?, ?, ?)`,
			[]string{"genre", "tag", "person"}[i%3], fmt.Sprint("t", i), fmt.Sprint("T", i))
	}
	for i := 1; i <= 10000; i++ {
		tx.Exec(`INSERT INTO items(id, content_hash, size_bytes, title, kind, year) VALUES (?, ?, 1, ?, 'movie', ?)`,
			i, fmt.Sprint("h", i), fmt.Sprint("item", i), 1960+i%60)
		tx.Exec(`INSERT INTO files(item_id, library_id, rel_path, size_bytes, mtime) VALUES (?, 1, ?, 1, 0)`, i, fmt.Sprint(i))
		for k := 0; k < 6; k++ {
			tx.Exec(`INSERT OR IGNORE INTO item_tags(item_id, tag_id) VALUES (?, ?)`, i, 1+(i*7+k*31)%200)
		}
		if i%50 == 0 {
			tx.Exec(`INSERT INTO play_state(profile_id, item_id, completed, play_count, last_played_at) VALUES (1, ?, 1, 1, ?)`,
				i, now.Unix()-int64(i))
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	return d
}

func BenchmarkShelves10k(b *testing.B) {
	d := bigLibrary(b)
	for b.Loop() {
		m, err := Load(ctx, d, 1, now)
		if err != nil {
			b.Fatal(err)
		}
		if len(m.Shelves(12)) == 0 {
			b.Fatal("no shelves")
		}
	}
}

// With the library features cached, only watch history is read per request.
func BenchmarkShelves10kCached(b *testing.B) {
	d := bigLibrary(b)
	lib, err := LoadLibrary(ctx, d)
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		m, err := lib.Model(ctx, d, 1, now)
		if err != nil {
			b.Fatal(err)
		}
		if len(m.Shelves(12)) == 0 {
			b.Fatal("no shelves")
		}
	}
}
