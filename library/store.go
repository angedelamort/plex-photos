package library

import (
	"database/sql"
	"encoding/json"
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
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(photo_count), 0) FROM nodes WHERE library_id = ?`, id).Scan(&lib.PhotoCount)
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
	// Best-effort child count and recursive photo total for cards.
	for _, n := range out {
		_ = s.countChildren(n)
		_ = s.countTotalPhotos(n)
	}
	return out, nil
}

// store reference for collectNodes child-count; bound via method below.
func (s *Store) countChildren(n *Node) error {
	return s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE parent_id = ?`, n.ID).Scan(&n.ChildCount)
}

// countTotalPhotos sums photo_count for the node and all of its descendants so
// collection cards can show how many photos live beneath them, not just the
// (often zero) photos sitting directly in the folder.
func (s *Store) countTotalPhotos(n *Node) error {
	return s.db.QueryRow(`
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM nodes WHERE id = ?
			UNION ALL
			SELECT n.id FROM nodes n JOIN descendants d ON n.parent_id = d.id
		)
		SELECT COALESCE(SUM(photo_count), 0) FROM nodes WHERE id IN (SELECT id FROM descendants)`,
		n.ID).Scan(&n.TotalPhotoCount)
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
	_ = s.countTotalPhotos(n)
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

// ReferencedArtPhotos returns every distinct cover/background photo path
// referenced by nodes or libraries for the given library. These are real photo
// tokens (not "@art/" custom uploads) that a thumbnail may have been generated
// for; the scan cleanup phase treats them as in-use so it never deletes a
// thumbnail that the DB still points at, even if the photo is not part of the
// normal listed-photo set (e.g. an admin-set cover on an unusual path).
func (s *Store) ReferencedArtPhotos(libraryID string) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT cover_photo FROM nodes WHERE library_id = ? AND cover_photo IS NOT NULL AND cover_photo != ''
		UNION
		SELECT background_photo FROM nodes WHERE library_id = ? AND background_photo IS NOT NULL AND background_photo != ''
		UNION
		SELECT cover_photo FROM libraries WHERE id = ? AND cover_photo IS NOT NULL AND cover_photo != ''
		UNION
		SELECT background_photo FROM libraries WHERE id = ? AND background_photo IS NOT NULL AND background_photo != ''`,
		libraryID, libraryID, libraryID, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Best-effort child count and recursive photo total for home cards.
	for _, a := range out {
		_ = s.countChildren(&a.Node)
		_ = s.countTotalPhotos(&a.Node)
	}
	return out, nil
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

// Job status values.
const (
	JobRunning = "running"
	JobSuccess = "success"
	JobFailed  = "failed"
)

// Job type values.
const (
	JobTypeScan        = "scan"
	JobTypeThumbnail   = "thumbnails"
	JobTypeCleanup     = "cleanup"
	JobTypePlaylistAdd = "playlist-add"
)

// maxJobHistory caps how many finished job rows are retained. Running jobs are
// never pruned; on insert, finished rows beyond this count are removed.
const maxJobHistory = 20

// Job is a single background job run with live progress and a final status.
type Job struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	Target     string     `json:"target"`
	Status     string     `json:"status"`
	Phase      string     `json:"phase"`
	Total      int        `json:"total"`
	Done       int        `json:"done"`
	Message    string     `json:"message"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	// Current is the item (e.g. photo path) being processed right now. It is a
	// live, in-memory overlay for the running job only and is never persisted.
	Current string `json:"current,omitempty"`
	// Paused reports whether an admin has held the running job. Like Current, it
	// is a live overlay for the active job only and is never persisted.
	Paused bool `json:"paused,omitempty"`
}

// CreateJob inserts a new running job row and returns its ID.
func (s *Store) CreateJob(id, jobType, target string) error {
	_, err := s.db.Exec(
		`INSERT INTO jobs (id, type, target, status, started_at) VALUES (?, ?, ?, ?, ?)`,
		id, jobType, target, JobRunning, time.Now())
	return err
}

// UpdateJobProgress updates the live progress fields of a running job.
func (s *Store) UpdateJobProgress(id, phase string, done, total int) error {
	_, err := s.db.Exec(
		`UPDATE jobs SET phase = ?, done = ?, total = ? WHERE id = ?`,
		phase, done, total, id)
	return err
}

// FinishJob marks a job as completed (success or failed) and prunes the history
// so only the most recent maxJobHistory finished jobs are retained.
func (s *Store) FinishJob(id, status, message string) error {
	if _, err := s.db.Exec(
		`UPDATE jobs SET status = ?, message = ?, phase = '', finished_at = ? WHERE id = ?`,
		status, message, time.Now(), id); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`DELETE FROM jobs WHERE status != ? AND id NOT IN (
			SELECT id FROM jobs WHERE status != ? ORDER BY started_at DESC LIMIT ?
		)`, JobRunning, JobRunning, maxJobHistory)
	return err
}

// ListJobs returns recorded jobs, most recent first.
func (s *Store) ListJobs() ([]*Job, error) {
	rows, err := s.db.Query(
		`SELECT id, type, COALESCE(target, ''), status, COALESCE(phase, ''),
		        total, done, COALESCE(message, ''), started_at, finished_at
		 FROM jobs ORDER BY started_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		var j Job
		var finished sql.NullTime
		if err := rows.Scan(&j.ID, &j.Type, &j.Target, &j.Status, &j.Phase,
			&j.Total, &j.Done, &j.Message, &j.StartedAt, &finished); err != nil {
			return nil, err
		}
		if finished.Valid {
			j.FinishedAt = &finished.Time
		}
		out = append(out, &j)
	}
	return out, rows.Err()
}

// --- Scan timing reports ---

// SettingScanReportLimit is the settings key holding how many scan timing
// reports are retained. Older reports beyond this count are pruned on insert.
const SettingScanReportLimit = "scan_report_limit"

// defaultScanReportLimit / maxScanReportLimit bound the retained-report count.
const (
	defaultScanReportLimit = 10
	maxScanReportLimit     = 50
)

// ScanReportMeta is the lightweight header of a stored scan report (everything
// except the potentially large JSON body), used for the reports list.
type ScanReportMeta struct {
	ID          int64      `json:"id"`
	JobID       string     `json:"jobId"`
	LibraryID   string     `json:"libraryId"`
	LibraryName string     `json:"libraryName"`
	Status      string     `json:"status"`
	StartedAt   *time.Time `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

// ScanReportRecord is a stored report's header plus its JSON measurements body.
// Report is kept raw so it is embedded as an object in the API response rather
// than a re-encoded string.
type ScanReportRecord struct {
	ScanReportMeta
	Report json.RawMessage `json:"report"`
}

// scanReportLimit resolves the configured retention count, clamped to a sane
// range. Falls back to the default when unset or unparseable.
func (s *Store) scanReportLimit() int {
	n := defaultScanReportLimit
	if v, err := s.GetSetting(SettingScanReportLimit, ""); err == nil {
		if parsed, perr := strconv.Atoi(strings.TrimSpace(v)); perr == nil {
			n = parsed
		}
	}
	if n < 1 {
		n = 1
	}
	if n > maxScanReportLimit {
		n = maxScanReportLimit
	}
	return n
}

// RecordScanReport stores one scan's JSON timing report and prunes older reports
// beyond the configured limit. reportJSON is the marshaled ScanReport.
func (s *Store) RecordScanReport(jobID, libraryID, libraryName, status string, startedAt, finishedAt time.Time, reportJSON string) error {
	if _, err := s.db.Exec(
		`INSERT INTO scan_reports (job_id, library_id, library_name, status, started_at, finished_at, report)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		nullIfEmpty(jobID), nullIfEmpty(libraryID), libraryName, status,
		startedAt, finishedAt, reportJSON); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`DELETE FROM scan_reports WHERE id NOT IN (
			SELECT id FROM scan_reports ORDER BY id DESC LIMIT ?
		)`, s.scanReportLimit())
	return err
}

// ListScanReports returns the stored report headers (no JSON body), newest
// first, so the admin UI can list runs cheaply.
func (s *Store) ListScanReports() ([]*ScanReportMeta, error) {
	rows, err := s.db.Query(
		`SELECT id, COALESCE(job_id, ''), COALESCE(library_id, ''), library_name,
		        status, started_at, finished_at
		 FROM scan_reports ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ScanReportMeta{}
	for rows.Next() {
		var m ScanReportMeta
		var started, finished sql.NullTime
		if err := rows.Scan(&m.ID, &m.JobID, &m.LibraryID, &m.LibraryName,
			&m.Status, &started, &finished); err != nil {
			return nil, err
		}
		if started.Valid {
			m.StartedAt = &started.Time
		}
		if finished.Valid {
			m.FinishedAt = &finished.Time
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// GetScanReport returns a single stored report (header + JSON body) by ID, or
// ErrNotFound when it does not exist.
func (s *Store) GetScanReport(id int64) (*ScanReportRecord, error) {
	var rec ScanReportRecord
	var started, finished sql.NullTime
	var body string
	err := s.db.QueryRow(
		`SELECT id, COALESCE(job_id, ''), COALESCE(library_id, ''), library_name,
		        status, started_at, finished_at, report
		 FROM scan_reports WHERE id = ?`, id).
		Scan(&rec.ID, &rec.JobID, &rec.LibraryID, &rec.LibraryName,
			&rec.Status, &started, &finished, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if started.Valid {
		rec.StartedAt = &started.Time
	}
	if finished.Valid {
		rec.FinishedAt = &finished.Time
	}
	rec.Report = json.RawMessage(body)
	return &rec, nil
}

// ClearScanReports removes all stored scan reports.
func (s *Store) ClearScanReports() error {
	_, err := s.db.Exec(`DELETE FROM scan_reports`)
	return err
}

// MarkStaleJobsFailed flips any jobs left in the "running" state (e.g. due to a
// process crash/restart) to failed, since they can no longer make progress.
func (s *Store) MarkStaleJobsFailed() error {
	_, err := s.db.Exec(
		`UPDATE jobs SET status = ?, message = ?, finished_at = ? WHERE status = ?`,
		JobFailed, "interrupted by restart", time.Now(), JobRunning)
	return err
}

// --- Photo metadata index ---

// UpsertPhotoMeta inserts or replaces the indexed metadata for a single photo,
// rewriting its person tags. photo_meta and photo_people are updated together
// in a transaction so a photo's row and its people never drift apart.
func (s *Store) UpsertPhotoMeta(m PhotoMeta) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO photo_meta
		    (photo_path, library_id, taken_at, year, lat, lon, has_gps,
		     place_city, place_province, place_country,
		     camera, lens, exposure, aperture, iso, focal_length,
		     width, height, orientation,
		     has_sidecar, file_mtime, file_size, meta_version, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(photo_path) DO UPDATE SET
		    library_id = excluded.library_id,
		    taken_at = excluded.taken_at,
		    year = excluded.year,
		    lat = excluded.lat,
		    lon = excluded.lon,
		    has_gps = excluded.has_gps,
		    place_city = excluded.place_city,
		    place_province = excluded.place_province,
		    place_country = excluded.place_country,
		    camera = excluded.camera,
		    lens = excluded.lens,
		    exposure = excluded.exposure,
		    aperture = excluded.aperture,
		    iso = excluded.iso,
		    focal_length = excluded.focal_length,
		    width = excluded.width,
		    height = excluded.height,
		    orientation = excluded.orientation,
		    has_sidecar = excluded.has_sidecar,
		    file_mtime = excluded.file_mtime,
		    file_size = excluded.file_size,
		    meta_version = excluded.meta_version,
		    indexed_at = CURRENT_TIMESTAMP`,
		m.PhotoPath, nullIfEmpty(m.LibraryID), nullTime(m.TakenAt), nullIfZero(m.Year),
		m.Lat, m.Lon, boolToInt(m.HasGPS),
		nullIfEmpty(m.PlaceCity), nullIfEmpty(m.PlaceProvince), nullIfEmpty(m.PlaceCountry),
		nullIfEmpty(m.Camera), nullIfEmpty(m.Lens), nullIfEmpty(m.Exposure),
		nullIfEmpty(m.Aperture), nullIfEmpty(m.ISO), nullIfEmpty(m.FocalLength),
		nullIfZero(m.Width), nullIfZero(m.Height), nullIfEmpty(m.Orientation),
		boolToInt(m.HasSidecar), m.FileMtime, m.FileSize, m.MetaVersion); err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM photo_people WHERE photo_path = ?`, m.PhotoPath); err != nil {
		return err
	}
	for _, name := range m.People {
		if name == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO photo_people (photo_path, name) VALUES (?, ?)`,
			m.PhotoPath, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetPhotoMeta returns the indexed metadata (with person tags) for a photo, or
// ErrNotFound when it has not been indexed.
func (s *Store) GetPhotoMeta(photoPath string) (*PhotoMeta, error) {
	var m PhotoMeta
	var takenAt sql.NullTime
	var year, width, height sql.NullInt64
	var hasGPS, hasSidecar int
	var city, province, country, orient, libID sql.NullString
	var camera, lens, exposure, aperture, iso, focal sql.NullString
	var metaVersion sql.NullInt64
	err := s.db.QueryRow(`
		SELECT photo_path, COALESCE(library_id, ''), taken_at, year, lat, lon, has_gps,
		       place_city, place_province, place_country,
		       camera, lens, exposure, aperture, iso, focal_length,
		       width, height, orientation, has_sidecar,
		       COALESCE(file_mtime, 0), COALESCE(file_size, 0), COALESCE(meta_version, 0)
		FROM photo_meta WHERE photo_path = ?`, photoPath).
		Scan(&m.PhotoPath, &libID, &takenAt, &year, &m.Lat, &m.Lon, &hasGPS,
			&city, &province, &country,
			&camera, &lens, &exposure, &aperture, &iso, &focal,
			&width, &height, &orient, &hasSidecar,
			&m.FileMtime, &m.FileSize, &metaVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.LibraryID = libID.String
	if takenAt.Valid {
		m.TakenAt = takenAt.Time
	}
	m.Year = int(year.Int64)
	m.Width = int(width.Int64)
	m.Height = int(height.Int64)
	m.HasGPS = hasGPS != 0
	m.HasSidecar = hasSidecar != 0
	m.PlaceCity = city.String
	m.PlaceProvince = province.String
	m.PlaceCountry = country.String
	m.Camera = camera.String
	m.Lens = lens.String
	m.Exposure = exposure.String
	m.Aperture = aperture.String
	m.ISO = iso.String
	m.FocalLength = focal.String
	m.MetaVersion = int(metaVersion.Int64)
	m.Orientation = orient.String

	people, err := s.photoPeople(photoPath)
	if err != nil {
		return nil, err
	}
	m.People = people
	return &m, nil
}

func (s *Store) photoPeople(photoPath string) ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM photo_people WHERE photo_path = ? ORDER BY name`, photoPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// PhotoMetaStat returns the file mtime, size, and meta_version recorded for a
// photo the last time it was indexed. ok is false when the photo has no row
// yet, so the scan hook knows it must (re)extract. The version lets the scan
// re-index rows written by an older extractor even when the file is unchanged.
func (s *Store) PhotoMetaStat(photoPath string) (mtime, size int64, version int, ok bool, err error) {
	var v sql.NullInt64
	err = s.db.QueryRow(`SELECT COALESCE(file_mtime, 0), COALESCE(file_size, 0), COALESCE(meta_version, 0) FROM photo_meta WHERE photo_path = ?`, photoPath).
		Scan(&mtime, &size, &v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, 0, false, err
	}
	return mtime, size, int(v.Int64), true, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
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
