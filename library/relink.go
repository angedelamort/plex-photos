package library

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// errMoveConflict means a folder looked like a move but the destination already
// has rows we must not overwrite. The scan then treats that folder as removed
// and added, which is the previous behavior.
var errMoveConflict = errors.New("move relink conflict")

// moveLink is one relinked folder: every descendant row under oldPath is
// rewritten to the matching place under newPath.
type moveLink struct {
	oldPath string
	newPath string
}

// folderFinger is a directory identified by its path and a content signature.
// An empty sig means "do not pair" (no indexed images to match on).
type folderFinger struct {
	path string
	sig  string
}

// fileStamp is one image relative to a folder, using the same size and mtime
// (Unix seconds) the metadata index stores. Matching on these avoids rereading
// file bytes.
type fileStamp struct {
	rel   string
	size  int64
	mtime int64
}

// relinkMoves pairs folders that disappeared with folders that appeared, when
// the folder name and the image tree (relative path, size, mtime) match
// uniquely. It rewrites the existing rows inside tx. Thumbnail files are not
// touched here; the caller renames those after commit.
//
// A folder that was renamed (its base name changed), a file whose size or
// mtime changed, and two identical copies are left unpaired.
func (sc *Scanner) relinkMoves(tx *sql.Tx, lib *Library, currentDirs []string) ([]moveLink, error) {
	nodes, err := loadNodePaths(tx, lib.ID)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, nil
	}

	currentSet := make(map[string]bool, len(currentDirs))
	for _, d := range currentDirs {
		currentSet[filepath.Clean(d)] = true
	}
	nodeSet := make(map[string]bool, len(nodes))
	var missing []nodePath
	for _, n := range nodes {
		p := filepath.Clean(n.path)
		nodeSet[p] = true
		if !currentSet[p] {
			missing = append(missing, nodePath{id: n.id, path: p})
		}
	}
	var appeared []string
	for p := range currentSet {
		if !nodeSet[p] {
			appeared = append(appeared, p)
		}
	}
	if len(missing) == 0 || len(appeared) == 0 {
		return nil, nil
	}

	photos, err := loadPhotoStamps(tx, lib.ID)
	if err != nil {
		return nil, err
	}
	oldFingers := fingerprintsFromDB(missing, photos)
	newFingers, err := fingerprintsFromDisk(appeared)
	if err != nil {
		return nil, fmt.Errorf("relink: stat moved folders: %w", err)
	}

	pairs := pairFolderMoves(oldFingers, newFingers)
	var applied []moveLink
	for _, p := range pairs {
		n, err := sc.applyMove(tx, lib.ID, p)
		if err != nil {
			if skipMoveErr(err) {
				log.Printf("relink: skip %q -> %q: %v", p.oldPath, p.newPath, err)
				continue
			}
			return nil, err
		}
		log.Printf("relink: %q -> %q (%d folders)", p.oldPath, p.newPath, n)
		applied = append(applied, p)
	}
	return applied, nil
}

func skipMoveErr(err error) bool {
	return errors.Is(err, errMoveConflict) || strings.Contains(strings.ToLower(err.Error()), "constraint")
}

// pairFolderMoves returns the moves to apply. A signature must match exactly
// one missing folder and one new folder. A pair covered by an ancestor pair is
// dropped; rewriting the ancestor rewrites the descendants.
func pairFolderMoves(oldFolders, newFolders []folderFinger) []moveLink {
	oldBy := map[string][]folderFinger{}
	newBy := map[string][]folderFinger{}
	for _, f := range oldFolders {
		if f.sig == "" {
			continue
		}
		oldBy[f.sig] = append(oldBy[f.sig], f)
	}
	for _, f := range newFolders {
		if f.sig == "" {
			continue
		}
		newBy[f.sig] = append(newBy[f.sig], f)
	}
	var pairs []moveLink
	for sig, olds := range oldBy {
		news := newBy[sig]
		if len(olds) != 1 || len(news) != 1 {
			if len(olds) > 0 && len(news) > 0 {
				log.Printf("relink: %d missing and %d new folders share a signature (name %q); not relinking them",
					len(olds), len(news), filepath.Base(olds[0].path))
			}
			continue
		}
		pairs = append(pairs, moveLink{oldPath: olds[0].path, newPath: news[0].path})
	}
	return topmostMoves(pairs)
}

// topmostMoves drops a pair whose old path sits inside another paired folder.
func topmostMoves(pairs []moveLink) []moveLink {
	var out []moveLink
	for _, p := range pairs {
		covered := false
		for _, o := range pairs {
			if o.oldPath != p.oldPath && hasDirPrefix(p.oldPath, o.oldPath) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

func fingerSig(name string, files []fileStamp) string {
	if name == "" || len(files) == 0 {
		return ""
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte(0)
	for _, f := range files {
		fmt.Fprintf(&b, "%s\x00%d\x00%d\n", f.rel, f.size, f.mtime)
	}
	return b.String()
}

type nodePath struct {
	id   string
	path string
}

func loadNodePaths(tx *sql.Tx, libraryID string) ([]nodePath, error) {
	rows, err := tx.Query(`SELECT id, fs_path FROM nodes WHERE library_id = ?`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []nodePath
	for rows.Next() {
		var n nodePath
		if err := rows.Scan(&n.id, &n.path); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

type photoStamp struct {
	abs   string
	size  int64
	mtime int64
}

func loadPhotoStamps(tx *sql.Tx, libraryID string) ([]photoStamp, error) {
	rows, err := tx.Query(
		`SELECT photo_path, COALESCE(file_size, 0), COALESCE(file_mtime, 0) FROM photo_meta WHERE library_id = ?`,
		libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []photoStamp
	for rows.Next() {
		var token string
		var p photoStamp
		if err := rows.Scan(&token, &p.size, &p.mtime); err != nil {
			return nil, err
		}
		p.abs = filepath.Clean(URLPathToAbs(token))
		out = append(out, p)
	}
	return out, rows.Err()
}

func fingerprintsFromDB(nodes []nodePath, photos []photoStamp) []folderFinger {
	out := make([]folderFinger, 0, len(nodes))
	for _, n := range nodes {
		var files []fileStamp
		for _, p := range photos {
			if !hasDirPrefix(p.abs, n.path) {
				continue
			}
			rel, err := filepath.Rel(n.path, p.abs)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}
			files = append(files, fileStamp{rel: filepath.ToSlash(rel), size: p.size, mtime: p.mtime})
		}
		out = append(out, folderFinger{path: n.path, sig: fingerSig(filepath.Base(n.path), files)})
	}
	return out
}

// fingerprintsFromDisk stats images under the new directories once and builds
// a signature for each of them. Hidden and system directories are skipped the
// same way the scanner skips them.
func fingerprintsFromDisk(dirs []string) ([]folderFinger, error) {
	dirSet := make(map[string]bool, len(dirs))
	for _, d := range dirs {
		dirSet[filepath.Clean(d)] = true
	}
	type img struct {
		path  string
		size  int64
		mtime int64
	}
	var imgs []img
	for _, top := range topmostDirs(dirSet) {
		err := filepath.WalkDir(top, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != top && SkipDirName(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !IsImage(d.Name()) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			imgs = append(imgs, img{filepath.Clean(path), info.Size(), info.ModTime().Unix()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	out := make([]folderFinger, 0, len(dirs))
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		var files []fileStamp
		for _, im := range imgs {
			if !hasDirPrefix(im.path, dir) {
				continue
			}
			rel, err := filepath.Rel(dir, im.path)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}
			files = append(files, fileStamp{rel: filepath.ToSlash(rel), size: im.size, mtime: im.mtime})
		}
		out = append(out, folderFinger{path: dir, sig: fingerSig(filepath.Base(dir), files)})
	}
	return out, nil
}

func topmostDirs(set map[string]bool) []string {
	var out []string
	for d := range set {
		if ancestorIn(d, set) {
			continue
		}
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func ancestorIn(path string, set map[string]bool) bool {
	for {
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		if set[parent] {
			return true
		}
		path = parent
	}
}

func hasDirPrefix(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return false
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// applyMove rewrites one moved folder and its descendants onto newPath.
// On conflict the savepoint is rolled back and the error is returned so the
// caller can skip this pair without undoing earlier pairs.
func (sc *Scanner) applyMove(tx *sql.Tx, libraryID string, m moveLink) (int, error) {
	if _, err := tx.Exec(`SAVEPOINT relink_move`); err != nil {
		return 0, err
	}
	n, err := applyMoveInner(tx, libraryID, m)
	if err != nil {
		_, _ = tx.Exec(`ROLLBACK TO relink_move`)
		_, _ = tx.Exec(`RELEASE relink_move`)
		return 0, err
	}
	if _, err := tx.Exec(`RELEASE relink_move`); err != nil {
		return 0, err
	}
	return n, nil
}

func applyMoveInner(tx *sql.Tx, libraryID string, m moveLink) (int, error) {
	nodes, err := loadNodePaths(tx, libraryID)
	if err != nil {
		return 0, err
	}
	type upd struct{ id, path string }
	var updates []upd
	updateIDs := map[string]bool{}
	newOwner := map[string]string{}
	for _, n := range nodes {
		np, ok := rewriteDir(n.path, m.oldPath, m.newPath)
		if !ok {
			continue
		}
		if prev, exists := newOwner[np]; exists {
			return 0, fmt.Errorf("%w: folders %s and %s both map to %s", errMoveConflict, prev, n.id, np)
		}
		newOwner[np] = n.id
		updateIDs[n.id] = true
		updates = append(updates, upd{n.id, np})
	}
	if len(updates) == 0 {
		return 0, fmt.Errorf("%w: no folder row at %s", errMoveConflict, m.oldPath)
	}
	for _, n := range nodes {
		if updateIDs[n.id] {
			continue
		}
		if id, clash := newOwner[filepath.Clean(n.path)]; clash {
			return 0, fmt.Errorf("%w: %s already used by folder %s", errMoveConflict, n.path, id)
		}
	}
	for _, u := range updates {
		if _, err := tx.Exec(`UPDATE nodes SET fs_path = ? WHERE id = ?`, u.path, u.id); err != nil {
			return 0, err
		}
	}

	oldToken := AbsToURLPath(m.oldPath)
	newToken := AbsToURLPath(m.newPath)
	if oldToken == newToken {
		return len(updates), nil
	}
	for _, c := range []struct{ table, col string }{
		{"photo_meta", "photo_path"},
		{"photo_people", "photo_path"},
		{"photo_ratings", "photo_path"},
		{"playlist_items", "photo_path"},
		{"media_quarantine", "photo_path"},
		{"nodes", "cover_photo"},
		{"nodes", "background_photo"},
		{"libraries", "cover_photo"},
		{"libraries", "background_photo"},
		{"playlists", "cover_photo"},
	} {
		if err := rewriteColumn(tx, c.table, c.col, oldToken, newToken); err != nil {
			return 0, err
		}
	}
	if err := rewriteTVPaths(tx, oldToken, newToken); err != nil {
		return 0, err
	}
	return len(updates), nil
}

// rewriteDir returns path with oldRoot swapped for newRoot. path equal to
// oldRoot maps to newRoot. A path that merely shares a string prefix
// (Vacation vs Vacation2) is left alone.
func rewriteDir(path, oldRoot, newRoot string) (string, bool) {
	path = filepath.Clean(path)
	oldRoot = filepath.Clean(oldRoot)
	newRoot = filepath.Clean(newRoot)
	if path == oldRoot {
		return newRoot, true
	}
	prefix := oldRoot + string(filepath.Separator)
	if !strings.HasPrefix(path, prefix) {
		return path, false
	}
	return filepath.Join(newRoot, path[len(prefix):]), true
}

// rewriteColumn replaces oldToken with newToken on a text column. The match is
// the token itself or the token followed by a slash, so a shorter path cannot
// eat a longer sibling path.
func rewriteColumn(tx *sql.Tx, table, column, oldToken, newToken string) error {
	chars := utf8.RuneCountInString(oldToken)
	q := fmt.Sprintf(`
		UPDATE %s SET %s = CASE
			WHEN %s = ? THEN ?
			ELSE ? || substr(%s, ?)
		END
		WHERE %s = ? OR (length(%s) > ? AND substr(%s, 1, ?) = ?)`,
		table, column, column, column, column, column, column)
	_, err := tx.Exec(q,
		oldToken, newToken, newToken, chars+1,
		oldToken, chars, chars+1, oldToken+"/",
	)
	return err
}

func rewriteToken(token, oldToken, newToken string) (string, bool) {
	if token == "" {
		return token, false
	}
	if token == oldToken {
		return newToken, true
	}
	if strings.HasPrefix(token, oldToken+"/") {
		return newToken + token[len(oldToken):], true
	}
	return token, false
}

func rewriteTVPaths(tx *sql.Tx, oldToken, newToken string) error {
	rows, err := tx.Query(`SELECT tv_id, COALESCE(current_path, ''), COALESCE(deck, '') FROM tv_player_state`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct{ id, current, deck string }
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.current, &r.deck); err != nil {
			return err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range pending {
		current, curChanged := rewriteToken(r.current, oldToken, newToken)
		deck := r.deck
		deckChanged := false
		if r.deck != "" {
			lines := strings.Split(r.deck, "\n")
			for i, line := range lines {
				if nw, ok := rewriteToken(line, oldToken, newToken); ok {
					lines[i] = nw
					deckChanged = true
				}
			}
			if deckChanged {
				deck = strings.Join(lines, "\n")
			}
		}
		if !curChanged && !deckChanged {
			continue
		}
		if _, err := tx.Exec(
			`UPDATE tv_player_state SET current_path = ?, deck = ? WHERE tv_id = ?`,
			current, deck, r.id); err != nil {
			return err
		}
	}
	return nil
}

// renameMovedThumbs moves each relinked folder's thumbnail cache directory to
// the new path so the next thumbnail pass sees the files as already generated.
// Missing caches are skipped. A destination that already exists is left alone;
// the scan then generates any thumbnail that is actually absent.
func (sc *Scanner) renameMovedThumbs(moves []moveLink) {
	if sc.thumbs == nil || len(moves) == 0 {
		return
	}
	for _, m := range moves {
		oldC := strings.TrimSuffix(sc.thumbs.CacheDirFor(m.oldPath), string(filepath.Separator))
		newC := strings.TrimSuffix(sc.thumbs.CacheDirFor(m.newPath), string(filepath.Separator))
		if oldC == newC {
			continue
		}
		if _, err := os.Stat(oldC); err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(newC), 0o755); err != nil {
			log.Printf("relink: thumbnail cache dir for %q: %v", m.newPath, err)
			continue
		}
		if _, err := os.Stat(newC); err == nil {
			log.Printf("relink: thumbnail cache %q already exists; leaving %q in place", newC, oldC)
			continue
		}
		if err := os.Rename(oldC, newC); err != nil {
			log.Printf("relink: rename thumbnail cache %q -> %q: %v", oldC, newC, err)
		}
	}
}
