package library

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sams96/rgeo"
)

// rgeo is initialized lazily because building the index parses the embedded
// Natural Earth datasets, which is comparatively expensive. We only pay that
// cost the first time a caller actually resolves a coordinate (e.g. the scan's
// metadata phase, or a TV location caption).
var (
	geoOnce  sync.Once
	geoIdx   *rgeo.Rgeo
	geoReady atomic.Bool
)

func geocoder() *rgeo.Rgeo {
	geoOnce.Do(func() {
		// Cities10 gives city names; Provinces10 adds the state/province;
		// Countries10 adds the country name.
		r, err := rgeo.New(rgeo.Cities10, rgeo.Provinces10, rgeo.Countries10)
		if err != nil {
			log.Printf("geocode: reverse geocoder unavailable: %v", err)
			return
		}
		geoIdx = r
		geoReady.Store(true)
	})
	return geoIdx
}

// WarmGeocoder builds the reverse-geocoding index in the background so the first
// on-demand lookup (e.g. the photo info panel) doesn't block an HTTP request on
// the expensive one-time dataset parse. Safe to call repeatedly.
func WarmGeocoder() {
	go geocoder()
}

// GeocoderReady reports whether the index has finished building. It never
// triggers the build nor blocks on it.
func GeocoderReady() bool { return geoReady.Load() }

// PlaceName resolves coordinates to a human "City, Province, Country" label
// (any part may be missing). Returns "" when geocoding is unavailable or finds
// nothing, e.g. open ocean.
func PlaceName(lat, lon float64) string {
	city, province, country := PlaceParts(lat, lon)
	return joinPlace(city, province, country)
}

// joinPlace formats a "City, Province, Country" label, dropping whichever parts
// are empty.
func joinPlace(city, province, country string) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{city, province, country} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

// PlaceNameIfReady is the non-blocking counterpart of PlaceName for use on the
// request path. When the geocoding index is not yet built it returns ("", false)
// immediately (kicking off a background warm-up) instead of blocking the caller
// on the expensive one-time dataset parse, so handlers like the photo info panel
// always respond promptly. Once warmed, later calls return the resolved label.
func PlaceNameIfReady(lat, lon float64) (string, bool) {
	if !GeocoderReady() {
		WarmGeocoder()
		return "", false
	}
	return PlaceName(lat, lon), true
}

// snapStepDeg is the radius increment (~1.1 km/lat) used when the exact GPS
// point misses every polygon, and snapMaxRings caps the search so a photo is
// never attributed to a far-away place. ~0.01° × 5 ≈ 5.5 km cardinal.
const (
	snapStepDeg  = 0.01
	snapMaxRings = 5
)

// PlaceParts resolves coordinates to (city, province, country); any may be
// empty when geocoding is unavailable or finds nothing. Splitting the parts
// lets callers that index metadata store each component separately.
//
// When the exact point falls in a gap of the offline dataset (e.g. a lake,
// river, or the sliver between border polygons) the direct lookup fails; we
// then snap to the nearest resolvable point within snapMaxRings rings so such
// photos still get a best-effort region label instead of nothing.
func PlaceParts(lat, lon float64) (city, province, country string) {
	g := geocoder()
	if g == nil {
		return "", "", ""
	}
	if loc, err := g.ReverseGeocode([]float64{lon, lat}); err == nil { // rgeo wants {lon, lat}
		return loc.City, loc.Province, loc.Country
	}
	return snapPlaceParts(g, lat, lon)
}

// snapPlaceParts probes points in expanding rings around (lat, lon) and returns
// the components of the first one that resolves. Returns empty strings when no
// land is found within the capped radius.
func snapPlaceParts(g *rgeo.Rgeo, lat, lon float64) (city, province, country string) {
	dirs := [8][2]float64{{1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {1, -1}, {-1, 1}, {-1, -1}}
	for ring := 1; ring <= snapMaxRings; ring++ {
		step := snapStepDeg * float64(ring)
		for _, d := range dirs {
			loc, err := g.ReverseGeocode([]float64{lon + d[1]*step, lat + d[0]*step})
			if err != nil {
				continue
			}
			return loc.City, loc.Province, loc.Country
		}
	}
	return "", "", ""
}
