package library

import (
	"database/sql"
	"fmt"
	"path/filepath"

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

CREATE INDEX IF NOT EXISTS idx_nodes_library ON nodes(library_id);
CREATE INDEX IF NOT EXISTS idx_nodes_parent ON nodes(parent_id);
CREATE INDEX IF NOT EXISTS idx_access_library ON library_access(library_id);
CREATE INDEX IF NOT EXISTS idx_favorites_user ON album_favorites(plex_username);
CREATE INDEX IF NOT EXISTS idx_views_user ON album_views(plex_username, viewed_at);
`

// OpenDB opens (and creates if needed) the SQLite database at dataPath/plex-photos.db,
// enabling foreign keys and applying the schema migrations.
func OpenDB(dataPath string) (*sql.DB, error) {
	dbPath := filepath.Join(dataPath, "plex-photos.db")
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", dbPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite: serialize writes to avoid "database is locked".

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
