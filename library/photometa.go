package library

import (
	"time"
)

// exifDateLayout is the format ReadExif emits for DateTaken.
const exifDateLayout = "2006-01-02 15:04:05"

// Orientation values derived from a photo's pixel dimensions.
const (
	OrientationPortrait  = "portrait"
	OrientationLandscape = "landscape"
	OrientationSquare    = "square"
)

// PhotoMeta is the indexed, per-photo metadata persisted to photo_meta /
// photo_people. PhotoPath, LibraryID, FileMtime and FileSize are set by the
// scan hook; extractPhotoMeta fills the content fields read from the image and
// its sidecar.
type PhotoMeta struct {
	PhotoPath    string
	LibraryID    string
	TakenAt      time.Time // zero when unknown
	Year         int       // 0 when unknown
	Lat          float64
	Lon          float64
	HasGPS       bool
	PlaceCity    string
	PlaceCountry string
	Width        int
	Height       int
	Orientation  string
	HasSidecar   bool
	People       []string
	FileMtime    int64
	FileSize     int64
}

// extractPhotoMeta reads metadata for the photo at abs by merging EXIF (pixel
// dimensions, capture date, GPS) with the Google Takeout sidecar JSON (person
// tags, and date/GPS fallbacks). When coordinates are available it reverse
// geocodes them to city/country. It never errors: missing data simply leaves
// the corresponding fields zero/empty.
func extractPhotoMeta(abs string) PhotoMeta {
	var m PhotoMeta

	if ex, err := ReadExif(abs); err == nil && ex != nil {
		m.Width = ex.Width
		m.Height = ex.Height
		if ex.HasGPS {
			m.Lat, m.Lon, m.HasGPS = ex.Lat, ex.Lon, true
		}
		if ex.DateTaken != "" {
			if t, perr := time.Parse(exifDateLayout, ex.DateTaken); perr == nil {
				m.TakenAt = t
			}
		}
	}

	// Layer the sidecar: people always come from here, and it supplies date /
	// GPS when EXIF lacked them (common for scans, screenshots, edited files).
	if g, ok := parseSidecar(abs); ok {
		m.HasSidecar = true
		m.People = g.People
		if m.TakenAt.IsZero() && !g.TakenAt.IsZero() {
			m.TakenAt = g.TakenAt
		}
		if !m.HasGPS && g.HasGPS {
			m.Lat, m.Lon, m.HasGPS = g.Lat, g.Lon, true
		}
	}

	if !m.TakenAt.IsZero() {
		m.Year = m.TakenAt.Year()
	}
	m.Orientation = orientationOf(m.Width, m.Height)
	if m.HasGPS {
		m.PlaceCity, m.PlaceCountry = PlaceParts(m.Lat, m.Lon)
	}

	return m
}

// orientationOf classifies pixel dimensions. Unknown dimensions (0) yield "".
func orientationOf(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	switch {
	case h > w:
		return OrientationPortrait
	case w > h:
		return OrientationLandscape
	default:
		return OrientationSquare
	}
}
