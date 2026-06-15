package unit

import (
	"testing"

	"plex-photos/library"
)

// restoreGeocodeMode resets the global mode after a test so cases don't leak
// state into one another (the geocoder mode is process-global).
func restoreGeocodeMode(t *testing.T) {
	t.Helper()
	prev := library.GetGeocodeMode()
	t.Cleanup(func() { library.SetGeocodeMode(prev) })
}

func TestNormalizeGeocodeMode(t *testing.T) {
	cases := map[string]library.GeocodeMode{
		"off":       library.GeocodeOff,
		"OFF":       library.GeocodeOff,
		" nearest ": library.GeocodeNearest,
		"accurate":  library.GeocodeAccurate,
		"":          library.DefaultGeocodeMode,
		"bogus":     library.DefaultGeocodeMode,
	}
	for in, want := range cases {
		if got := library.NormalizeGeocodeMode(in); got != want {
			t.Errorf("NormalizeGeocodeMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGeocodeOffReturnsEmpty(t *testing.T) {
	restoreGeocodeMode(t)
	library.SetGeocodeMode(library.GeocodeOff)
	if c, p, co := library.PlaceParts(48.8566, 2.3522); c != "" || p != "" || co != "" {
		t.Fatalf("off mode should return empty, got %q/%q/%q", c, p, co)
	}
	if !library.GeocoderReady() {
		t.Fatalf("off mode should always be ready")
	}
}

func TestGeocodeNearestResolvesKnownCities(t *testing.T) {
	restoreGeocodeMode(t)
	library.SetGeocodeMode(library.GeocodeNearest)

	cases := []struct {
		name        string
		lat, lon    float64
		wantCountry string
	}{
		{"Paris", 48.8566, 2.3522, "France"},
		{"Tokyo", 35.6762, 139.6503, "Japan"},
		{"New York", 40.7128, -74.0060, "United States"},
	}
	for _, tc := range cases {
		city, _, country := library.PlaceParts(tc.lat, tc.lon)
		if city == "" {
			t.Errorf("%s: expected a nearest city, got empty", tc.name)
		}
		if country != tc.wantCountry {
			t.Errorf("%s: country = %q, want %q (nearest city %q)", tc.name, country, tc.wantCountry, city)
		}
	}
}

func TestGeocodeNearestIsReadyAfterLookup(t *testing.T) {
	restoreGeocodeMode(t)
	library.SetGeocodeMode(library.GeocodeNearest)
	_ = library.PlaceName(51.5074, -0.1278) // London; forces the lazy build
	if !library.GeocoderReady() {
		t.Fatalf("nearest mode should be ready after a lookup")
	}
}
