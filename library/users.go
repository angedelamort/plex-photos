package library

import (
	"database/sql"
	"errors"
	"time"
)

// User is a known Plex account that has signed in or been granted access.
type User struct {
	Username   string     `json:"username"`
	Email      string     `json:"email"`
	IsAdmin    bool       `json:"isAdmin"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastSeen   *time.Time `json:"lastSeen,omitempty"`
	LibraryIDs []string   `json:"libraryIds"`
}

// TouchUser records a sign-in: it inserts the user if unknown and updates the
// admin flag, email (when provided) and last-seen timestamp. This keeps the
// users table in sync with whoever actually logs in via Plex.
func (s *Store) TouchUser(username, email string, isAdmin bool) error {
	if username == "" {
		return nil
	}
	admin := 0
	if isAdmin {
		admin = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO users (plex_username, email, is_admin, last_seen)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(plex_username) DO UPDATE SET
			is_admin = excluded.is_admin,
			email = CASE WHEN excluded.email != '' THEN excluded.email ELSE users.email END,
			last_seen = CURRENT_TIMESTAMP`,
		username, email, admin)
	return err
}

// AddUser creates (or revives) a user record by Plex username so an admin can
// grant library access before the user has ever signed in.
func (s *Store) AddUser(username, email string) (*User, error) {
	if username == "" {
		return nil, errors.New("username is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO users (plex_username, email) VALUES (?, ?)
		ON CONFLICT(plex_username) DO UPDATE SET
			email = CASE WHEN excluded.email != '' THEN excluded.email ELSE users.email END`,
		username, email)
	if err != nil {
		return nil, err
	}
	return s.GetUser(username)
}

// GetUser fetches a single user with their accessible library ids.
func (s *Store) GetUser(username string) (*User, error) {
	var u User
	var email sql.NullString
	var admin int
	var lastSeen sql.NullTime
	err := s.db.QueryRow(`SELECT plex_username, COALESCE(email, ''), is_admin, created_at, last_seen FROM users WHERE plex_username = ?`, username).
		Scan(&u.Username, &email, &admin, &u.CreatedAt, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Email = email.String
	u.IsAdmin = admin != 0
	if lastSeen.Valid {
		u.LastSeen = &lastSeen.Time
	}
	if u.LibraryIDs, err = s.UserLibraryIDs(username); err != nil {
		return nil, err
	}
	return &u, nil
}

// ListUsers returns all known users (those who signed in or were added) plus
// any usernames that appear only in library_access (legacy whitelist entries),
// each with their accessible library ids.
func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(`
		SELECT plex_username FROM users
		UNION
		SELECT DISTINCT plex_username FROM library_access
		ORDER BY plex_username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]*User, 0, len(names))
	for _, n := range names {
		u, err := s.GetUser(n)
		if errors.Is(err, ErrNotFound) {
			// Username present only in library_access: synthesize a record.
			ids, lerr := s.UserLibraryIDs(n)
			if lerr != nil {
				return nil, lerr
			}
			out = append(out, &User{Username: n, LibraryIDs: ids})
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// DeleteUser removes a user and all of their library access grants.
func (s *Store) DeleteUser(username string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM library_access WHERE plex_username = ?`, username); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM users WHERE plex_username = ?`, username); err != nil {
		return err
	}
	return tx.Commit()
}

// UserLibraryIDs returns the ids of libraries the user has been granted access to.
func (s *Store) UserLibraryIDs(username string) ([]string, error) {
	rows, err := s.db.Query(`SELECT library_id FROM library_access WHERE plex_username = ? ORDER BY library_id`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetUserLibraryAccess replaces the full set of libraries a user can access.
func (s *Store) SetUserLibraryAccess(username string, libraryIDs []string) error {
	if username == "" {
		return errors.New("username is required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM library_access WHERE plex_username = ?`, username); err != nil {
		return err
	}
	for _, id := range libraryIDs {
		if id == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO library_access (library_id, plex_username) VALUES (?, ?)`, id, username); err != nil {
			return err
		}
	}
	return tx.Commit()
}
