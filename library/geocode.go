package library

import (
	"log"
	"math"
	"strings"
	"sync"
	"sync/atomic"

	"plex-photos/library/geodata"

	"github.com/sams96/rgeo"
)

// GeocodeMode selects how photo GPS coordinates are turned into place labels.
// It trades resolution against memory/CPU, which matters on small NAS hosts.
type GeocodeMode string

const (
	// GeocodeOff disables reverse geocoding entirely: no place labels are
	// derived and no dataset is loaded, so the geocoder costs zero memory.
	GeocodeOff GeocodeMode = "off"

	// GeocodeNearest (the default) resolves coordinates to the nearest city in
	// a small embedded GeoNames dataset (~34k places). It keeps city / province
	// / country labels at a low, fixed memory cost (a few MB) and is fast. The
	// trade-off is that it returns the nearest populated place rather than the
	// administrative region that actually contains the point, so a remote photo
	// can be attributed to a town some distance away and a point near a border
	// may pick the neighbouring region.
	GeocodeNearest GeocodeMode = "nearest"

	// GeocodeAccurate resolves coordinates by exact polygon containment using
	// the full-resolution Natural Earth datasets (via rgeo). It is the most
	// precise option, especially near borders.
	//
	// WARNING: this mode parses and holds high-resolution boundary polygons and
	// builds an in-memory spatial index. It can use a LOT of RAM (hundreds of
	// MB) and is comparatively SLOW to build, which can be too heavy for
	// memory-constrained hosts (e.g. a small Synology NAS shared with Plex).
	GeocodeAccurate GeocodeMode = "accurate"
)

// SettingGeocodeMode is the settings key holding the active GeocodeMode.
const SettingGeocodeMode = "geocode_mode"

// DefaultGeocodeMode is the conservative default: city-level labels at a small,
// fixed memory cost. Exported so startup wiring can seed the persisted setting.
const DefaultGeocodeMode = GeocodeNearest

// NormalizeGeocodeMode coerces an arbitrary string to a valid GeocodeMode,
// falling back to the default for unknown/empty values.
func NormalizeGeocodeMode(s string) GeocodeMode {
	switch GeocodeMode(strings.ToLower(strings.TrimSpace(s))) {
	case GeocodeOff:
		return GeocodeOff
	case GeocodeNearest:
		return GeocodeNearest
	case GeocodeAccurate:
		return GeocodeAccurate
	default:
		return DefaultGeocodeMode
	}
}

var (
	geoModeMu sync.RWMutex
	geoMode   = DefaultGeocodeMode
)

// GetGeocodeMode returns the active geocoding mode.
func GetGeocodeMode() GeocodeMode {
	geoModeMu.RLock()
	defer geoModeMu.RUnlock()
	return geoMode
}

// SetGeocodeMode switches the active geocoding mode (clamping unknown values to
// the default) and clears the in-memory result cache so subsequent lookups are
// resolved by the newly selected backend. It does not build any index itself;
// call WarmGeocoder to pre-build the new backend in the background.
func SetGeocodeMode(m GeocodeMode) GeocodeMode {
	m = NormalizeGeocodeMode(string(m))
	geoModeMu.Lock()
	geoMode = m
	geoModeMu.Unlock()
	geoCacheMu.Lock()
	geoCache = map[geoCacheKeyT]placeResult{}
	geoCacheMu.Unlock()
	return m
}

// --- "accurate" backend: rgeo polygon containment ---------------------------

// The rgeo index is initialized lazily because building it parses the embedded
// high-resolution Natural Earth datasets and an s2 ShapeIndex, which is
// expensive in both time and (especially) memory. We only pay that cost when
// the accurate mode is actually selected and used.
var (
	geoOnce  sync.Once
	geoIdx   *rgeo.Rgeo
	geoReady atomic.Bool
)

func geocoder() *rgeo.Rgeo {
	geoOnce.Do(func() {
		// Provinces10 already carries the country information, so it is paired
		// with Cities10 (urban areas) without the redundant Countries dataset.
		r, err := rgeo.New(rgeo.Provinces10, rgeo.Cities10)
		if err != nil {
			log.Printf("geocode: accurate geocoder unavailable: %v", err)
			return
		}
		// rgeo defers the (CPU/RAM-heavy) s2 ShapeIndex build until the first
		// lookup. Force it now, while we're single-threaded under the Once, so
		// the build never stalls a scan mid-phase and concurrent scan workers
		// can't race on the lazy build.
		_, _ = r.ReverseGeocode([]float64{0, 0})
		geoIdx = r
		geoReady.Store(true)
	})
	return geoIdx
}

// --- "nearest" backend: embedded GeoNames cities ----------------------------

var (
	nearOnce  sync.Once
	nearCity  []geodata.City
	nearReady atomic.Bool
)

func nearestIndex() []geodata.City {
	nearOnce.Do(func() {
		c, err := geodata.Cities()
		if err != nil {
			log.Printf("geocode: nearest dataset unavailable: %v", err)
			return
		}
		nearCity = c
		nearReady.Store(true)
	})
	return nearCity
}

// nearestPlaceParts returns the labels of the geographically nearest city to
// (lat, lon). Longitude deltas are scaled by cos(lat) so the comparison is an
// approximate metric distance rather than raw degrees (which overstate
// east–west distance away from the equator).
func nearestPlaceParts(lat, lon float64) (city, province, country string) {
	cities := nearestIndex()
	if len(cities) == 0 {
		return "", "", ""
	}
	cosLat := math.Cos(lat * math.Pi / 180)
	best := -1
	bestD := math.MaxFloat64
	for i := range cities {
		dLat := lat - float64(cities[i].Lat)
		dLon := (lon - float64(cities[i].Lon)) * cosLat
		d := dLat*dLat + dLon*dLon
		if d < bestD {
			bestD = d
			best = i
		}
	}
	if best < 0 {
		return "", "", ""
	}
	c := cities[best]
	return c.Name, c.Province, c.Country
}

// --- warm-up + readiness ----------------------------------------------------

// WarmGeocoder builds the active mode's index in the background so the first
// on-demand lookup doesn't block on the one-time build. It is a no-op for the
// "off" mode. Safe to call repeatedly (and after a mode switch).
func WarmGeocoder() {
	switch GetGeocodeMode() {
	case GeocodeOff:
		return
	case GeocodeAccurate:
		go geocoder()
	default:
		go nearestIndex()
	}
}

// GeocoderReady reports whether the active mode's index is ready for lookups
// without blocking. "off" is always ready (it resolves to empty instantly).
func GeocoderReady() bool {
	switch GetGeocodeMode() {
	case GeocodeOff:
		return true
	case GeocodeAccurate:
		return geoReady.Load()
	default:
		return nearReady.Load()
	}
}

// PlaceName resolves coordinates to a human "City, Province, Country" label
// (any part may be missing). Returns "" when geocoding is off/unavailable or
// finds nothing.
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
// request path. When the active geocoding index is not yet built it returns
// ("", false) immediately (kicking off a background warm-up) instead of
// blocking the caller on the one-time build. The "off" mode resolves to
// ("", true) — ready, but no place.
func PlaceNameIfReady(lat, lon float64) (string, bool) {
	if GetGeocodeMode() == GeocodeOff {
		return "", true
	}
	if !GeocoderReady() {
		WarmGeocoder()
		return "", false
	}
	return PlaceName(lat, lon), true
}

// snapStepDeg is the radius increment (~1.1 km/lat) used by the accurate mode
// when the exact GPS point misses every polygon, and snapMaxRings caps the
// search so a photo is never attributed to a far-away place.
const (
	snapStepDeg  = 0.01
	snapMaxRings = 5
)

// placeResult is a cached reverse-geocode outcome. An entry's mere presence
// means "already resolved" — including a negative result (all fields empty).
type placeResult struct{ city, province, country string }

// geoCacheRound rounds coordinates to ~11 m so bursts of photos shot at the
// same spot share one cached lookup.
const geoCacheRound = 1e5

// geoCacheKeyT keys the result cache by rounded coordinate AND mode, so a mode
// switch never serves a label resolved by a different backend.
type geoCacheKeyT struct {
	mode     GeocodeMode
	lat, lon int64
}

func geoCacheKey(mode GeocodeMode, lat, lon float64) geoCacheKeyT {
	return geoCacheKeyT{
		mode: mode,
		lat:  int64(lat*geoCacheRound + 0.5),
		lon:  int64(lon*geoCacheRound + 0.5),
	}
}

var (
	geoCacheMu sync.RWMutex
	geoCache   = map[geoCacheKeyT]placeResult{}
)

// PlaceParts resolves coordinates to (city, province, country) using the active
// GeocodeMode; any part may be empty when geocoding is off/unavailable or finds
// nothing. Results (including negative ones) are memoized by rounded coordinate
// and mode so each location is resolved at most once per mode for the life of
// the process.
func PlaceParts(lat, lon float64) (city, province, country string) {
	mode := GetGeocodeMode()
	if mode == GeocodeOff {
		return "", "", ""
	}

	key := geoCacheKey(mode, lat, lon)
	geoCacheMu.RLock()
	cached, ok := geoCache[key]
	geoCacheMu.RUnlock()
	if ok {
		return cached.city, cached.province, cached.country
	}

	var res placeResult
	switch mode {
	case GeocodeAccurate:
		res.city, res.province, res.country = accuratePlaceParts(lat, lon)
	default:
		res.city, res.province, res.country = nearestPlaceParts(lat, lon)
	}

	geoCacheMu.Lock()
	geoCache[key] = res
	geoCacheMu.Unlock()
	return res.city, res.province, res.country
}

// accuratePlaceParts resolves coordinates by exact polygon containment.
//
// When the exact point falls in a gap of the offline dataset (e.g. a lake,
// river, or the sliver between border polygons) the direct lookup fails; we
// then snap to the nearest resolvable point within snapMaxRings rings so such
// photos still get a best-effort region label instead of nothing.
func accuratePlaceParts(lat, lon float64) (city, province, country string) {
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
