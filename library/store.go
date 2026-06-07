package library

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

// Store provides persistence operations over the SQLite database.
type Store struct {
	db *sql.DB
}

// NewStore wraps a database handle.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// CreateLibrary inserts a new library with its access whitelist.
func (s *Store) CreateLibrary(name, rootPath string, whitelist []string) (*Library, error) {
	id := uuid.NewString()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO libraries (id, name, root_path) VALUES (?, ?, ?)`, id, name, rootPath); err != nil {
		return nil, err
	}
	if err := replaceAccess(tx, id, whitelist); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetLibrary(id)
}

// UpdateLibrary updates name, root path and whitelist of an existing library.
func (s *Store) UpdateLibrary(id, name, rootPath string, whitelist []string, sortTitle, summary string) (*Library, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE libraries SET name = ?, root_path = ?, sort_title = ?, summary = ? WHERE id = ?`, name, rootPath, sortTitle, summary, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if err := replaceAccess(tx, id, whitelist); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetLibrary(id)
}

func replaceAccess(tx *sql.Tx, libraryID string, whitelist []string) error {
	if _, err := tx.Exec(`DELETE FROM library_access WHERE library_id = ?`, libraryID); err != nil {
		return err
	}
	for _, u := range whitelist {
		if u == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO library_access (library_id, plex_username) VALUES (?, ?)`, libraryID, u); err != nil {
			return err
		}
	}
	return nil
}

// DeleteLibrary removes a library and (via cascade) its collections/albums/access.
func (s *Store) DeleteLibrary(id string) error {
	res, err := s.db.Exec(`DELETE FROM libraries WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetLibrary fetches a single library with its whitelist and collection count.
func (s *Store) GetLibrary(id string) (*Library, error) {
	var lib Library
	var lastScan sql.NullTime
	err := s.db.QueryRow(`SELECT id, name, root_path, created_at, last_scan, COALESCE(cover_photo, ''), COALESCE(background_photo, ''), COALESCE(sort_title, ''), COALESCE(summary, '') FROM libraries WHERE id = ?`, id).
		Scan(&lib.ID, &lib.Name, &lib.RootPath, &lib.CreatedAt, &lastScan, &lib.CoverPhoto, &lib.BackgroundPhoto, &lib.SortTitle, &lib.Summary)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if lastScan.Valid {
		lib.LastScan = &lastScan.Time
	}
	if lib.Whitelist, err = s.whitelist(id); err != nil {
		return nil, err
	}
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE library_id = ? AND parent_id IS NULL`, id).Scan(&lib.CollectionCount)
	return &lib, nil
}

func (s *Store) whitelist(libraryID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT plex_username FROM library_access WHERE library_id = ? ORDER BY plex_username`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListLibraries returns all libraries (admin view).
func (s *Store) ListLibraries() ([]*Library, error) {
	rows, err := s.db.Query(`SELECT id FROM libraries ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]*Library, 0, len(ids))
	for _, id := range ids {
		lib, err := s.GetLibrary(id)
		if err != nil {
			return nil, err
		}
		out = append(out, lib)
	}
	return out, nil
}

// ListLibrariesForUser returns libraries the given user can access. Admins see all.
func (s *Store) ListLibrariesForUser(username string, isAdmin bool) ([]*Library, error) {
	if isAdmin {
		return s.ListLibraries()
	}
	rows, err := s.db.Query(`SELECT library_id FROM library_access WHERE plex_username = ?`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]*Library, 0, len(ids))
	for _, id := range ids {
		lib, err := s.GetLibrary(id)
		if err != nil {
			return nil, err
		}
		out = append(out, lib)
	}
	return out, nil
}

// nodeCols is the canonical column list for scanning a Node.
const nodeCols = `id, library_id, COALESCE(parent_id, ''), name, fs_path, depth, photo_count, has_children,
	COALESCE(cover_photo, ''), COALESCE(background_photo, ''),
	COALESCE(sort_title, ''), COALESCE(summary, ''), COALESCE(content_rating, ''), COALESCE(year, ''), COALESCE(studio, '')`

func scanNode(sc interface{ Scan(...any) error }) (*Node, error) {
	var n Node
	var hasChildren int
	if err := sc.Scan(&n.ID, &n.LibraryID, &n.ParentID, &n.Name, &n.FSPath, &n.Depth, &n.PhotoCount, &hasChildren,
		&n.CoverPhoto, &n.BackgroundPhoto, &n.SortTitle, &n.Summary, &n.ContentRating, &n.Year, &n.Studio); err != nil {
		return nil, err
	}
	n.HasChildren = hasChildren != 0
	n.Type = nodeType(n.HasChildren, n.PhotoCount)
	return &n, nil
}

// nodeType classifies a node: a leaf folder holding photos is an "album",
// everything else (has sub-folders, or empty) is a "collection".
func nodeType(hasChildren bool, photoCount int) string {
	if !hasChildren && photoCount > 0 {
		return "album"
	}
	return "collection"
}

// ListTopLevelNodes returns the top-level nodes (parent_id NULL) of a library.
func (s *Store) ListTopLevelNodes(libraryID string) ([]*Node, error) {
	rows, err := s.db.Query(`SELECT `+nodeCols+`
		FROM nodes WHERE library_id = ? AND parent_id IS NULL
		ORDER BY COALESCE(NULLIF(sort_title, ''), name)`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.collectNodes(rows)
}

// ListChildNodes returns the direct children of a node.
func (s *Store) ListChildNodes(parentID string) ([]*Node, error) {
	rows, err := s.db.Query(`SELECT `+nodeCols+`
		FROM nodes WHERE parent_id = ?
		ORDER BY COALESCE(NULLIF(sort_title, ''), name)`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.collectNodes(rows)
}

// SearchNodes returns nodes (albums / collections) whose name or sort title
// matches the query, limited to the given set of accessible library ids. The
// match is a case-insensitive substring. Results are capped by limit.
func (s *Store) SearchNodes(libraryIDs []string, query string, limit int) ([]*Node, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(libraryIDs) == 0 {
		return []*Node{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	placeholders := make([]string, len(libraryIDs))
	args := make([]any, 0, len(libraryIDs)+3)
	for i, id := range libraryIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	like := "%" + escapeLike(strings.ToLower(query)) + "%"
	args = append(args, like, like, limit)
	rows, err := s.db.Query(`SELECT `+nodeCols+`
		FROM nodes
		WHERE library_id IN (`+strings.Join(placeholders, ",")+`)
		  AND (LOWER(name) LIKE ? ESCAPE '\' OR LOWER(sort_title) LIKE ? ESCAPE '\')
		ORDER BY COALESCE(NULLIF(sort_title, ''), name)
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.collectNodes(rows)
}

// escapeLike escapes the LIKE wildcards in a user-supplied search term so they
// are matched literally (using '\' as the escape character).
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (s *Store) collectNodes(rows *sql.Rows) ([]*Node, error) {
	out := []*Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Best-effort child count for cards.
	for _, n := range out {
		_ = s.countChildren(n)
	}
	return out, nil
}

// store reference for collectNodes child-count; bound via method below.
func (s *Store) countChildren(n *Node) error {
	return s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE parent_id = ?`, n.ID).Scan(&n.ChildCount)
}

// GetNode fetches one node by id.
func (s *Store) GetNode(id string) (*Node, error) {
	n, err := scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = s.countChildren(n)
	return n, nil
}

// Ancestors returns the chain of ancestor nodes from the top-level node down to
// (but not including) the given node, used to build breadcrumbs.
func (s *Store) Ancestors(id string) ([]*Node, error) {
	var chain []*Node
	cur, err := s.GetNode(id)
	if err != nil {
		return nil, err
	}
	for cur.ParentID != "" {
		p, err := s.GetNode(cur.ParentID)
		if err != nil {
			return nil, err
		}
		chain = append([]*Node{p}, chain...)
		cur = p
	}
	return chain, nil
}

// NodeMetadata holds the user-editable fields of a node.
type NodeMetadata struct {
	SortTitle     string
	Summary       string
	ContentRating string
	Year          string
	Studio        string
}

// UpdateNodeMetadata sets the user-editable metadata of a node. These fields are
// stored separately from the folder-derived name so they survive rescans.
func (s *Store) UpdateNodeMetadata(id string, m NodeMetadata) error {
	res, err := s.db.Exec(`UPDATE nodes SET sort_title = ?, summary = ?, content_rating = ?, year = ?, studio = ? WHERE id = ?`,
		m.SortTitle, m.Summary, m.ContentRating, m.Year, m.Studio, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetNodeCover updates the cover photo (relative path) of a node.
func (s *Store) SetNodeCover(id, coverPhoto, setBy string) error {
	res, err := s.db.Exec(`UPDATE nodes SET cover_photo = ?, cover_set_by = ?, cover_set_at = ? WHERE id = ?`,
		coverPhoto, setBy, time.Now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CacheNodeCover persists a system-discovered cover only when the node has no
// cover yet, so request-time backfill of an empty cover is stored once and
// subsequent reads skip the filesystem walk. It never overwrites a user-set or
// scan-set cover (cover_set_by stays NULL so a later scan can still refresh it).
func (s *Store) CacheNodeCover(id, coverPhoto string) error {
	if coverPhoto == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE nodes SET cover_photo = ? WHERE id = ? AND (cover_photo IS NULL OR cover_photo = '')`,
		coverPhoto, id)
	return err
}

// SetNodeBackground updates the background photo (relative path) of a node.
func (s *Store) SetNodeBackground(id, photo string) error {
	res, err := s.db.Exec(`UPDATE nodes SET background_photo = ? WHERE id = ?`, photo, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetLibraryCover updates the cover photo (relative path) of a library.
func (s *Store) SetLibraryCover(id, photo, setBy string) error {
	res, err := s.db.Exec(`UPDATE libraries SET cover_photo = ?, cover_set_by = ?, cover_set_at = ? WHERE id = ?`,
		photo, setBy, time.Now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetLibraryBackground updates the background photo (relative path) of a library.
func (s *Store) SetLibraryBackground(id, photo string) error {
	res, err := s.db.Exec(`UPDATE libraries SET background_photo = ? WHERE id = ?`, photo, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Favorites ---

// SetFavorite adds or removes a node from a user's favorites.
func (s *Store) SetFavorite(username, nodeID string, fav bool) error {
	if fav {
		_, err := s.db.Exec(`INSERT OR IGNORE INTO album_favorites (plex_username, node_id) VALUES (?, ?)`, username, nodeID)
		return err
	}
	_, err := s.db.Exec(`DELETE FROM album_favorites WHERE plex_username = ? AND node_id = ?`, username, nodeID)
	return err
}

// FavoriteNodeIDs returns the set of node ids favorited by the user.
func (s *Store) FavoriteNodeIDs(username string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT node_id FROM album_favorites WHERE plex_username = ?`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// FavoriteNodes returns the user's favorited nodes with navigation context,
// most recently favorited first.
func (s *Store) FavoriteNodes(username string) ([]*HomeNode, error) {
	return s.queryHomeNodes(`
		`+homeNodeSelect+`
		FROM album_favorites f
		JOIN nodes n ON n.id = f.node_id
		JOIN libraries l ON l.id = n.library_id
		WHERE f.plex_username = ?
		ORDER BY f.created_at DESC`, username)
}

// --- Recently viewed ---

// RecordView upserts the last-viewed timestamp for a user/node pair.
func (s *Store) RecordView(username, nodeID string) error {
	_, err := s.db.Exec(`
		INSERT INTO album_views (plex_username, node_id, viewed_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(plex_username, node_id) DO UPDATE SET viewed_at = CURRENT_TIMESTAMP`,
		username, nodeID)
	return err
}

// GetPreferences returns the user's stored UI preferences as a raw JSON object.
// When the user has no saved preferences, it returns "{}".
func (s *Store) GetPreferences(username string) (string, error) {
	var prefs string
	err := s.db.QueryRow(`SELECT prefs FROM user_preferences WHERE plex_username = ?`, username).Scan(&prefs)
	if errors.Is(err, sql.ErrNoRows) {
		return "{}", nil
	}
	if err != nil {
		return "", err
	}
	if prefs == "" {
		return "{}", nil
	}
	return prefs, nil
}

// SetPreferences stores the user's UI preferences as a raw JSON object.
func (s *Store) SetPreferences(username, prefsJSON string) error {
	_, err := s.db.Exec(`
		INSERT INTO user_preferences (plex_username, prefs, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(plex_username) DO UPDATE SET
			prefs = excluded.prefs,
			updated_at = CURRENT_TIMESTAMP`,
		username, prefsJSON)
	return err
}

// RecentNodes returns the user's most recently viewed nodes with context.
func (s *Store) RecentNodes(username string, limit int) ([]*HomeNode, error) {
	if limit <= 0 {
		limit = 20
	}
	return s.queryHomeNodes(`
		`+homeNodeSelect+`
		FROM album_views v
		JOIN nodes n ON n.id = v.node_id
		JOIN libraries l ON l.id = n.library_id
		WHERE v.plex_username = ?
		ORDER BY v.viewed_at DESC
		LIMIT `+itoa(limit), username)
}

// RandomLibraryNodes returns up to limit photo-bearing nodes from a single
// library, shuffled deterministically by seed. The same seed yields the same
// selection, so callers can hold a selection stable across refreshes by reusing
// a time-bucketed seed (see TimeBucketSeed).
func (s *Store) RandomLibraryNodes(libraryID string, limit int, seed int64) ([]*HomeNode, error) {
	if limit <= 0 {
		limit = 12
	}
	// Deterministic shuffle: order rows by a seed-dependent hash of the rowid.
	// modernc.org/sqlite has no RANDOM(seed) or hash function, so we use a
	// multiplicative permutation modulo a large prime, which spreads rows
	// pseudo-randomly while staying stable for a fixed seed.
	const prime = 2147483647
	mult := (seed%prime+prime)%prime*2654435761 + 1
	return s.queryHomeNodes(`
		`+homeNodeSelect+`
		FROM nodes n
		JOIN libraries l ON l.id = n.library_id
		WHERE l.id = ? AND n.photo_count > 0
		ORDER BY ((n.rowid + 1) * ?) % `+itoa(prime)+`
		LIMIT `+itoa(limit), libraryID, mult)
}

// TimeBucketSeed returns a seed that stays constant within each window-long
// interval of wall-clock time and changes when the interval rolls over. A
// window of 30 minutes keeps home page "random" picks stable for ~30 minutes.
func TimeBucketSeed(window time.Duration) int64 {
	if window <= 0 {
		return time.Now().UnixNano()
	}
	return time.Now().UnixNano() / int64(window)
}

const homeNodeSelect = `SELECT n.id, n.library_id, COALESCE(n.parent_id, ''), n.name, n.fs_path, n.depth, n.photo_count, n.has_children,
		COALESCE(n.cover_photo, ''), COALESCE(n.background_photo, ''),
		COALESCE(n.sort_title, ''), COALESCE(n.summary, ''), COALESCE(n.content_rating, ''), COALESCE(n.year, ''), COALESCE(n.studio, ''),
		l.name`

func (s *Store) queryHomeNodes(query string, args ...any) ([]*HomeNode, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*HomeNode{}
	for rows.Next() {
		var a HomeNode
		var hasChildren int
		if err := rows.Scan(&a.ID, &a.LibraryID, &a.ParentID, &a.Name, &a.FSPath, &a.Depth, &a.PhotoCount, &hasChildren,
			&a.CoverPhoto, &a.BackgroundPhoto, &a.SortTitle, &a.Summary, &a.ContentRating, &a.Year, &a.Studio,
			&a.LibraryName); err != nil {
			return nil, err
		}
		a.HasChildren = hasChildren != 0
		a.Type = nodeType(a.HasChildren, a.PhotoCount)
		out = append(out, &a)
	}
	return out, rows.Err()
}

func itoa(n int) string { return strconv.Itoa(n) }

// --- Global settings (key/value) ---

// GetSetting returns the stored value for a key, or fallback if unset.
func (s *Store) GetSetting(key, fallback string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return fallback, err
	}
	return v, nil
}

// SetSetting upserts a key/value setting.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value)
	return err
}

// --- Scan error log ---

// maxScanErrors caps how many scan-error rows are retained. On insert, older
// rows beyond this count are pruned so the log can't grow unbounded.
const maxScanErrors = 500

// ScanError is a single recorded library scan failure.
type ScanError struct {
	ID          int64     `json:"id"`
	LibraryID   string    `json:"libraryId"`
	LibraryName string    `json:"libraryName"`
	Source      string    `json:"source"`
	Message     string    `json:"message"`
	OccurredAt  time.Time `json:"occurredAt"`
}

// RecordScanError appends a failed-scan entry to the persistent log and prunes
// the oldest rows beyond maxScanErrors. source is e.g. "manual" or "auto-scan".
func (s *Store) RecordScanError(libraryID, libraryName, source, message string) error {
	if _, err := s.db.Exec(
		`INSERT INTO scan_errors (library_id, library_name, source, message) VALUES (?, ?, ?, ?)`,
		nullIfEmpty(libraryID), libraryName, source, message); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`DELETE FROM scan_errors WHERE id NOT IN (
			SELECT id FROM scan_errors ORDER BY id DESC LIMIT ?
		)`, maxScanErrors)
	return err
}

// ListScanErrors returns recorded scan errors, most recent first.
func (s *Store) ListScanErrors() ([]*ScanError, error) {
	rows, err := s.db.Query(
		`SELECT id, COALESCE(library_id, ''), library_name, source, message, occurred_at
		 FROM scan_errors ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ScanError{}
	for rows.Next() {
		var e ScanError
		if err := rows.Scan(&e.ID, &e.LibraryID, &e.LibraryName, &e.Source, &e.Message, &e.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ClearScanErrors removes all recorded scan errors.
func (s *Store) ClearScanErrors() error {
	_, err := s.db.Exec(`DELETE FROM scan_errors`)
	return err
}

// FindNodeByPath returns the node whose fs_path matches, if any.
func (s *Store) FindNodeByPath(fsPath string) (*Node, error) {
	n, err := scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE fs_path = ?`, fsPath))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}
