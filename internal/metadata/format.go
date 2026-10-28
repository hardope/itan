// Package metadata exports and imports the metadata people create (titles,
// tags, ratings, collections, watch history) so it survives reinstalls and
// moves between devices. The format is specified in docs/export-format.md.
package metadata

import "time"

const (
	libraryFormat = "itan-library"
	sidecarFormat = "itan-sidecar"
	formatVersion = "1.0"
	formatMajor   = "1"

	// SidecarExt is appended to a video's file name: "x.mkv.meta.json".
	SidecarExt = ".meta.json"
)

// Library is a whole-library export file.
type Library struct {
	Format      string        `json:"format"`
	Version     string        `json:"version"`
	ExportedAt  time.Time     `json:"exported_at"`
	Generator   string        `json:"generator,omitempty"`
	Scope       Scope         `json:"scope"`
	Libraries   []LibraryInfo `json:"libraries"`
	Tags        []TagDef      `json:"tags"`
	Items       []Item        `json:"items"`
	Collections []Collection  `json:"collections"`
	Profiles    []Profile     `json:"profiles,omitempty"`
}

type Scope struct {
	IncludeHistory bool     `json:"include_history"`
	Libraries      []string `json:"libraries"`
}

type LibraryInfo struct {
	Name        string `json:"name"`
	DefaultKind string `json:"default_kind"`
	FolderTags  bool   `json:"folder_tags"`
}

type TagDef struct {
	Ref    string  `json:"ref"` // "namespace:slug"
	Name   string  `json:"name"`
	Parent *string `json:"parent"`
	Color  *string `json:"color"`
}

type Item struct {
	Hash        string            `json:"hash"`
	Size        int64             `json:"size"`
	Paths       []PathHint        `json:"paths,omitempty"`
	Fields      map[string]Field  `json:"fields"`
	Tags        []ItemTag         `json:"tags"`
	ExternalIDs map[string]string `json:"external_ids,omitempty"`
	AddedAt     *time.Time        `json:"added_at,omitempty"`
	UpdatedAt   *time.Time        `json:"updated_at,omitempty"`
	Technical   *Technical        `json:"technical,omitempty"`
}

type PathHint struct {
	Library string `json:"library"`
	Path    string `json:"path"`
}

type Field struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

type ItemTag struct {
	Ref     string `json:"ref"`
	Source  string `json:"source"`
	Removed bool   `json:"removed,omitempty"`
}

// Technical is informational only; importers never use it.
type Technical struct {
	DurationMS *int64  `json:"duration_ms,omitempty"`
	Width      *int    `json:"width,omitempty"`
	Height     *int    `json:"height,omitempty"`
	VideoCodec *string `json:"video_codec,omitempty"`
}

type Collection struct {
	UID         string           `json:"uid"`
	Kind        string           `json:"kind"`
	Title       string           `json:"title"`
	Description *string          `json:"description,omitempty"`
	Parent      *string          `json:"parent"`
	Position    *int             `json:"position"`
	Items       []CollectionItem `json:"items"`
}

type CollectionItem struct {
	Hash     string `json:"hash"`
	Position *int   `json:"position"`
}

type Profile struct {
	UID   string      `json:"uid"`
	Name  string      `json:"name"`
	State []PlayState `json:"state"`
}

type PlayState struct {
	Hash         string     `json:"hash"`
	PositionMS   int64      `json:"position_ms"`
	Completed    bool       `json:"completed"`
	PlayCount    int        `json:"play_count"`
	Favorite     bool       `json:"favorite"`
	LastPlayedAt *time.Time `json:"last_played_at"`
}

// Sidecar is the per-video file written next to a video.
type Sidecar struct {
	Format  string   `json:"format"`
	Version string   `json:"version"`
	Item    Item     `json:"item"`
	TagDefs []TagDef `json:"tag_defs"`
}

// pendingPayload is what pending_imports stores for an item whose file
// isn't here yet: everything needed to apply it later on its own.
type pendingPayload struct {
	Item        Item                `json:"item"`
	TagDefs     []TagDef            `json:"tag_defs"`
	Collections []pendingMembership `json:"collections,omitempty"`
	State       []pendingState      `json:"state,omitempty"`
}

type pendingMembership struct {
	UID      string `json:"uid"`
	Position *int   `json:"position"`
}

type pendingState struct {
	ProfileUID  string    `json:"profile_uid"`
	ProfileName string    `json:"profile_name"`
	State       PlayState `json:"state"`
}

// Exportable user-editable fields (the items table columns of the same name).
var exportFields = []string{
	"title", "sort_title", "original_title", "description",
	"year", "release_date", "kind", "rating", "language",
}

var sourcePriority = map[string]int{
	"filename": 1, "folder": 2, "embedded": 3, "scraper": 4, "user": 5,
}
