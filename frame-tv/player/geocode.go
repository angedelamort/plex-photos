package player

import (
	"log"
	"strings"
	"sync"

	"github.com/sams96/rgeo"
)

// rgeo is initialized lazily because building the index parses the embedded
// Natural Earth datasets, which is comparatively expensive. We only pay that
// cost the first time a TV actually renders a location caption.
var (
	geoOnce sync.Once
	geoIdx  *rgeo.Rgeo
)

func geocoder() *rgeo.Rgeo {
	geoOnce.Do(func() {
		// Cities10 gives city names; Countries10 adds the country name.
		r, err := rgeo.New(rgeo.Cities10, rgeo.Countries10)
		if err != nil {
			log.Printf("tv caption: reverse geocoder unavailable: %v", err)
			return
		}
		geoIdx = r
	})
	return geoIdx
}

// placeName resolves coordinates to a human "City, Country" label (either part
// may be missing). Returns "" when geocoding is unavailable or finds nothing,
// e.g. open ocean.
func placeName(lat, lon float64) string {
	g := geocoder()
	if g == nil {
		return ""
	}
	loc, err := g.ReverseGeocode([]float64{lon, lat}) // rgeo wants {lon, lat}
	if err != nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if loc.City != "" {
		parts = append(parts, loc.City)
	}
	if loc.Country != "" {
		parts = append(parts, loc.Country)
	}
	return strings.Join(parts, ", ")
}
