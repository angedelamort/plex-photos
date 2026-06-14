// Package player wires the low-level Samsung Frame TV client
// (plex-photos/frame-tv/tv) into the main app: it stores admin-configured TVs,
// runs the per-TV photo swap loop, and exposes status for the web UI. It is the
// orchestration layer between library playlists and the TV WebSocket client.
package player

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a TV id does not exist.
var ErrNotFound = errors.New("tv not found")

// TV is an admin-configured Samsung Frame TV. Token is never serialized to the
// client; HasToken exposes only whether one has been captured.
type TV struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Matte     string `json:"matte"`
	IntervalS int    `json:"intervalSeconds"`
	// DisplayMode controls how a photo is composed for the 16:9 panel:
	// blur-fill | fill | fit-color | tv-matte. BgColor (#rrggbb) and BorderPct
	// apply to fit-color; Matte applies only to tv-matte.
	DisplayMode string `json:"displayMode"`
	BgColor     string `json:"bgColor"`
	BorderPct   int    `json:"borderPct"`
	// SmartFill crops landscape photos to fill the whole panel (no bars / no
	// matte) on top of DisplayMode; portrait photos keep DisplayMode's behavior.
	SmartFill bool `json:"smartFill"`
	// CaptionFields lists the metadata snippets to overlay (e.g. "date", "gps").
	// Empty means no caption. Persisted as a comma-separated string.
	CaptionFields []string `json:"captionFields"`
	// PlayOrder controls the swap loop's ordering: "sequential" walks the
	// playlist in order; "random" plays a reshuffled deck (each photo once per
	// pass) so nothing repeats early or is starved on large collections.
	PlayOrder string `json:"playOrder"`
	// PhotoFilter is the Art Mode post-process effect applied to each photo
	// off-screen before it is shown (e.g. "Wash", "Pastel"). "none" disables it.
	// This is the Frame's own "painterly" filter, not a baked-in image change.
	PhotoFilter string    `json:"photoFilter"`
	HasToken    bool      `json:"hasToken"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`

	Token string `json:"-"`
}

// State is the persisted runtime state of a TV's swap loop. Transient fields
// (current step, seconds-until-next, last error) live only in the Manager.
type State struct {
	TVID           string
	Owner          string
	PlaylistID     string
	Status         string // stopped | playing | error
	Position       int
	CurrentPath    string
	CurrentContent string
	LastSwapAt     *time.Time
	// Deck is the persisted random play order (photo paths) so a restart
	// resumes the same shuffle from Position instead of reshuffling. Empty for
	// sequential playback, which resumes from Position alone.
	Deck []string
}

// Store persists TVs and their player state in the shared SQLite database.
type Store struct {
	db *sql.DB
}

// NewStore wraps a database handle.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func scanTV(sc interface{ Scan(...any) error }) (*TV, error) {
	var t TV
	var caption string
	if err := sc.Scan(&t.ID, &t.Name, &t.IP, &t.Token, &t.Matte, &t.IntervalS,
		&t.DisplayMode, &t.BgColor, &t.BorderPct, &t.SmartFill, &caption, &t.PlayOrder, &t.PhotoFilter, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.CaptionFields = splitFields(caption)
	t.HasToken = t.Token != ""
	return &t, nil
}

const tvColumns = `id, name, ip, token, matte, interval_s, display_mode, bg_color, border_pct, smart_fill, caption_fields, play_order, photo_filter, created_at, updated_at`

// splitFields parses a comma-separated field list into a clean slice.
func splitFields(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// joinFields serializes the caption field list for storage.
func joinFields(fields []string) string {
	clean := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			clean = append(clean, f)
		}
	}
	return strings.Join(clean, ",")
}

// List returns all configured TVs, ordered by name.
func (s *Store) List() ([]*TV, error) {
	rows, err := s.db.Query(`SELECT ` + tvColumns + ` FROM tvs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TV{}
	for rows.Next() {
		t, err := scanTV(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get returns a single TV by id.
func (s *Store) Get(id string) (*TV, error) {
	t, err := scanTV(s.db.QueryRow(`SELECT `+tvColumns+` FROM tvs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// Create inserts a new TV and returns it.
func (s *Store) Create(tv TV) (*TV, error) {
	id := uuid.NewString()
	if _, err := s.db.Exec(
		`INSERT INTO tvs (id, name, ip, matte, interval_s, display_mode, bg_color, border_pct, smart_fill, caption_fields, play_order, photo_filter)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, tv.Name, tv.IP, tv.Matte, tv.IntervalS, tv.DisplayMode, tv.BgColor, tv.BorderPct,
		tv.SmartFill, joinFields(tv.CaptionFields), tv.PlayOrder, tv.PhotoFilter); err != nil {
		return nil, err
	}
	return s.Get(id)
}

// Update changes a TV's editable fields.
func (s *Store) Update(id string, tv TV) (*TV, error) {
	res, err := s.db.Exec(`
		UPDATE tvs SET name = ?, ip = ?, matte = ?, interval_s = ?,
		    display_mode = ?, bg_color = ?, border_pct = ?, smart_fill = ?, caption_fields = ?, play_order = ?, photo_filter = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		tv.Name, tv.IP, tv.Matte, tv.IntervalS, tv.DisplayMode, tv.BgColor, tv.BorderPct,
		tv.SmartFill, joinFields(tv.CaptionFields), tv.PlayOrder, tv.PhotoFilter, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.Get(id)
}

// Delete removes a TV (and its player state via cascade).
func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM tvs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetToken persists the auth token captured from the TV on connect.
func (s *Store) SetToken(id, token string) error {
	_, err := s.db.Exec(`UPDATE tvs SET token = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, token, id)
	return err
}

// LoadState returns the persisted player state for a TV, or a zero-value
// stopped state when none has been saved yet.
func (s *Store) LoadState(tvID string) (*State, error) {
	st := &State{TVID: tvID, Status: "stopped"}
	var playlist, curPath, curContent, deck sql.NullString
	var lastSwap sql.NullTime
	err := s.db.QueryRow(`
		SELECT owner, playlist_id, status, position, current_path, current_content, last_swap_at, deck
		FROM tv_player_state WHERE tv_id = ?`, tvID).
		Scan(&st.Owner, &playlist, &st.Status, &st.Position, &curPath, &curContent, &lastSwap, &deck)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	st.PlaylistID = playlist.String
	st.CurrentPath = curPath.String
	st.CurrentContent = curContent.String
	st.Deck = splitDeck(deck.String)
	if lastSwap.Valid {
		st.LastSwapAt = &lastSwap.Time
	}
	return st, nil
}

// SaveDeck persists the random play order and the position within it, leaving
// the rest of the player state untouched. It is written only when the deck is
// (re)built—once per shuffle pass—so per-swap writes stay small.
func (s *Store) SaveDeck(tvID string, deck []string, position int) error {
	_, err := s.db.Exec(
		`UPDATE tv_player_state SET deck = ?, position = ?, updated_at = CURRENT_TIMESTAMP WHERE tv_id = ?`,
		strings.Join(deck, "\n"), position, tvID)
	return err
}

// splitDeck parses the newline-joined persisted deck back into photo paths.
func splitDeck(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// SaveState upserts the player state row for a TV.
func (s *Store) SaveState(st *State) error {
	var lastSwap any
	if st.LastSwapAt != nil {
		lastSwap = *st.LastSwapAt
	}
	_, err := s.db.Exec(`
		INSERT INTO tv_player_state
		    (tv_id, owner, playlist_id, status, position, current_path, current_content, last_swap_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(tv_id) DO UPDATE SET
		    owner = excluded.owner,
		    playlist_id = excluded.playlist_id,
		    status = excluded.status,
		    position = excluded.position,
		    current_path = excluded.current_path,
		    current_content = excluded.current_content,
		    last_swap_at = excluded.last_swap_at,
		    updated_at = CURRENT_TIMESTAMP`,
		st.TVID, st.Owner, nullStr(st.PlaylistID), st.Status, st.Position,
		nullStr(st.CurrentPath), nullStr(st.CurrentContent), lastSwap)
	return err
}

// PlayingStates returns the persisted state of every TV whose loop was playing,
// used to resume rotation after a restart.
func (s *Store) PlayingStates() ([]*State, error) {
	rows, err := s.db.Query(`SELECT tv_id FROM tv_player_state WHERE status = 'playing'`)
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
	out := make([]*State, 0, len(ids))
	for _, id := range ids {
		st, err := s.LoadState(id)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
