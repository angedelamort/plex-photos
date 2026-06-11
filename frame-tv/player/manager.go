package player

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"plex-photos/frame-tv/tv"
	"plex-photos/library"
)

// Playback ordering for a TV's swap loop.
const (
	OrderSequential = "sequential" // walk the playlist in its stored order
	OrderRandom     = "random"     // play a reshuffled deck, each photo once per pass
)

// photoSource is the slice of library.Store the player needs: read a playlist's
// ordered photos and resolve a photo token to an absolute file path.
type photoSource interface {
	PlaylistPhotos(owner, id string) ([]library.PlaylistPhoto, error)
	ResolvePhotoFile(path string) (string, error)
}

// artConn is the subset of *tv.ArtClient the swap loop uses. It is an interface
// so tests can inject a fake TV.
type artConn interface {
	Upload(data []byte, fileType, matteID string) (string, error)
	SelectImage(contentID string, show bool) error
	DeleteImages(contentIDs ...string) error
	Token() string
	Close() error
}

// artDialer opens a connection to a TV. Defaults to tv.DialArt.
type artDialer func(ctx context.Context, ip, token string) (artConn, error)

func defaultDialer(ctx context.Context, ip, token string) (artConn, error) {
	c, err := tv.DialArt(ctx, ip, token)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Snapshot is the live status of a TV's swap loop, surfaced to the web UI.
type Snapshot struct {
	TVID        string     `json:"tvId"`
	Status      string     `json:"status"` // stopped | playing | error
	Step        string     `json:"step"`   // idle | processing | uploading | selecting | waiting | error
	PlaylistID  string     `json:"playlistId,omitempty"`
	CurrentPath string     `json:"currentPath,omitempty"`
	CurrentName string     `json:"currentName,omitempty"`
	NextPath    string     `json:"nextPath,omitempty"`
	NextName    string     `json:"nextName,omitempty"`
	Position    int        `json:"position"`
	Total       int        `json:"total"`
	IntervalS   int        `json:"intervalSeconds"`
	LastSwapAt  *time.Time `json:"lastSwapAt,omitempty"`
	NextSwapAt  *time.Time `json:"nextSwapAt,omitempty"`
	Error       string     `json:"error,omitempty"`
	// Resume hints for a stopped TV: whether saved progress exists, which
	// playlist it belongs to, and that playlist's photo count, so the UI can
	// offer "Resume (n/total)" versus a fresh "Play".
	Resumable        bool   `json:"resumable,omitempty"`
	ResumePlaylistID string `json:"resumePlaylistId,omitempty"`
	ResumeTotal      int    `json:"resumeTotal,omitempty"`
}

// runner owns a single TV's swap goroutine and its live snapshot.
type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
	skip   chan struct{}

	mu       sync.Mutex
	stopping bool
	snap     Snapshot
}

func (r *runner) update(fn func(*Snapshot)) {
	r.mu.Lock()
	fn(&r.snap)
	r.mu.Unlock()
}

func (r *runner) snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snap
}

func (r *runner) markStopping() {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
}

func (r *runner) isStopping() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopping
}

// Manager runs the per-TV swap loops and tracks their live status.
type Manager struct {
	store *Store
	lib   photoSource
	dial  artDialer

	baseCtx    context.Context
	baseCancel context.CancelFunc

	mu      sync.Mutex
	runners map[string]*runner
}

// NewManager builds a Manager. lib is satisfied by *library.Store.
func NewManager(store *Store, lib photoSource) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		store:      store,
		lib:        lib,
		dial:       defaultDialer,
		baseCtx:    ctx,
		baseCancel: cancel,
		runners:    map[string]*runner{},
	}
}

// Play starts (or restarts) rotation of a playlist on a TV.
func (m *Manager) Play(tvID, owner, playlistID string) error {
	if _, err := m.store.Get(tvID); err != nil {
		return err
	}
	photos, err := m.lib.PlaylistPhotos(owner, playlistID)
	if err != nil {
		return err
	}
	if len(photos) == 0 {
		return fmt.Errorf("playlist has no photos")
	}

	// Cancel any existing runner (its defer persists nothing playing-relevant
	// once stopping); we then immediately mark the new intent as playing.
	m.cancelRunner(tvID)

	st := &State{TVID: tvID, Owner: owner, PlaylistID: playlistID, Status: "playing", Position: 0}
	if err := m.store.SaveState(st); err != nil {
		return err
	}

	m.startRunner(tvID, owner, playlistID, 0, "", nil)
	return nil
}

// Stop halts rotation on a TV and persists the stopped state.
func (m *Manager) Stop(tvID string) error {
	if m.cancelRunner(tvID) {
		// The goroutine's defer persisted stopped (stopping was set).
		return nil
	}
	st, err := m.store.LoadState(tvID)
	if err != nil {
		return err
	}
	st.Status = "stopped"
	return m.store.SaveState(st)
}

// Skip advances to the next photo immediately (only while playing).
func (m *Manager) Skip(tvID string) error {
	m.mu.Lock()
	r := m.runners[tvID]
	m.mu.Unlock()
	if r == nil {
		return fmt.Errorf("tv is not playing")
	}
	select {
	case r.skip <- struct{}{}:
	default:
	}
	return nil
}

// Status returns the live snapshot for a TV (or one derived from persisted
// state when its loop is not running).
func (m *Manager) Status(tvID string) (Snapshot, error) {
	m.mu.Lock()
	r := m.runners[tvID]
	m.mu.Unlock()
	if r != nil {
		return r.snapshot(), nil
	}

	st, err := m.store.LoadState(tvID)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		TVID:        tvID,
		Status:      st.Status,
		Step:        "idle",
		PlaylistID:  st.PlaylistID,
		CurrentPath: st.CurrentPath,
		CurrentName: baseName(st.CurrentPath),
		Position:    st.Position,
		LastSwapAt:  st.LastSwapAt,
	}
	if snap.Status == "" {
		snap.Status = "stopped"
	}
	if tvCfg, err := m.store.Get(tvID); err == nil {
		snap.IntervalS = tvCfg.IntervalS
	}
	// The loop isn't running: if we have a saved playlist, offer to resume from
	// the saved position/deck rather than restart from the first photo.
	if st.PlaylistID != "" && st.Owner != "" {
		snap.Resumable = true
		snap.ResumePlaylistID = st.PlaylistID
		if photos, err := m.lib.PlaylistPhotos(st.Owner, st.PlaylistID); err == nil {
			snap.ResumeTotal = len(photos)
		}
	}
	return snap, nil
}

// Resume continues a stopped TV from its saved position and shuffle deck
// instead of restarting. Use this (rather than Play) after a manual Stop so the
// rotation picks up where it left off. Returns an error if nothing is saved.
func (m *Manager) Resume(tvID string) error {
	if _, err := m.store.Get(tvID); err != nil {
		return err
	}
	m.cancelRunner(tvID)
	st, err := m.store.LoadState(tvID)
	if err != nil {
		return err
	}
	if st.PlaylistID == "" || st.Owner == "" {
		return fmt.Errorf("nothing to resume")
	}
	// SaveState preserves the persisted deck (its upsert omits that column) and
	// keeps the saved position, so the runner picks up exactly where it stopped.
	st.Status = "playing"
	if err := m.store.SaveState(st); err != nil {
		return err
	}
	m.startRunner(tvID, st.Owner, st.PlaylistID, st.Position, st.CurrentContent, st.Deck)
	return nil
}

// Recover resumes rotation for TVs that were playing before a restart.
func (m *Manager) Recover() {
	states, err := m.store.PlayingStates()
	if err != nil {
		log.Printf("frame-tv: recover: %v", err)
		return
	}
	for _, st := range states {
		if st.PlaylistID == "" || st.Owner == "" {
			continue
		}
		m.startRunner(st.TVID, st.Owner, st.PlaylistID, st.Position, st.CurrentContent, st.Deck)
		log.Printf("frame-tv: resumed playback on TV %s", st.TVID)
	}
}

// Shutdown stops all loops without changing their persisted status, so playing
// TVs resume on the next start.
func (m *Manager) Shutdown() {
	m.baseCancel()
	m.mu.Lock()
	runners := m.runners
	m.runners = map[string]*runner{}
	m.mu.Unlock()
	for _, r := range runners {
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
		}
	}
}

// cancelRunner removes and cancels a runner (marking it stopping so its defer
// persists stopped). Returns whether one was running.
func (m *Manager) cancelRunner(tvID string) bool {
	m.mu.Lock()
	r := m.runners[tvID]
	delete(m.runners, tvID)
	m.mu.Unlock()
	if r == nil {
		return false
	}
	r.markStopping()
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(35 * time.Second):
	}
	return true
}

func (m *Manager) startRunner(tvID, owner, playlistID string, startPos int, prevContent string, initialDeck []string) {
	ctx, cancel := context.WithCancel(m.baseCtx)
	r := &runner{
		cancel: cancel,
		done:   make(chan struct{}),
		skip:   make(chan struct{}, 1),
		snap:   Snapshot{TVID: tvID, Status: "playing", Step: "idle", PlaylistID: playlistID},
	}
	m.mu.Lock()
	m.runners[tvID] = r
	m.mu.Unlock()
	go m.run(ctx, r, tvID, owner, playlistID, startPos, prevContent, initialDeck)
}

// staged is a photo that has already been composed and uploaded to the TV's
// "My Photos" and is waiting to be selected. Pre-uploading during the idle
// interval means a swap is just a select_image call against fully-ingested
// content, which avoids the multi-second black screen that comes from
// selecting an image the moment it lands on the TV.
type staged struct {
	pos       int
	path      string // playlist photo token, to detect a changed playlist
	contentID string // content_id returned by the TV upload
	fp        string // display-settings fingerprint the JPEG was composed for
}

func (m *Manager) run(ctx context.Context, r *runner, tvID, owner, playlistID string, startPos int, prevContent string, initialDeck []string) {
	defer func() {
		// Persist the stopped status (when this exit is due to Stop) BEFORE
		// signaling done, so a caller waiting on done observes the final state.
		if r.isStopping() {
			if st, err := m.store.LoadState(tvID); err == nil {
				st.Status = "stopped"
				_ = m.store.SaveState(st)
			}
		}
		close(r.done)
	}()

	pos := startPos
	corner := 0     // rotates per prepared image to avoid uneven panel wear
	var stg *staged // next image, already uploaded and ready to select

	// deck is the playback order for the current playlist: identical to the
	// playlist for sequential mode, a shuffled copy for random. It is rebuilt
	// when the playlist contents or the order mode change (deckSig), and—for
	// random—reshuffled each time it is exhausted so every photo shows once per
	// pass with a fresh order and no early repeats.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var deck []library.PlaylistPhoto
	var deckSig uint64
	resumePaths := initialDeck // persisted shuffle to resume, consumed once
	for {
		if ctx.Err() != nil {
			return
		}

		tvCfg, err := m.store.Get(tvID)
		if err != nil {
			m.fail(r, "load tv config: "+err.Error())
			if !sleep(ctx, r, 30*time.Second) {
				return
			}
			continue
		}

		photos, err := m.lib.PlaylistPhotos(owner, playlistID)
		if err != nil || len(photos) == 0 {
			msg := "playlist has no photos"
			if err != nil {
				msg = err.Error()
			}
			m.fail(r, msg)
			if !sleep(ctx, r, backoff(tvCfg)) {
				return
			}
			continue
		}
		// (Re)build the play-order deck when the playlist or order mode changes.
		random := tvCfg.PlayOrder == OrderRandom
		if sig := orderSig(random, photos); deck == nil || sig != deckSig {
			var nd []library.PlaylistPhoto
			// On the first build, resume the persisted shuffle if it still
			// matches the playlist, so a restart continues the same deck from
			// its saved position instead of reshuffling.
			if random && len(resumePaths) > 0 {
				if d, ok := deckFromPaths(resumePaths, photos); ok {
					nd = d
				}
			}
			if nd == nil {
				nd = buildDeck(photos, random, rng, "")
				if random {
					_ = m.store.SaveDeck(tvID, deckPaths(nd), pos)
				}
			}
			deck = nd
			deckSig = sig
			resumePaths = nil
		}
		if pos >= len(deck) {
			pos = 0
		}
		cur := deck[pos]
		next := deck[(pos+1)%len(deck)]
		fp := displayFingerprint(tvCfg)
		interval := time.Duration(tvCfg.IntervalS) * time.Second
		if interval <= 0 {
			interval = time.Hour
		}

		r.update(func(s *Snapshot) {
			s.Status = "playing"
			s.Error = ""
			s.PlaylistID = playlistID
			s.CurrentPath = cur.Path
			s.CurrentName = displayName(cur)
			s.NextPath = next.Path
			s.NextName = displayName(next)
			s.Position = pos
			s.Total = len(photos)
			s.IntervalS = tvCfg.IntervalS
		})

		conn, err := m.connect(ctx, tvCfg)
		if err != nil {
			m.fail(r, err.Error())
			if !sleep(ctx, r, backoff(tvCfg)) {
				return
			}
			continue
		}

		// In steady state the image for this position was pre-uploaded at the
		// end of the previous cycle. Re-upload only when we have no valid
		// staged content (first run, resume, a changed playlist, or edited
		// display settings) — and surface that work as processing/uploading.
		if stg == nil || stg.pos != pos || stg.path != cur.Path || stg.fp != fp {
			contentID, err := m.prepareUpload(r, conn, tvCfg, cur, corner, fp, true)
			if err != nil {
				conn.Close()
				stg = nil
				m.fail(r, err.Error())
				if !sleep(ctx, r, backoff(tvCfg)) {
					return
				}
				continue // retry the same photo
			}
			stg = &staged{pos: pos, path: cur.Path, contentID: contentID, fp: fp}
			corner = (corner + 1) % 4
		}

		// The swap itself: just point the TV at the already-uploaded image.
		r.update(func(s *Snapshot) { s.Step = "selecting" })
		if err := conn.SelectImage(stg.contentID, true); err != nil {
			conn.Close()
			stg = nil
			m.fail(r, fmt.Sprintf("select: %v", err))
			if !sleep(ctx, r, backoff(tvCfg)) {
				return
			}
			continue
		}

		now := time.Now()
		// Now that the swap completed, clean up the previously shown image.
		if prevContent != "" && prevContent != stg.contentID {
			_ = conn.DeleteImages(prevContent) // best effort
		}
		prevContent = stg.contentID
		_ = m.store.SaveState(&State{
			TVID: tvID, Owner: owner, PlaylistID: playlistID, Status: "playing",
			Position: pos, CurrentPath: cur.Path, CurrentContent: stg.contentID, LastSwapAt: &now,
		})

		nextSwap := now.Add(interval)
		r.update(func(s *Snapshot) {
			s.Step = "waiting"
			s.Error = ""
			s.LastSwapAt = &now
			s.NextSwapAt = &nextSwap
		})

		// Advance, then pre-upload the next image on this same connection so it
		// is fully ingested by the TV long before the next swap. A preload
		// failure is non-fatal: the next cycle just falls back to uploading
		// inline (and retries via backoff if that fails too).
		pos++
		if pos >= len(deck) {
			pos = 0
			if random {
				// Deck exhausted: reshuffle for the next pass, keeping the just
				// shown photo off the front to avoid a back-to-back repeat, and
				// persist the new pass so a restart resumes it from the start.
				deck = buildDeck(photos, random, rng, cur.Path)
				deckSig = orderSig(random, photos)
				_ = m.store.SaveDeck(tvID, deckPaths(deck), 0)
			}
		}
		stg = nil
		nphoto := deck[pos]
		if contentID, perr := m.prepareUpload(r, conn, tvCfg, nphoto, corner, fp, false); perr != nil {
			log.Printf("frame-tv: preload next photo: %v", perr)
		} else {
			stg = &staged{pos: pos, path: nphoto.Path, contentID: contentID, fp: fp}
			corner = (corner + 1) % 4
		}
		conn.Close()

		if !sleep(ctx, r, interval) {
			return
		}
	}
}

// connect opens a TV connection and persists any auth token the TV issues.
func (m *Manager) connect(ctx context.Context, tvCfg *TV) (artConn, error) {
	conn, err := m.dial(ctx, tvCfg.IP, tvCfg.Token)
	if err != nil {
		return nil, fmt.Errorf("connect TV: %w", err)
	}
	if tok := conn.Token(); tok != "" && tok != tvCfg.Token {
		_ = m.store.SetToken(tvCfg.ID, tok)
	}
	return conn, nil
}

// prepareUpload composes a photo for the panel and uploads it to the TV,
// returning its new content_id. It does not select the image. When track is
// true the processing/uploading steps are surfaced in the snapshot; preloads
// pass false so they happen silently while the current photo stays on screen.
func (m *Manager) prepareUpload(r *runner, conn artConn, tvCfg *TV, photo library.PlaylistPhoto, corner int, fp string, track bool) (string, error) {
	if track {
		r.update(func(s *Snapshot) { s.Step = "processing" })
	}
	abs, err := m.lib.ResolvePhotoFile(photo.Path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", displayName(photo), err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", displayName(photo), err)
	}
	res, err := prepareJPEG(raw, DisplayOptions{
		Mode:          tvCfg.DisplayMode,
		BgColor:       tvCfg.BgColor,
		BorderPct:     tvCfg.BorderPct,
		SmartFill:     tvCfg.SmartFill,
		Caption:       buildCaptionLines(tvCfg.CaptionFields, abs, photo),
		CaptionCorner: corner,
	})
	if err != nil {
		return "", fmt.Errorf("process %s: %w", displayName(photo), err)
	}

	if track {
		r.update(func(s *Snapshot) { s.Step = "uploading" })
	}
	// We bake the framing into the JPEG for every mode except tv-matte, where
	// the TV draws its own matte from the matte id. "auto" picks a mat color
	// per photo for the best contrast. When the composed image already fills the
	// panel (e.g. a landscape photo cropped by smart fill), force matte "none"
	// so the TV doesn't frame an already full-bleed image.
	matte := "none"
	if tvCfg.DisplayMode == ModeTVMatte && !res.FullPanel {
		matte = tvCfg.Matte
		if matte == MatteAuto {
			matte = autoMatte(raw)
		}
	}
	contentID, err := conn.Upload(res.JPEG, "jpg", matte)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	return contentID, nil
}

// buildDeck returns the playback order for a playlist. Sequential mode returns
// a copy in playlist order; random mode returns a Fisher-Yates shuffle. When
// avoidFirst is set (a reshuffle at a pass boundary) and it lands first, it is
// swapped to the back so the same photo never shows twice in a row.
func buildDeck(photos []library.PlaylistPhoto, random bool, rng *rand.Rand, avoidFirst string) []library.PlaylistPhoto {
	deck := make([]library.PlaylistPhoto, len(photos))
	copy(deck, photos)
	if !random || len(deck) < 2 {
		return deck
	}
	rng.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })
	if avoidFirst != "" && deck[0].Path == avoidFirst {
		deck[0], deck[len(deck)-1] = deck[len(deck)-1], deck[0]
	}
	return deck
}

// deckFromPaths rebuilds a deck from persisted photo paths, mapping each back
// to its current playlist entry (to recover the display name for captions). ok
// is false when the paths no longer match the playlist's photo set exactly
// (photos added, removed, or reordered), so the caller starts a fresh shuffle.
func deckFromPaths(savedPaths []string, photos []library.PlaylistPhoto) ([]library.PlaylistPhoto, bool) {
	if len(savedPaths) != len(photos) {
		return nil, false
	}
	byPath := make(map[string]library.PlaylistPhoto, len(photos))
	for _, p := range photos {
		byPath[p.Path] = p
	}
	deck := make([]library.PlaylistPhoto, 0, len(savedPaths))
	for _, sp := range savedPaths {
		p, ok := byPath[sp]
		if !ok {
			return nil, false
		}
		deck = append(deck, p)
	}
	return deck, true
}

// deckPaths extracts the ordered photo paths from a deck for persistence.
func deckPaths(deck []library.PlaylistPhoto) []string {
	out := make([]string, len(deck))
	for i, p := range deck {
		out[i] = p.Path
	}
	return out
}

// orderSig is a cheap fingerprint of the order mode plus the playlist's photo
// set, used to detect when the deck must be rebuilt.
func orderSig(random bool, photos []library.PlaylistPhoto) uint64 {
	h := fnv.New64a()
	if random {
		_, _ = h.Write([]byte{'r'})
	} else {
		_, _ = h.Write([]byte{'s'})
	}
	for _, p := range photos {
		_, _ = h.Write([]byte(p.Path))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

// displayFingerprint captures the settings that affect how a photo is composed
// and matted. When it changes (the admin edited the TV), any image staged under
// the old settings is discarded and re-uploaded so the swap reflects the edit.
func displayFingerprint(tvCfg *TV) string {
	return strings.Join([]string{
		tvCfg.DisplayMode,
		tvCfg.BgColor,
		fmt.Sprintf("%d", tvCfg.BorderPct),
		tvCfg.Matte,
		strings.Join(tvCfg.CaptionFields, ","),
	}, "|")
}

func (m *Manager) fail(r *runner, msg string) {
	log.Printf("frame-tv: %s", msg)
	r.update(func(s *Snapshot) {
		s.Step = "error"
		s.Error = msg
	})
}

// sleep waits for d, returning false if the loop should exit (ctx cancelled).
// A skip signal returns true early.
func sleep(ctx context.Context, r *runner, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-r.skip:
		return true
	case <-t.C:
		return true
	}
}

// backoff is the retry delay after a failure: the swap interval, capped to
// [5s, 30s] so a temporarily-off TV recovers without hammering it.
func backoff(tvCfg *TV) time.Duration {
	d := time.Duration(tvCfg.IntervalS) * time.Second
	if d <= 0 || d > 30*time.Second {
		d = 30 * time.Second
	}
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	return d
}

// Caption field keys (must match the frontend + handler whitelist).
const (
	capDate     = "date"
	capYear     = "year"
	capCamera   = "camera"
	capLocation = "location"
	capFilename = "filename"
	capAlbum    = "album"
)

// buildCaptionLines turns the enabled caption fields into the text lines drawn
// on the photo. EXIF is read only when a field needs it; missing data yields
// fewer lines rather than an error.
func buildCaptionLines(fields []string, abs string, photo library.PlaylistPhoto) []string {
	if len(fields) == 0 {
		return nil
	}
	on := make(map[string]bool, len(fields))
	for _, f := range fields {
		on[f] = true
	}

	var ex *library.ExifInfo
	if on[capDate] || on[capYear] || on[capCamera] || on[capLocation] {
		ex, _ = library.ReadExif(abs)
	}

	var lines []string
	if on[capAlbum] {
		if f := folderName(photo.Path); f != "" {
			lines = append(lines, f)
		}
	}
	if on[capFilename] {
		lines = append(lines, displayName(photo))
	}
	if ex != nil && ex.DateTaken != "" {
		if on[capDate] {
			lines = append(lines, ex.DateTaken)
		} else if on[capYear] {
			lines = append(lines, yearOf(ex.DateTaken))
		}
	}
	if on[capCamera] && ex != nil {
		if c := cameraLine(ex); c != "" {
			lines = append(lines, c)
		}
	}
	if on[capLocation] && ex != nil && ex.HasGPS {
		if place := library.PlaceName(ex.Lat, ex.Lon); place != "" {
			lines = append(lines, place)
		}
	}
	return lines
}

// cameraLine combines the camera model and shooting settings into one line.
func cameraLine(ex *library.ExifInfo) string {
	parts := []string{}
	for _, p := range []string{ex.Camera, ex.Aperture, ex.Exposure, isoLabel(ex.ISO), ex.FocalLength} {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

func isoLabel(iso string) string {
	iso = strings.TrimSpace(iso)
	if iso == "" {
		return ""
	}
	return "ISO " + iso
}

// yearOf extracts the leading year from a "2006-01-02 ..." date string.
func yearOf(date string) string {
	if len(date) >= 4 {
		return date[:4]
	}
	return date
}

// folderName returns the immediate parent folder name of a photo URL token.
func folderName(p string) string {
	p = strings.TrimRight(filepath.ToSlash(p), "/")
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return ""
	}
	dir := p[:i]
	if j := strings.LastIndex(dir, "/"); j >= 0 {
		return dir[j+1:]
	}
	return dir
}

func displayName(p library.PlaylistPhoto) string {
	if strings.TrimSpace(p.Name) != "" {
		return p.Name
	}
	return baseName(p.Path)
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
