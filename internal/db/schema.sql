-- Itan database schema (SQLite 3.35+, FTS5 required)
--
-- Design rules:
--   1. A media item is identified by its content hash, never by its path.
--      Paths are just "where we last saw it" and can change freely.
--   2. Every user-editable field records where its value came from.
--      Rescans may overwrite auto values but NEVER values with source = 'user'.
--   3. Technical metadata (ffprobe) is derived and disposable; user metadata is precious.
--      Only user metadata goes into exports.
--
-- Schema version is tracked with PRAGMA user_version.
-- Per-connection pragmas (foreign_keys, journal_mode, busy_timeout) are set
-- in the connection string, see db.go.

PRAGMA user_version = 4;

------------------------------------------------------------------------
-- Libraries: root folders the user configured
------------------------------------------------------------------------
CREATE TABLE libraries (
    id              INTEGER PRIMARY KEY,
    name            TEXT    NOT NULL,
    root_path       TEXT    NOT NULL UNIQUE,      -- absolute path on this machine
    -- Default kind for new items found here (user can override per item)
    default_kind    TEXT    NOT NULL DEFAULT 'video',
    -- Turn folder names into tags: /Docs/Nature/x.mkv -> tags "docs", "nature"
    folder_tags     INTEGER NOT NULL DEFAULT 1,
    -- Keep the library's edits in <root>/.itan.json (needs write access).
    -- The name is from when this wrote one .meta.json sidecar per video.
    write_sidecars  INTEGER NOT NULL DEFAULT 0,
    enabled         INTEGER NOT NULL DEFAULT 1,
    last_scan_at    INTEGER,                      -- unix seconds
    created_at      INTEGER NOT NULL DEFAULT (unixepoch())
);

------------------------------------------------------------------------
-- Items: one row per unique piece of content (unique content hash)
------------------------------------------------------------------------
CREATE TABLE items (
    id              INTEGER PRIMARY KEY,
    -- Portable identity, see docs/export-format.md "Content hash".
    -- Format: "ph1:<16 hex chars>"
    content_hash    TEXT    NOT NULL UNIQUE,
    size_bytes      INTEGER NOT NULL,

    -- User-editable fields (effective values; provenance in item_field_sources)
    title           TEXT    NOT NULL,
    sort_title      TEXT,
    original_title  TEXT,
    description     TEXT,
    year            INTEGER,
    release_date    TEXT,                         -- ISO 8601 date, if known
    kind            TEXT    NOT NULL DEFAULT 'video'
                    CHECK (kind IN ('video','movie','episode','music_video',
                                    'concert','documentary','lecture','home_video','other')),
    rating          INTEGER CHECK (rating BETWEEN 0 AND 10),  -- user's own rating
    language        TEXT,                         -- BCP 47, e.g. 'en', 'yo'
    external_ids    TEXT,                         -- JSON: {"tmdb":"603","imdb":"tt0133093"}

    -- Technical metadata from ffprobe (derived; safe to recompute)
    container       TEXT,                         -- 'matroska', 'mp4', ...
    duration_ms     INTEGER,
    bitrate         INTEGER,                      -- bits/sec, overall
    width           INTEGER,
    height          INTEGER,
    video_codec     TEXT,                         -- 'h264', 'hevc', 'av1', ...
    video_profile   TEXT,
    frame_rate      REAL,
    hdr             TEXT,                         -- NULL, 'hdr10', 'hlg', 'dolby_vision'
    quality_label   TEXT,                         -- computed: '480p','720p','1080p','4k'
    -- How the browser can play it, decided at probe time:
    --   direct = serve bytes as-is, remux = ffmpeg -c copy, transcode = re-encode
    playback_mode   TEXT    CHECK (playback_mode IN ('direct','remux','transcode')),
    probed_at       INTEGER,
    probe_error     TEXT,

    -- Media assets (paths inside the config dir, regenerated on demand)
    poster_path     TEXT,
    thumbnail_path  TEXT,
    thumbnail_at_ms INTEGER,                      -- frame someone chose; NULL: 10% in

    added_at        INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at      INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX items_kind_idx     ON items(kind);
CREATE INDEX items_year_idx     ON items(year);
CREATE INDEX items_quality_idx  ON items(quality_label);
CREATE INDEX items_added_idx    ON items(added_at);

-- Provenance of each user-editable field. Missing row = 'filename' default.
-- Priority when merging (high -> low): user > scraper > embedded > folder > filename
CREATE TABLE item_field_sources (
    item_id     INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    field       TEXT    NOT NULL,                 -- 'title', 'year', 'description', ...
    source      TEXT    NOT NULL
                CHECK (source IN ('filename','folder','embedded','scraper','user')),
    updated_at  INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (item_id, field)
) WITHOUT ROWID;

------------------------------------------------------------------------
-- Files: where an item lives on disk. One item can have several files
-- (exact duplicates on different drives); one file always has one item.
------------------------------------------------------------------------
CREATE TABLE files (
    id              INTEGER PRIMARY KEY,
    item_id         INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    library_id      INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    rel_path        TEXT    NOT NULL,             -- relative to libraries.root_path
    size_bytes      INTEGER NOT NULL,
    mtime           INTEGER NOT NULL,             -- skip re-hashing if size+mtime unchanged
    first_seen_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    last_seen_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    missing_since   INTEGER,                      -- set when a scan can't find it
    UNIQUE (library_id, rel_path)
);

CREATE INDEX files_item_idx ON files(item_id);

-- Audio / subtitle / video tracks, for track pickers and playback decisions
CREATE TABLE streams (
    id              INTEGER PRIMARY KEY,
    item_id         INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    stream_index    INTEGER,                      -- ffprobe index; NULL for external subs
    type            TEXT    NOT NULL CHECK (type IN ('video','audio','subtitle')),
    codec           TEXT,
    language        TEXT,
    title           TEXT,
    channels        INTEGER,                      -- audio only
    is_default      INTEGER NOT NULL DEFAULT 0,
    is_forced       INTEGER NOT NULL DEFAULT 0,
    external_path   TEXT                          -- sidecar .srt/.vtt, relative to library root
);

CREATE INDEX streams_item_idx ON streams(item_id);

------------------------------------------------------------------------
-- Tags: genres and free tags share one table, separated by namespace.
-- Hierarchy is optional: "jazz" can have parent "music".
------------------------------------------------------------------------
CREATE TABLE tags (
    id          INTEGER PRIMARY KEY,
    namespace   TEXT    NOT NULL DEFAULT 'tag'
                CHECK (namespace IN ('genre','tag','person','mood','source')),
    slug        TEXT    NOT NULL,                 -- lowercase, url-safe: 'live-concert'
    name        TEXT    NOT NULL,                 -- display: 'Live Concert'
    parent_id   INTEGER REFERENCES tags(id) ON DELETE SET NULL,
    color       TEXT,                             -- '#ff8800', for UI chips
    created_at  INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (namespace, slug)
);

CREATE TABLE item_tags (
    item_id     INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    tag_id      INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    source      TEXT    NOT NULL DEFAULT 'user'
                CHECK (source IN ('filename','folder','embedded','scraper','user')),
    -- Tombstone: the user removed an auto tag. Keep the row so rescans
    -- don't add it back. Queries must filter on removed = 0.
    removed     INTEGER NOT NULL DEFAULT 0,
    added_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (item_id, tag_id)
) WITHOUT ROWID;

CREATE INDEX item_tags_tag_idx ON item_tags(tag_id, item_id) WHERE removed = 0;

------------------------------------------------------------------------
-- Collections: series, seasons, playlists, user groupings.
-- Nestable (series -> season -> episodes) and ordered.
------------------------------------------------------------------------
CREATE TABLE collections (
    id          INTEGER PRIMARY KEY,
    uid         TEXT    NOT NULL UNIQUE,          -- random UUID; stable across exports
    kind        TEXT    NOT NULL CHECK (kind IN ('series','season','playlist','set')),
    title       TEXT    NOT NULL,
    description TEXT,
    parent_id   INTEGER REFERENCES collections(id) ON DELETE CASCADE,
    position    INTEGER,                          -- order within parent (season number)
    poster_path TEXT,
    -- Series only: slug of the show name, so episodes found by later scans
    -- ("Show.S02E01.mkv") join the series even after it was renamed.
    match_key   TEXT,
    created_at  INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at  INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX collections_match_idx ON collections(kind, match_key);
CREATE INDEX collections_parent_idx ON collections(parent_id);

CREATE TABLE collection_items (
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    item_id       INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    position      INTEGER,                        -- episode number / playlist order
    PRIMARY KEY (collection_id, item_id)
) WITHOUT ROWID;

CREATE INDEX collection_items_item_idx ON collection_items(item_id);
-- An item's series membership is a user-editable field: item_field_sources
-- has field 'series' with source 'user' once someone set or removed it, and
-- automatic grouping then leaves the item alone.

------------------------------------------------------------------------
-- Profiles and watch history (no passwords by default: LAN household use)
------------------------------------------------------------------------
CREATE TABLE profiles (
    id          INTEGER PRIMARY KEY,
    uid         TEXT    NOT NULL UNIQUE,          -- random UUID
    name        TEXT    NOT NULL UNIQUE,
    avatar      TEXT,
    pin_hash    TEXT,                             -- optional PIN, argon2id
    is_admin    INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Current state per profile/item: resume point, seen status
CREATE TABLE play_state (
    profile_id      INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    item_id         INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    position_ms     INTEGER NOT NULL DEFAULT 0,
    completed       INTEGER NOT NULL DEFAULT 0,   -- watched past ~90%
    play_count      INTEGER NOT NULL DEFAULT 0,
    favorite        INTEGER NOT NULL DEFAULT 0,
    last_played_at  INTEGER,
    PRIMARY KEY (profile_id, item_id)
) WITHOUT ROWID;

-- Append-only log feeding recommendations (time decay, session patterns)
CREATE TABLE play_events (
    id          INTEGER PRIMARY KEY,
    profile_id  INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    item_id     INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    started_at  INTEGER NOT NULL,
    watched_ms  INTEGER NOT NULL DEFAULT 0,
    completed   INTEGER NOT NULL DEFAULT 0,
    device      TEXT                              -- user agent summary
);

CREATE INDEX play_events_profile_idx ON play_events(profile_id, started_at);

------------------------------------------------------------------------
-- Full-text search over title, description and tag names
------------------------------------------------------------------------
CREATE VIRTUAL TABLE items_fts USING fts5(
    title, original_title, description, tags,
    content = '',                                 -- contentless; we only need rowids
    contentless_delete = 1,                       -- SQLite 3.43+
    tokenize = 'unicode61 remove_diacritics 2'
);
-- items_fts.rowid = items.id. The app rewrites the row whenever an item or
-- its tags change (one place in code instead of triggers on 3 tables).

------------------------------------------------------------------------
-- Imported metadata for files not found on this machine yet (the drive
-- isn't mounted, or the files haven't been copied over). Applied and
-- deleted when a matching file is scanned. See docs/export-format.md.
------------------------------------------------------------------------
CREATE TABLE pending_imports (
    id              INTEGER PRIMARY KEY,
    content_hash    TEXT    NOT NULL UNIQUE,
    size_bytes      INTEGER NOT NULL,
    -- First path hint, for matching re-encoded or moved files
    library_name    TEXT,
    rel_path        TEXT,
    file_name       TEXT,
    payload         TEXT    NOT NULL,             -- JSON, see metadata.pendingPayload
    created_at      INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX pending_imports_path_idx ON pending_imports(library_name, rel_path);
CREATE INDEX pending_imports_name_idx ON pending_imports(file_name, size_bytes);

------------------------------------------------------------------------
-- Key/value settings (server name, port, scraper keys, transcode prefs)
------------------------------------------------------------------------
CREATE TABLE settings (
    key     TEXT PRIMARY KEY,
    value   TEXT NOT NULL                         -- JSON-encoded
) WITHOUT ROWID;

------------------------------------------------------------------------
-- Devices unlocked for editing with the edit PIN (settings 'edit_pin').
-- The cookie holds a random token; only its SHA-256 is stored.
------------------------------------------------------------------------
CREATE TABLE edit_sessions (
    token_hash      TEXT    PRIMARY KEY,
    device          TEXT,                         -- user agent summary
    created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
    last_used_at    INTEGER NOT NULL DEFAULT (unixepoch())
) WITHOUT ROWID;
