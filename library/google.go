package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GoogleMeta is the subset of a Google Takeout sidecar JSON we care about.
// Empty/zero fields mean the sidecar omitted them.
type GoogleMeta struct {
	Title       string
	Description string
	// TakenAt is the photoTakenTime, the most reliable capture date. Zero when
	// the sidecar lacked a (parseable) photoTakenTime.timestamp.
	TakenAt time.Time
	// Lat/Lon come from geoData. HasGPS is false for a missing fix or the 0,0
	// placeholder Google writes when it has no location (see parseSidecar).
	Lat    float64
	Lon    float64
	HasGPS bool
	// People are the tagged person names (Takeout's people[].name).
	People []string
}

// rawSidecar mirrors the on-disk JSON structure.
type rawSidecar struct {
	Title          string `json:"title"`
	Description    string `json:"description"`
	PhotoTakenTime struct {
		Timestamp string `json:"timestamp"`
	} `json:"photoTakenTime"`
	CreationTime struct {
		Timestamp string `json:"timestamp"`
	} `json:"creationTime"`
	GeoData struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"geoData"`
	People []struct {
		Name string `json:"name"`
	} `json:"people"`
}

// findSidecar locates the Google Takeout sidecar JSON for a photo. Takeout
// names it "<photo filename>.<suffix>.json" where <suffix> is some (often
// truncated) form of "supplemental-metadata" - e.g.
//
//	IMG.jpg.supplemental-metadata.json
//	IMG.jpg.supplemental-met.json
//	IMG.jpg.sup.json
//
// It also occasionally appears as exactly "<photo filename>.json". We match the
// exact form first, then any same-directory entry that starts with the full
// photo filename plus '.' and ends with '.json'. Returns "" when none is found.
//
// Known limitation: Google also truncates very long *base* filenames in the
// sidecar name; those are not matched here.
func findSidecar(absPhotoPath string) string {
	dir := filepath.Dir(absPhotoPath)
	base := filepath.Base(absPhotoPath)

	exact := absPhotoPath + ".json"
	if fi, err := os.Stat(exact); err == nil && !fi.IsDir() {
		return exact
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	prefix := base + "."
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".json") {
			return filepath.Join(dir, name)
		}
	}
	return ""
}

// parseSidecar reads and parses the sidecar JSON for a photo. The bool reports
// whether a sidecar file existed (regardless of how much it contained); a
// parse/read error yields (nil, false).
func parseSidecar(absPhotoPath string) (*GoogleMeta, bool) {
	path := findSidecar(absPhotoPath)
	if path == "" {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var raw rawSidecar
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}

	m := &GoogleMeta{
		Title:       raw.Title,
		Description: raw.Description,
	}
	if t, ok := unixTimestamp(raw.PhotoTakenTime.Timestamp); ok {
		m.TakenAt = t
	}
	// Google writes 0,0 as a placeholder when it has no real location; treat
	// that (and any exact-zero pair) as "no GPS" rather than the Gulf of Guinea.
	if raw.GeoData.Latitude != 0 || raw.GeoData.Longitude != 0 {
		m.Lat = raw.GeoData.Latitude
		m.Lon = raw.GeoData.Longitude
		m.HasGPS = true
	}
	for _, p := range raw.People {
		if n := strings.TrimSpace(p.Name); n != "" {
			m.People = append(m.People, n)
		}
	}
	return m, true
}

// unixTimestamp parses a string of whole Unix seconds (as Takeout stores them)
// into a UTC time. ok is false for empty or non-numeric input.
func unixTimestamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0).UTC(), true
}
