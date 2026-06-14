package library

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS libraries (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL,
  root_path       TEXT NOT NULL,
  created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
  last_scan       DATETIME,
  cover_photo     TEXT,
  cover_set_by    TEXT,
  cover_set_at    DATETIME,
  background_photo TEXT,
  sort_title      TEXT,
  summary         TEXT
);

CREATE TABLE IF NOT EXISTS library_access (
  library_id    TEXT REFERENCES libraries(id) ON DELETE CASCADE,
  plex_username TEXT NOT NULL,
  PRIMARY KEY (library_id, plex_username)
);

CREATE TABLE IF NOT EXISTS users (
  plex_username TEXT PRIMARY KEY,
  email         TEXT,
  is_admin      INTEGER NOT NULL DEFAULT 0,
  created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
  last_seen     DATETIME
);

-- nodes is a recursive tree: every folder under a library root is a node. A
-- node can hold sub-nodes (collection view) and/or photos (album view), and may
-- be both at once. parent_id NULL means a top-level node directly under the
-- library root; a library itself is the implicit root.
CREATE TABLE IF NOT EXISTS nodes (
  id            TEXT PRIMARY KEY,
  library_id    TEXT REFERENCES libraries(id) ON DELETE CASCADE,
  parent_id     TEXT REFERENCES nodes(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  display_name  TEXT,
  fs_path       TEXT NOT NULL,
  depth         INTEGER DEFAULT 0,
  photo_count   INTEGER DEFAULT 0,
  has_children  INTEGER DEFAULT 0,
  cover_photo   TEXT,
  cover_set_by  TEXT,
  cover_set_at  DATETIME,
  background_photo TEXT,
  sort_title    TEXT,
  summary       TEXT,
  content_rating TEXT,
  year          TEXT,
  studio        TEXT,
  scanned_at    DATETIME
);

CREATE TABLE IF NOT EXISTS album_favorites (
  plex_username TEXT NOT NULL,
  node_id       TEXT REFERENCES nodes(id) ON DELETE CASCADE,
  created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (plex_username, node_id)
);

CREATE TABLE IF NOT EXISTS album_views (
  plex_username TEXT NOT NULL,
  node_id       TEXT REFERENCES nodes(id) ON DELETE CASCADE,
  viewed_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (plex_username, node_id)
);

-- Per-user UI preferences, stored as a single JSON blob so new settings can be
-- added without schema changes.
CREATE TABLE IF NOT EXISTS user_preferences (
  plex_username TEXT PRIMARY KEY,
  prefs         TEXT NOT NULL DEFAULT '{}',
  updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- User-owned playlists: an ordered, hand-curated set of photos (independent of
-- the folder tree). Playlists are private to their owner. last_used_at is
-- bumped whenever the owner adds to the playlist so the "Add to playlist" menu
-- can surface the most recently used ones first.
CREATE TABLE IF NOT EXISTS playlists (
  id            TEXT PRIMARY KEY,
  owner         TEXT NOT NULL,
  name          TEXT NOT NULL,
  cover_photo   TEXT,
  created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
  last_used_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- A single photo entry in a playlist. photo_path is the same URL token used for
-- thumb/photo requests (access is re-checked per request against the owner's
-- accessible library roots). The UNIQUE constraint dedupes re-adds; position
-- keeps the user's ordering.
CREATE TABLE IF NOT EXISTS playlist_items (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  playlist_id TEXT NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
  photo_path  TEXT NOT NULL,
  photo_name  TEXT NOT NULL DEFAULT '',
  position    INTEGER NOT NULL DEFAULT 0,
  added_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (playlist_id, photo_path)
);

CREATE INDEX IF NOT EXISTS idx_playlists_owner ON playlists(owner, last_used_at);
CREATE INDEX IF NOT EXISTS idx_playlist_items_pl ON playlist_items(playlist_id, position);

-- Samsung Frame TVs configured by an admin. token is captured on the first
-- successful connection and reused to skip the "allow this device?" prompt.
-- matte + interval_s are this TV's defaults for the playlist swap loop.
CREATE TABLE IF NOT EXISTS tvs (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  ip           TEXT NOT NULL,
  token        TEXT NOT NULL DEFAULT '',
  matte        TEXT NOT NULL DEFAULT 'none',
  interval_s   INTEGER NOT NULL DEFAULT 3600,
  display_mode TEXT NOT NULL DEFAULT 'blur-fill',
  bg_color     TEXT NOT NULL DEFAULT '#000000',
  border_pct   INTEGER NOT NULL DEFAULT 0,
  smart_fill   INTEGER NOT NULL DEFAULT 0,
  caption_fields TEXT NOT NULL DEFAULT '',
  play_order   TEXT NOT NULL DEFAULT 'sequential',
  photo_filter TEXT NOT NULL DEFAULT 'none',
  created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Runtime state of the swap loop for each TV (one row per TV). Only the
-- persistent fields needed to resume after a restart live here; transient
-- fields (current step, seconds-until-next, last error) are kept in memory by
-- the player. status is 'stopped' | 'playing' | 'error'.
CREATE TABLE IF NOT EXISTS tv_player_state (
  tv_id           TEXT PRIMARY KEY REFERENCES tvs(id) ON DELETE CASCADE,
  owner           TEXT NOT NULL DEFAULT '',
  playlist_id     TEXT,
  status          TEXT NOT NULL DEFAULT 'stopped',
  position        INTEGER NOT NULL DEFAULT 0,
  current_path    TEXT,
  current_content TEXT,
  last_swap_at    DATETIME,
  deck            TEXT NOT NULL DEFAULT '',
  updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Per-photo metadata extracted at scan time from EXIF and Google Takeout
-- sidecar JSON. photo_path is the same URL token used for thumb/photo requests
-- (see AbsToURLPath). file_mtime/file_size let a rescan skip unchanged files.
-- Multi-valued person tags live in photo_people. orientation is derived from
-- width/height ("portrait" | "landscape" | "square").
CREATE TABLE IF NOT EXISTS photo_meta (
  photo_path    TEXT PRIMARY KEY,
  library_id    TEXT REFERENCES libraries(id) ON DELETE CASCADE,
  taken_at      DATETIME,
  year          INTEGER,
  lat           REAL,
  lon           REAL,
  has_gps       INTEGER NOT NULL DEFAULT 0,
  place_city     TEXT,
  place_province TEXT,
  place_country  TEXT,
  camera         TEXT,
  lens           TEXT,
  exposure       TEXT,
  aperture       TEXT,
  iso            TEXT,
  focal_length   TEXT,
  width         INTEGER,
  height        INTEGER,
  orientation   TEXT,
  has_sidecar   INTEGER NOT NULL DEFAULT 0,
  file_mtime    INTEGER,
  file_size     INTEGER,
  meta_version  INTEGER NOT NULL DEFAULT 0,
  indexed_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Person tags for a photo (Google Takeout people[].name). One row per name.
CREATE TABLE IF NOT EXISTS photo_people (
  photo_path TEXT NOT NULL,
  name       TEXT NOT NULL,
  PRIMARY KEY (photo_path, name)
);

-- Global app settings as simple key/value rows (e.g. auto-scan interval).
CREATE TABLE IF NOT EXISTS settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Persistent log of failed library scans (one row per failed scan run), so
-- admins can review background/auto-scan failures that would otherwise only
-- appear on stdout. Capped to the most recent N rows by the store on insert.
CREATE TABLE IF NOT EXISTS scan_errors (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id   TEXT,
  library_name TEXT,
  source       TEXT NOT NULL,
  message      TEXT NOT NULL,
  occurred_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Persistent log of background jobs (library scans, thumbnail regeneration).
-- One row per job run with live progress fields and a final status, so the
-- admin Jobs page can show the active job plus a history of recent runs.
-- Capped to the most recent N finished rows by the store on insert.
CREATE TABLE IF NOT EXISTS jobs (
  id           TEXT PRIMARY KEY,
  type         TEXT NOT NULL,
  target       TEXT,
  status       TEXT NOT NULL,
  phase        TEXT,
  total        INTEGER NOT NULL DEFAULT 0,
  done         INTEGER NOT NULL DEFAULT 0,
  message      TEXT,
  started_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
  finished_at  DATETIME
);

CREATE INDEX IF NOT EXISTS idx_jobs_started ON jobs(started_at);
CREATE INDEX IF NOT EXISTS idx_nodes_library ON nodes(library_id);
CREATE INDEX IF NOT EXISTS idx_nodes_parent ON nodes(parent_id);
CREATE INDEX IF NOT EXISTS idx_access_library ON library_access(library_id);
CREATE INDEX IF NOT EXISTS idx_favorites_user ON album_favorites(plex_username);
CREATE INDEX IF NOT EXISTS idx_views_user ON album_views(plex_username, viewed_at);
CREATE INDEX IF NOT EXISTS idx_photo_meta_year_place ON photo_meta(year, place_country, place_city);
CREATE INDEX IF NOT EXISTS idx_photo_meta_lib ON photo_meta(library_id);
CREATE INDEX IF NOT EXISTS idx_photo_people_name ON photo_people(name);
`

// OpenDB opens (and creates if needed) the SQLite database at dataPath/plex-photos.db,
// enabling foreign keys and applying the schema migrations.
func OpenDB(dataPath string) (*sql.DB, error) {
	dbPath := filepath.Join(dataPath, "plex-photos.db")
	// WAL lets readers run concurrently with a single writer, so browsing the UI
	// stays responsive while a library scan writes metadata. synchronous=NORMAL
	// is the recommended, durable pairing for WAL (only an OS/power crash can
	// lose the last transaction). busy_timeout retries instead of failing when a
	// second writer briefly contends.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)",
		dbPath,
	)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// WAL supports one writer + many concurrent readers. A small pool lets reads
	// proceed while a scan writes; SQLite still allows only one writer at a time,
	// and any brief writer collision is absorbed by busy_timeout above.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// migrate applies idempotent additive migrations for databases created by
// earlier schema versions. Errors from already-applied columns are ignored.
func migrate(db *sql.DB) error {
	// One-time migration from the legacy flat model (collections + albums, with
	// album_favorites/album_views keyed on album_id) to the recursive nodes
	// tree. Per design we rebuild on next scan rather than migrating rows, so we
	// drop the legacy tables. Favorites/recently-viewed are reset.
	if hasColumn(db, "album_favorites", "album_id") || hasColumn(db, "album_views", "album_id") {
		for _, ddl := range []string{
			`DROP TABLE IF EXISTS album_favorites`,
			`DROP TABLE IF EXISTS album_views`,
		} {
			if _, err := db.Exec(ddl); err != nil {
				return err
			}
		}
	}
	for _, ddl := range []string{
		`DROP TABLE IF EXISTS albums`,
		`DROP TABLE IF EXISTS collections`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			return err
		}
	}
	// Recreate the (possibly dropped) node/favorite/view tables so the rest of
	// the schema is present even on an upgraded DB.
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	for _, m := range []struct{ table, column, ddl string }{
		{"libraries", "cover_photo", `ALTER TABLE libraries ADD COLUMN cover_photo TEXT`},
		{"libraries", "cover_set_by", `ALTER TABLE libraries ADD COLUMN cover_set_by TEXT`},
		{"libraries", "cover_set_at", `ALTER TABLE libraries ADD COLUMN cover_set_at DATETIME`},
		{"libraries", "background_photo", `ALTER TABLE libraries ADD COLUMN background_photo TEXT`},
		{"libraries", "sort_title", `ALTER TABLE libraries ADD COLUMN sort_title TEXT`},
		{"libraries", "summary", `ALTER TABLE libraries ADD COLUMN summary TEXT`},
		{"tvs", "display_mode", `ALTER TABLE tvs ADD COLUMN display_mode TEXT NOT NULL DEFAULT 'blur-fill'`},
		{"tvs", "bg_color", `ALTER TABLE tvs ADD COLUMN bg_color TEXT NOT NULL DEFAULT '#000000'`},
		{"tvs", "border_pct", `ALTER TABLE tvs ADD COLUMN border_pct INTEGER NOT NULL DEFAULT 0`},
		{"tvs", "smart_fill", `ALTER TABLE tvs ADD COLUMN smart_fill INTEGER NOT NULL DEFAULT 0`},
		{"tvs", "caption_fields", `ALTER TABLE tvs ADD COLUMN caption_fields TEXT NOT NULL DEFAULT ''`},
		{"tvs", "play_order", `ALTER TABLE tvs ADD COLUMN play_order TEXT NOT NULL DEFAULT 'sequential'`},
		{"tvs", "photo_filter", `ALTER TABLE tvs ADD COLUMN photo_filter TEXT NOT NULL DEFAULT 'none'`},
		{"tv_player_state", "deck", `ALTER TABLE tv_player_state ADD COLUMN deck TEXT NOT NULL DEFAULT ''`},
		{"photo_meta", "place_province", `ALTER TABLE photo_meta ADD COLUMN place_province TEXT`},
		{"photo_meta", "camera", `ALTER TABLE photo_meta ADD COLUMN camera TEXT`},
		{"photo_meta", "lens", `ALTER TABLE photo_meta ADD COLUMN lens TEXT`},
		{"photo_meta", "exposure", `ALTER TABLE photo_meta ADD COLUMN exposure TEXT`},
		{"photo_meta", "aperture", `ALTER TABLE photo_meta ADD COLUMN aperture TEXT`},
		{"photo_meta", "iso", `ALTER TABLE photo_meta ADD COLUMN iso TEXT`},
		{"photo_meta", "focal_length", `ALTER TABLE photo_meta ADD COLUMN focal_length TEXT`},
		{"photo_meta", "meta_version", `ALTER TABLE photo_meta ADD COLUMN meta_version INTEGER NOT NULL DEFAULT 0`},
	} {
		if !hasColumn(db, m.table, m.column) {
			if _, err := db.Exec(m.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasColumn(db *sql.DB, table, column string) bool {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}
