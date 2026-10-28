// Package recommend suggests what to watch next from the library's own
// metadata and one profile's watch history. No external services, no
// training step: every request builds a small in-memory model.
//
// How it works:
//
//   - Each item becomes a sparse feature vector: its tags (weighted by
//     namespace, plus parent tags at half weight), collections, kind, decade
//     and language. Each feature is scaled by its IDF, so a tag on half the
//     library says little and a rare one says a lot. Vectors are L2-normalized,
//     so the similarity of two items is their dot product (cosine).
//   - Watch history gives each item an engagement weight: finishing it,
//     watching most of it, a favorite and a high rating count for; bailing out
//     in the first minutes and a low rating count against. Play signals fade
//     with time (half-life 120 days); favorites and ratings don't.
//   - The taste vector is the weighted sum of watched items' vectors. "For
//     you" ranks unwatched items by similarity to it, then re-ranks with
//     maximal marginal relevance so a shelf isn't twelve copies of one genre.
package recommend

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hardope/itan/internal/db"
)

// Rec is one suggested item and a short human-readable reason.
type Rec struct {
	ItemID int64
	Score  float64
	Reason string
}

// Shelf is a titled row of recommendations for the home page.
type Shelf struct {
	ID    string
	Title string
	Items []Rec
}

// Feature weights before IDF. Genres and people are the strongest signal of
// taste; folder-derived tags are often just "Movies" or "Downloads".
var (
	namespaceWeight = map[string]float64{
		"genre": 1.0, "person": 1.2, "mood": 0.8, "tag": 0.7, "source": 0.3,
	}
	folderTagWeight  = 0.5 // multiplier for tags that came from folder names
	parentTagWeight  = 0.5 // "jazz" also counts as half of its parent "music"
	collectionWeight = 1.5
	kindWeight       = 0.4
	decadeWeight     = 0.3
	languageWeight   = 0.3
)

const (
	playHalfLife = 120 * 24 * time.Hour
	minDecay     = 0.25 // an old binge still says something
	newHalfLife  = 14 * 24 * time.Hour
	newBoost     = 0.03 // tie-breaker in favour of recent additions
	minScore     = 0.05
)

// features is the profile-independent part of an item.
type features struct {
	id       int64
	title    string
	addedAt  int64
	rating   sql.NullInt64 // the household's own rating
	duration int64
	vec      map[string]float64 // L2-normalized
}

// Library is every available item's features. It only depends on metadata,
// so it can be cached and shared between profiles.
type Library struct {
	items  []*features
	labels map[string]string // feature -> display name, for reasons
}

type item struct {
	*features
	seen    bool    // watched, started, rated or favorited
	weight  float64 // engagement; 0 if no history
	liked   bool    // favorite or rated 8+
	played  int64   // last_played_at, 0 if never
	playing bool    // watched a fair part of it (anchor for "because you watched")
}

// Model is one profile's view of the library.
type Model struct {
	items  []*item
	byID   map[int64]*item
	labels map[string]string
	taste  map[string]float64
	now    time.Time
}

// Load builds a profile's model straight from the database.
func Load(ctx context.Context, q db.Execer, profileID int64, now time.Time) (*Model, error) {
	lib, err := LoadLibrary(ctx, q)
	if err != nil {
		return nil, err
	}
	return lib.Model(ctx, q, profileID, now)
}

// LoadLibrary computes feature vectors for items that have a file on disk.
func LoadLibrary(ctx context.Context, q db.Execer) (*Library, error) {
	lib := &Library{labels: map[string]string{}}
	raw := map[int64]map[string]float64{}
	add := func(id int64, feature string, w float64) {
		if v, ok := raw[id]; ok && w > v[feature] {
			v[feature] = w
		}
	}

	err := eachRow(ctx, q, `
		SELECT i.id, i.title, i.added_at, i.kind, i.year, i.language, i.rating, COALESCE(i.duration_ms, 0)
		  FROM items i
		 WHERE EXISTS (SELECT 1 FROM files f WHERE f.item_id = i.id AND f.missing_since IS NULL)`,
		nil, func(rows *sql.Rows) error {
			var (
				it   = &features{}
				kind string
				year sql.NullInt64
				lang sql.NullString
			)
			if err := rows.Scan(&it.id, &it.title, &it.addedAt, &kind, &year, &lang, &it.rating, &it.duration); err != nil {
				return err
			}
			raw[it.id] = map[string]float64{"kind:" + kind: kindWeight}
			if year.Valid && year.Int64 > 0 {
				raw[it.id][fmt.Sprintf("decade:%d", year.Int64/10*10)] = decadeWeight
			}
			if lang.Valid && lang.String != "" {
				raw[it.id]["lang:"+strings.ToLower(lang.String)] = languageWeight
			}
			lib.items = append(lib.items, it)
			return nil
		})
	if err != nil {
		return nil, err
	}

	err = eachRow(ctx, q, `
		SELECT it.item_id, it.tag_id, t.namespace, t.name, it.source, p.id, p.name
		  FROM item_tags it
		  JOIN tags t ON t.id = it.tag_id
		  LEFT JOIN tags p ON p.id = t.parent_id
		 WHERE it.removed = 0`, nil,
		func(rows *sql.Rows) error {
			var (
				itemID, tagID    int64
				ns, name, source string
				parentID         sql.NullInt64
				parentName       sql.NullString
			)
			if err := rows.Scan(&itemID, &tagID, &ns, &name, &source, &parentID, &parentName); err != nil {
				return err
			}
			w := namespaceWeight[ns]
			if source == "folder" {
				w *= folderTagWeight
			}
			key := "tag:" + strconv.FormatInt(tagID, 10)
			lib.labels[key] = name
			add(itemID, key, w)
			if parentID.Valid {
				pkey := "tag:" + strconv.FormatInt(parentID.Int64, 10)
				lib.labels[pkey] = parentName.String
				add(itemID, pkey, w*parentTagWeight)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	err = eachRow(ctx, q, `
		SELECT ci.item_id, c.id, c.title FROM collection_items ci JOIN collections c ON c.id = ci.collection_id`, nil,
		func(rows *sql.Rows) error {
			var itemID, colID int64
			var title string
			if err := rows.Scan(&itemID, &colID, &title); err != nil {
				return err
			}
			key := "col:" + strconv.FormatInt(colID, 10)
			lib.labels[key] = title
			add(itemID, key, collectionWeight)
			return nil
		})
	if err != nil {
		return nil, err
	}

	// IDF: log(N/df). A feature every item has says nothing and is dropped;
	// one shared by two items in a thousand is worth about 6.
	df := map[string]int{}
	for _, v := range raw {
		for f := range v {
			df[f]++
		}
	}
	n := float64(len(raw))
	for _, it := range lib.items {
		v := raw[it.id]
		for f, w := range v {
			if idf := math.Log(n / float64(df[f])); idf > 0 {
				v[f] = w * idf
			} else {
				delete(v, f)
			}
		}
		it.vec = normalize(v)
	}
	return lib, nil
}

// Model adds a profile's watch history to the library.
func (lib *Library) Model(ctx context.Context, q db.Execer, profileID int64, now time.Time) (*Model, error) {
	m := &Model{byID: make(map[int64]*item, len(lib.items)), labels: lib.labels, now: now}
	m.items = make([]*item, len(lib.items))
	for i, f := range lib.items {
		m.items[i] = &item{features: f}
		m.byID[f.id] = m.items[i]
		if f.rating.Valid {
			m.items[i].engagement(sql.NullInt64{}, sql.NullBool{}, sql.NullInt64{}, sql.NullBool{}, sql.NullInt64{}, now)
		}
	}
	err := eachRow(ctx, q, `
		SELECT item_id, position_ms, completed, play_count, favorite, last_played_at
		  FROM play_state WHERE profile_id = ?`, []any{profileID},
		func(rows *sql.Rows) error {
			var (
				id                         int64
				pos, playCount, lastPlayed sql.NullInt64
				completed, favorite        sql.NullBool
			)
			if err := rows.Scan(&id, &pos, &completed, &playCount, &favorite, &lastPlayed); err != nil {
				return err
			}
			if it := m.byID[id]; it != nil {
				it.engagement(pos, completed, playCount, favorite, lastPlayed, now)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}

	m.taste = map[string]float64{}
	for _, it := range m.items {
		if it.weight == 0 {
			continue
		}
		for f, w := range it.vec {
			m.taste[f] += it.weight * w
		}
	}
	m.taste = normalize(m.taste)
	return m, nil
}

// engagement turns play state into a weight. Positive means "more like this".
func (it *item) engagement(pos sql.NullInt64, completed sql.NullBool, playCount sql.NullInt64,
	favorite sql.NullBool, lastPlayed sql.NullInt64, now time.Time) {
	var play float64
	switch {
	case completed.Bool:
		play = 1 + 0.3*math.Log(float64(max(playCount.Int64, 1)))
	case pos.Int64 > 0:
		frac := 0.3 // unknown duration: assume a fair try
		if it.duration > 0 {
			frac = min(float64(pos.Int64)/float64(it.duration), 1)
		}
		age := now.Sub(time.Unix(lastPlayed.Int64, 0))
		switch {
		case frac >= 0.1:
			play = 0.2 + 0.6*frac
		case age > 7*24*time.Hour:
			play = -0.15 // gave up in the first minutes and never came back
		default:
			play = 0.1 // just started; might still be watching
		}
	}
	it.playing = play >= 0.2 // watched a fair part of it: good anchor for "because you watched"
	if play > 0 && lastPlayed.Valid {
		age := now.Sub(time.Unix(lastPlayed.Int64, 0))
		play *= max(math.Pow(0.5, float64(age)/float64(playHalfLife)), minDecay)
	}
	w := play
	if favorite.Bool {
		w += 1.5
	}
	if it.rating.Valid {
		w += (float64(it.rating.Int64) - 5) / 2.5 // 10 -> +2, 5 -> 0, 1 -> -1.6
	}
	it.weight = w
	it.seen = completed.Bool || pos.Int64 > 0 || favorite.Bool || it.rating.Valid
	it.liked = favorite.Bool || it.rating.Int64 >= 8
	it.played = lastPlayed.Int64
}

// HasHistory reports whether the profile has any taste signal yet.
func (m *Model) HasHistory() bool { return len(m.taste) > 0 }

// ForYou ranks unwatched items by similarity to the profile's taste.
func (m *Model) ForYou(n int, exclude map[int64]bool) []Rec {
	if !m.HasHistory() {
		return nil
	}
	var pool []scored
	for _, c := range m.items {
		if c.seen || exclude[c.id] {
			continue
		}
		if s := dot(m.taste, c.vec) + m.freshness(c); s >= minScore {
			pool = append(pool, scored{c, s})
		}
	}
	picked := m.diversify(pool, n, 0.7)
	recs := make([]Rec, len(picked))
	for i, p := range picked {
		recs[i] = Rec{ItemID: p.it.id, Score: p.score, Reason: m.becauseOf(p.it)}
	}
	return recs
}

// Similar finds items like the given one. Unwatched items rank higher.
func (m *Model) Similar(id int64, n int, exclude map[int64]bool, unseenOnly bool) []Rec {
	src := m.byID[id]
	if src == nil {
		return nil
	}
	var pool []scored
	for _, c := range m.items {
		if c.id == id || exclude[c.id] || (unseenOnly && c.seen) {
			continue
		}
		s := dot(src.vec, c.vec)
		if c.seen {
			s *= 0.6
		}
		if s >= minScore {
			pool = append(pool, scored{c, s})
		}
	}
	picked := m.diversify(pool, n, 0.85)
	recs := make([]Rec, len(picked))
	for i, p := range picked {
		recs[i] = Rec{ItemID: p.it.id, Score: p.score, Reason: m.shared(src, p.it)}
	}
	return recs
}

// Shelves builds the home page rows. Items appear on one shelf only.
//
//	for-you            taste-based, needs some history
//	because-<id>       like one of the two most recently watched items
//	different          unwatched items far from the usual, shuffled daily
//	new                recently added, shown instead of all the above for a new profile
func (m *Model) Shelves(n int) []Shelf {
	shown := map[int64]bool{}
	var shelves []Shelf
	addShelf := func(id, title string, recs []Rec) {
		if len(recs) < 3 {
			return
		}
		for _, r := range recs {
			shown[r.ItemID] = true
		}
		shelves = append(shelves, Shelf{ID: id, Title: title, Items: recs})
	}

	if !m.HasHistory() {
		addShelf("new", "Recently added", m.recent(n))
		return shelves
	}
	addShelf("for-you", "Recommended for you", m.ForYou(n, shown))

	var anchors []*item
	for _, it := range m.items {
		if it.playing && it.weight > 0 {
			anchors = append(anchors, it)
		}
	}
	sort.Slice(anchors, func(i, j int) bool { return anchors[i].played > anchors[j].played })
	made := 0
	for _, a := range anchors {
		if made == 2 {
			break
		}
		recs := m.Similar(a.id, n, shown, true)
		if len(recs) >= 3 {
			made++
		}
		addShelf("because-"+strconv.FormatInt(a.id, 10), "Because you watched "+a.title, recs)
	}

	addShelf("different", "Something different", m.different(n, shown))
	if len(shelves) == 0 {
		addShelf("new", "Recently added", m.recent(n)) // history so far is all dislikes
	}
	return shelves
}

// recent is unwatched items, newest first.
func (m *Model) recent(n int) []Rec {
	var list []*item
	for _, it := range m.items {
		if !it.seen {
			list = append(list, it)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].addedAt != list[j].addedAt {
			return list[i].addedAt > list[j].addedAt
		}
		return list[i].id > list[j].id
	})
	recs := make([]Rec, 0, min(n, len(list)))
	for _, it := range list[:min(n, len(list))] {
		recs = append(recs, Rec{ItemID: it.id})
	}
	return recs
}

// different picks unwatched items in the bottom half of the taste ranking,
// in an order that changes daily, skipping near-duplicates of earlier picks.
// It's how something outside the usual gets a chance.
func (m *Model) different(n int, exclude map[int64]bool) []Rec {
	var pool []scored
	for _, it := range m.items {
		if !it.seen && !exclude[it.id] {
			pool = append(pool, scored{it, dot(m.taste, it.vec)})
		}
	}
	if len(pool) < 2*n {
		return nil // small library: these would just be leftovers
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].score < pool[j].score })
	pool = pool[:len(pool)/2]
	// Reuse score as the shuffle key: a hash of today's date and the item.
	day := m.now.Format("2006-01-02")
	for i := range pool {
		h := fnv.New64a()
		fmt.Fprintf(h, "%s/%d", day, pool[i].it.id)
		pool[i].score = float64(h.Sum64())
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].score < pool[j].score })

	var picked []*item
	for _, c := range pool {
		if len(picked) == n {
			break
		}
		dup := false
		for _, p := range picked {
			if dot(p.vec, c.it.vec) > 0.5 {
				dup = true
				break
			}
		}
		if !dup {
			picked = append(picked, c.it)
		}
	}
	recs := make([]Rec, len(picked))
	for i, it := range picked {
		recs[i] = Rec{ItemID: it.id, Reason: "Outside your usual"}
	}
	return recs
}

type scored struct {
	it    *item
	score float64
}

// diversify picks n items by maximal marginal relevance: each pick
// maximizes lambda*score - (1-lambda)*similarity to what's already picked.
func (m *Model) diversify(pool []scored, n int, lambda float64) []scored {
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].score != pool[j].score {
			return pool[i].score > pool[j].score
		}
		return pool[i].it.id < pool[j].it.id
	})
	pool = pool[:min(len(pool), 5*n)]
	var picked []scored
	for len(picked) < n && len(pool) > 0 {
		best, bestVal := 0, math.Inf(-1)
		for i, c := range pool {
			var redundancy float64
			for _, p := range picked {
				redundancy = max(redundancy, dot(c.it.vec, p.it.vec))
			}
			if v := lambda*c.score - (1-lambda)*redundancy; v > bestVal {
				best, bestVal = i, v
			}
		}
		picked = append(picked, pool[best])
		pool = append(pool[:best], pool[best+1:]...)
	}
	return picked
}

// becauseOf names the history item that contributes most to a suggestion.
func (m *Model) becauseOf(c *item) string {
	var best *item
	var bestVal float64
	for _, h := range m.items {
		if h.weight <= 0 {
			continue
		}
		if v := h.weight * dot(h.vec, c.vec); v > bestVal {
			best, bestVal = h, v
		}
	}
	switch {
	case best == nil:
		return ""
	case best.liked:
		return "Because you liked " + best.title
	default:
		return "Because you watched " + best.title
	}
}

// shared names the two features that make two items most alike.
func (m *Model) shared(a, b *item) string {
	type contrib struct {
		label string
		v     float64
	}
	var cs []contrib
	for f, w := range a.vec {
		if label, ok := m.labels[f]; ok {
			if v := w * b.vec[f]; v > 0 {
				cs = append(cs, contrib{label, v})
			}
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].v != cs[j].v {
			return cs[i].v > cs[j].v
		}
		return cs[i].label < cs[j].label
	})
	var names []string
	for _, c := range cs {
		if len(names) == 2 {
			break
		}
		if len(names) == 1 && names[0] == c.label {
			continue // a tag and its parent can share a name
		}
		names = append(names, c.label)
	}
	return strings.Join(names, " · ")
}

func (m *Model) freshness(it *item) float64 {
	age := m.now.Sub(time.Unix(it.addedAt, 0))
	if age < 0 {
		age = 0
	}
	return newBoost * math.Pow(0.5, float64(age)/float64(newHalfLife))
}

func normalize(v map[string]float64) map[string]float64 {
	var sum float64
	for _, w := range v {
		sum += w * w
	}
	if sum == 0 {
		return map[string]float64{}
	}
	norm := math.Sqrt(sum)
	for f := range v {
		v[f] /= norm
	}
	return v
}

func dot(a, b map[string]float64) float64 {
	if len(b) < len(a) {
		a, b = b, a
	}
	var s float64
	for f, w := range a {
		s += w * b[f]
	}
	return s
}

func eachRow(ctx context.Context, q db.Execer, query string, args []any, fn func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
