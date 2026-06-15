//go:build ignore

// Command gen joins the GeoNames cities15000 dump with its admin1 and country
// lookup tables into one compact, gzipped TSV embedded by the library/geodata
// package. It is the "nearest" geocoder's offline dataset.
//
// Source files (public domain / CC BY 4.0, https://www.geonames.org/):
//
//	https://download.geonames.org/export/dump/cities15000.zip
//	https://download.geonames.org/export/dump/admin1CodesASCII.txt
//	https://download.geonames.org/export/dump/countryInfo.txt
//
// Regenerate with (from the repo root):
//
//	go run library/geodata/gen/gen.go -src <dir-with-the-three-files> -out library/geodata/cities.tsv.gz
//
// Output format: one line per city, tab-separated, "lat\tlon\tcity\tprovince\tcountry".
package main

import (
	"bufio"
	"compress/gzip"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	src := flag.String("src", ".", "directory containing cities15000.txt, admin1CodesASCII.txt, countryInfo.txt")
	out := flag.String("out", "library/geodata/cities.tsv.gz", "output gzipped TSV path")
	flag.Parse()

	countries, err := readCountries(filepath.Join(*src, "countryInfo.txt"))
	if err != nil {
		log.Fatalf("countryInfo: %v", err)
	}
	admin1, err := readAdmin1(filepath.Join(*src, "admin1CodesASCII.txt"))
	if err != nil {
		log.Fatalf("admin1: %v", err)
	}

	in, err := os.Open(filepath.Join(*src, "cities15000.txt"))
	if err != nil {
		log.Fatalf("cities: %v", err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}
	of, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create out: %v", err)
	}
	defer of.Close()
	zw := gzip.NewWriter(of)
	defer zw.Close()

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 11 {
			continue
		}
		name, lat, lon, cc, a1 := f[1], f[4], f[5], f[8], f[10]
		if name == "" || lat == "" || lon == "" {
			continue
		}
		province := admin1[cc+"."+a1]
		country := countries[cc]
		if country == "" {
			country = cc
		}
		// Sanitize: the TSV is line- and tab-delimited, so strip any stray
		// control characters from the free-text name.
		name = strings.ReplaceAll(strings.ReplaceAll(name, "\t", " "), "\n", " ")
		if _, err := fmt.Fprintf(zw, "%s\t%s\t%s\t%s\t%s\n", lat, lon, name, province, country); err != nil {
			log.Fatalf("write: %v", err)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		log.Fatalf("scan cities: %v", err)
	}
	log.Printf("wrote %d cities to %s", n, *out)
}

func readCountries(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		m[f[0]] = f[4] // ISO2 -> country name
	}
	return m, nil
}

func readAdmin1(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		m[f[0]] = f[1] // "CC.admin1" -> province name
	}
	return m, nil
}
