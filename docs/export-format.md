# Metadata export format

Itan metadata can leave the database in three ways:

| Format | File | Use |
|---|---|---|
| **Library export** | `itan-export-<date>.json` (optionally `.json.gz`) | Back up or move a whole library to a new install or device |
| **Folder file** | `.itan.json` at a library folder's root | Edits travel with the drive and stay in sync between installs |
| **Sidecar** (older versions) | `<video file>.meta.json`, next to the video | Read for compatibility; no longer written |

All use the same **item object**, so one parser handles them all.

## Principles

1. **Identity is the content hash, not the path.** Renames, moves and different
   mount points don't matter. Paths are included only as fallback hints.
2. **Only data a person created is exported.** Titles, tags, ratings,
   collections and watch history are exported. ffprobe output, thumbnails and
   database IDs are not, because the new install recomputes them.
3. **Provenance travels with the data.** Every field says whether a user set it,
   so importing never lets an auto-detected guess overwrite a user's edit.
4. **Versioned.** `format` and `version` come first. Readers reject a
   major version they don't know and ignore fields they don't recognise.

## Content hash (`ph1`)

```
ph1 = xxh3_64( le_uint64(size) || first 1 MiB || last 1 MiB )
```

- If the file is 2 MiB or smaller, hash `le_uint64(size) || whole file`.
- Written as `"ph1:" + 16 lowercase hex chars`, e.g. `ph1:9f2c4e1ab03d7781`.
- Hashing takes about 2 MiB of reads per file, so it stays fast on HDDs and
  network shares. The `size` field is always stored and compared alongside it.
- The `ph1` prefix allows switching to a different scheme later (`ph2`, or a
  full sha256) without breaking older exports.

**Limitation:** re-encoding or remuxing a file changes its hash. The import
falls back to matching on path (see Matching below).

## Library export

```jsonc
{
  "format": "itan-library",
  "version": "1.0",
  "exported_at": "2026-10-27T14:03:11Z",
  "generator": "itan 0.1.0",
  "scope": {
    "include_history": true,       // false => profiles/history omitted
    "libraries": ["Documentaries"] // which libraries this export covers
  },

  "libraries": [
    { "name": "Documentaries", "default_kind": "documentary", "folder_tags": true }
  ],

  "tags": [ /* tag objects: the full vocabulary, including unused tags */ ],
  "items": [ /* item objects */ ],
  "collections": [ /* collection objects */ ],
  "profiles": [ /* profile objects, only if include_history */ ]
}
```

A full example is in [examples/library-export.json](examples/library-export.json).

### Tag object

```jsonc
{
  "ref": "genre:music",          // namespace:slug, the tag's identity everywhere
  "name": "Music",
  "parent": null,                // or another ref, e.g. "genre:music" for "genre:jazz"
  "color": "#7c5cff"
}
```

Namespaces: `genre`, `tag`, `person`, `mood`, `source`.

### Item object

```jsonc
{
  "hash": "ph1:9f2c4e1ab03d7781",
  "size": 1843204096,
  "paths": [                                   // hints only, for fallback matching
    { "library": "Documentaries", "path": "Nature/Planet Earth II - Islands (2016).mkv" }
  ],

  "fields": {
    "title":       { "value": "Planet Earth II: Islands", "source": "user" },
    "year":        { "value": 2016, "source": "filename" },
    "description": { "value": "Island wildlife...", "source": "scraper" },
    "kind":        { "value": "documentary", "source": "folder" },
    "rating":      { "value": 9, "source": "user" }
  },

  "tags": [
    { "ref": "genre:documentary", "source": "folder" },
    { "ref": "tag:nature",        "source": "folder" },
    { "ref": "tag:4k",            "source": "filename", "removed": true }
  ],

  "external_ids": { "tmdb": "68595" },
  "added_at": "2026-03-02T19:22:40Z",
  "updated_at": "2026-10-20T08:11:05Z",

  // Optional, informational only. Importers never trust this over ffprobe;
  // it lets someone browsing an export see "1080p HEVC" without the file.
  "technical": { "duration_ms": 2941000, "width": 1920, "height": 1080, "video_codec": "hevc" }
}
```

Exportable `fields` keys: `title`, `sort_title`, `original_title`,
`description`, `year`, `release_date`, `kind`, `rating`, `language`.

`source` is one of `filename`, `folder`, `embedded`, `scraper` or `user`.
Exporters may leave out fields that are empty or that the filename can
derive again, to keep files small.

`removed: true` on a tag is a **tombstone**. It means the user deleted an
auto-detected tag. It must be exported, or the next scan on the new install
will add the tag back.

### Collection object

```jsonc
{
  "uid": "c6a1d6f0-3b5e-4b8e-9d0a-2f1f6f1f9b21",
  "kind": "series",               // series | season | playlist | set
  "title": "Planet Earth II",
  "parent": null,                 // another collection's uid
  "position": null,
  "items": [                      // ordered; position = episode/playlist order
    { "hash": "ph1:9f2c4e1ab03d7781", "position": 1 }
  ]
}
```

### Profile object (optional)

```jsonc
{
  "uid": "0b9e...",
  "name": "Ope",
  "state": [
    { "hash": "ph1:9f2c4e1ab03d7781", "position_ms": 1203000, "completed": false,
      "play_count": 1, "favorite": true, "last_played_at": "2026-10-20T21:40:00Z" }
  ]
}
```

PIN hashes and the raw `play_events` log are **not** exported. The log is
summarised into `state`.

## Folder file

`.itan.json`, a hidden file at the root of a library folder. It is a
library export (`"format": "itan-library"`, importable by hand like any
export) with these differences:

- Only the videos in that folder that have edits (user-set fields, added tags,
  removed tags). No watch history or profiles: a shared drive shouldn't tell
  everyone what you watched.
- `paths` are relative to the folder. The library name in them is ignored
  when reading.
- `exported_at` is the time of the newest edit, not of writing, so a library
  with no new edits produces an identical file and it isn't rewritten.

Rules:
- Written when the library has `write_sidecars` enabled ("Keep edits in this
  folder"), a couple of seconds after edits, and after imports and scans.
  Written atomically (temporary file, then rename).
- Read during each scan if its size or modification time changed since this
  install last read or wrote it, whether or not the library writes one. It is
  imported with the merge rules below, matching within that library only:
  hash and size, then path inside the folder, then file name and size. So a
  drive can move between installs where the folder has different names, and
  edits made on each come together.
- Adding a folder that already has a folder file turns on writing it, so it
  stays in sync.
- A file that can't be read (damaged, or a newer major version) is reported
  and never overwritten.
- When a library starts keeping a folder file, per-video sidecars in it are
  imported first, then deleted once the folder file is written. Files ending
  in `.meta.json` that aren't Itan sidecars are left alone.

## Sidecar (read only)

Earlier versions wrote one sidecar per edited video. They are still applied to
newly scanned files, and folded into the folder file as described above.

`Planet Earth II - Islands (2016).mkv.meta.json`:

```jsonc
{
  "format": "itan-sidecar",
  "version": "1.0",
  "item": { /* one item object, without "paths" */ },
  "tag_defs": [ /* tag objects for every ref used, so the file is self-contained */ ]
}
```

Rules:
- During a scan, a sidecar acts like an import of that one item, for files
  seen for the first time.
- The sidecar's `item.hash` must match the file. If it doesn't (the file was
  re-encoded), the sidecar is still applied, because it sits next to the file,
  and the hash inside it is updated.

## Import: matching and merging

**Matching** each exported item to a local item, in order:
1. `hash` + `size` match exactly.
2. Same library name and same relative `path` (catches re-encoded files).
3. Same file name and `size` anywhere (catches files moved between libraries).
4. Otherwise, keep it as **pending**: stored, and applied automatically if a
   matching file turns up in a later scan. (So the metadata can be imported
   before the drive is mounted.)

**Merging** each field:

| Local source | Incoming source | Result |
|---|---|---|
| any non-user | higher or equal priority | take incoming |
| `user` | non-user | keep local |
| `user` | `user` | newer `updated_at` wins (or ask, if the UI offers "review conflicts") |

Priority: `user > scraper > embedded > folder > filename`.

Tags merge as a union. A tombstone from either side wins over an
auto-detected tag, and a `user`-added tag wins over a tombstone.
Collections merge by `uid`, and profiles by `uid` or else by `name`.

Import modes: **merge** (default, the rules above), **replace** (incoming wins
every conflict), **dry run** (report what would change, write nothing).
