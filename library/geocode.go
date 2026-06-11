package library

import (
	"log"
	"strings"
	"sync"

	"github.com/sams96/rgeo"
)

// rgeo is initialized lazily because building the index parses the embedded
// Natural Earth datasets, which is comparatively expensive. We only pay that
// cost the first time a caller actually resolves a coordinate (e.g. the scan's
// metadata phase, or a TV location caption).
var (
	geoOnce sync.Once
	geoIdx  *rgeo.Rgeo
)

func geocoder() *rgeo.Rgeo {
	geoOnce.Do(func() {
		// Cities10 gives city names; Countries10 adds the country name.
		r, err := rgeo.New(rgeo.Cities10, rgeo.Countries10)
		if err != nil {
			log.Printf("geocode: reverse geocoder unavailable: %v", err)
			return
		}
		geoIdx = r
	})
	return geoIdx
}

// PlaceName resolves coordinates to a human "City, Country" label (either part
// may be missing). Returns "" when geocoding is unavailable or finds nothing,
// e.g. open ocean.
func PlaceName(lat, lon float64) string {
	c, country := PlaceParts(lat, lon)
	parts := make([]string, 0, 2)
	if c != "" {
		parts = append(parts, c)
	}
	if country != "" {
		parts = append(parts, country)
	}
	return strings.Join(parts, ", ")
}

// PlaceParts resolves coordinates to (city, country); either may be empty when
// geocoding is unavailable or finds nothing. Splitting the parts lets callers
// that index metadata store the city and country separately.
func PlaceParts(lat, lon float64) (city, country string) {
	g := geocoder()
	if g == nil {
		return "", ""
	}
	loc, err := g.ReverseGeocode([]float64{lon, lat}) // rgeo wants {lon, lat}
	if err != nil {
		return "", ""
	}
	return loc.City, loc.Country
}
