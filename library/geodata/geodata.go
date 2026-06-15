// Package geodata embeds the offline city dataset used by the "nearest"
// reverse-geocoding mode. The data is a compact, gzipped TSV joined from the
// GeoNames cities15000 dump (see gen/gen.go), one line per city:
//
//	lat\tlon\tcity\tprovince\tcountry
//
// Parsing it yields a flat slice of ~34k cities (a few MB resident), which the
// geocoder scans for the nearest populated place — far lighter than building a
// full polygon/S2 index, at the cost of returning the nearest town rather than
// the exact administrative region containing the point.
package geodata

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strconv"
	"strings"
)

//go:embed cities.tsv.gz
var citiesGz []byte

// City is one populated place with its coordinates and place labels.
type City struct {
	Lat, Lon float32
	Name     string
	Province string
	Country  string
}

// Cities decodes the embedded dataset into a flat slice. Province and country
// strings are interned (there are only a few hundred countries and a few
// thousand provinces) so the retained footprint stays small.
func Cities() ([]City, error) {
	zr, err := gzip.NewReader(bytes.NewReader(citiesGz))
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	intern := make(map[string]string, 4096)
	in := func(s string) string {
		if s == "" {
			return ""
		}
		if v, ok := intern[s]; ok {
			return v
		}
		intern[s] = s
		return s
	}

	out := make([]City, 0, 34000)
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 256*1024), 256*1024)
	for sc.Scan() {
		f := strings.SplitN(sc.Text(), "\t", 5)
		if len(f) < 5 {
			continue
		}
		lat, err1 := strconv.ParseFloat(f[0], 32)
		lon, err2 := strconv.ParseFloat(f[1], 32)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, City{
			Lat:      float32(lat),
			Lon:      float32(lon),
			Name:     f[2],
			Province: in(f[3]),
			Country:  in(f[4]),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
