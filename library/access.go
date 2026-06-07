package library

// CanAccessLibrary reports whether the given user may access the library.
// Admins always have access; others must be on the whitelist.
func (s *Store) CanAccessLibrary(libraryID, username string, isAdmin bool) (bool, error) {
	if isAdmin {
		return true, nil
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM library_access WHERE library_id = ? AND plex_username = ?`,
		libraryID, username).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// CanAccessNode checks access to the library that ultimately owns the node.
func (s *Store) CanAccessNode(nodeID, username string, isAdmin bool) (bool, error) {
	if isAdmin {
		return true, nil
	}
	n, err := s.GetNode(nodeID)
	if err != nil {
		return false, err
	}
	return s.CanAccessLibrary(n.LibraryID, username, isAdmin)
}

// CanAccessPhotoPath checks whether a user may access a photo at the given
// absolute path by verifying it falls under an accessible library root. This is
// the sole confinement guard for photo/thumb/exif requests.
func (s *Store) CanAccessPhotoPath(fullPath, username string, isAdmin bool) (bool, error) {
	libs, err := s.ListLibrariesForUser(username, isAdmin)
	if err != nil {
		return false, err
	}
	for _, lib := range libs {
		if underRoot(lib.RootPath, fullPath) {
			return true, nil
		}
	}
	return false, nil
}
