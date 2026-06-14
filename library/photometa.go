package library

import (
	"fmt"
	"time"
)

// exifDateLayout is the format ReadExif emits for DateTaken.
const exifDateLayout = "2006-01-02 15:04:05"

// photoMetaVersion is the schema version of the data extractPhotoMeta produces.
// Bump it whenever the set of extracted/derived fields changes so a rescan
// re-indexes rows written by an older extractor, even when the source file's
// mtime/size are unchanged (see Scanner.indexPhotoMeta).
const photoMetaVersion = 1

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
	PhotoPath     string
	LibraryID     string
	TakenAt       time.Time // zero when unknown
	Year          int       // 0 when unknown
	Lat           float64
	Lon           float64
	HasGPS        bool
	PlaceCity     string
	PlaceProvince string
	PlaceCountry  string
	Camera        string
	Lens          string
	Exposure      string
	Aperture      string
	ISO           string
	FocalLength   string
	Width         int
	Height        int
	Orientation   string
	HasSidecar    bool
	People        []string
	FileMtime     int64
	FileSize      int64
	// MetaVersion is the photoMetaVersion the row was indexed under; lets the
	// handler distinguish a fully-indexed row from one predating newer fields.
	MetaVersion int
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
		m.Camera = ex.Camera
		m.Lens = ex.Lens
		m.Exposure = ex.Exposure
		m.Aperture = ex.Aperture
		m.ISO = ex.ISO
		m.FocalLength = ex.FocalLength
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
		m.PlaceCity, m.PlaceProvince, m.PlaceCountry = PlaceParts(m.Lat, m.Lon)
	}
	m.MetaVersion = photoMetaVersion

	return m
}

// toExifInfo rebuilds the on-the-wire ExifInfo from the indexed row, so the
// photo info panel can be served entirely from the DB without re-reading the
// file. The derived DateTaken/GPS strings mirror what ReadExif produces.
func (m *PhotoMeta) toExifInfo() *ExifInfo {
	info := &ExifInfo{
		Camera:      m.Camera,
		Lens:        m.Lens,
		Width:       m.Width,
		Height:      m.Height,
		Exposure:    m.Exposure,
		Aperture:    m.Aperture,
		ISO:         m.ISO,
		FocalLength: m.FocalLength,
		Lat:         m.Lat,
		Lon:         m.Lon,
		HasGPS:      m.HasGPS,
	}
	if !m.TakenAt.IsZero() {
		info.DateTaken = m.TakenAt.Format(exifDateLayout)
	}
	if m.HasGPS {
		info.GPS = fmt.Sprintf("%.5f, %.5f", m.Lat, m.Lon)
	}
	return info
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
