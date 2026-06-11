package library

import (
	"fmt"
	"image"
	"os"
	"strings"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/tiff"
)

// ExifInfo is the subset of metadata exposed to the frontend. Empty fields are
// omitted from the JSON response.
type ExifInfo struct {
	DateTaken   string `json:"dateTaken,omitempty"`
	Camera      string `json:"camera,omitempty"`
	Lens        string `json:"lens,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Exposure    string `json:"exposure,omitempty"`
	Aperture    string `json:"aperture,omitempty"`
	ISO         string `json:"iso,omitempty"`
	FocalLength string `json:"focalLength,omitempty"`
	GPS         string `json:"gps,omitempty"`

	// Structured coordinates (when present) for reverse geocoding. HasGPS
	// distinguishes a real 0,0 fix from "no GPS data".
	Lat    float64 `json:"-"`
	Lon    float64 `json:"-"`
	HasGPS bool    `json:"-"`
}

// ReadExif extracts EXIF metadata (and pixel dimensions) from an image file.
// Missing EXIF is not an error: a partially-populated ExifInfo is returned.
func ReadExif(path string) (*ExifInfo, error) {
	info := &ExifInfo{}

	// Dimensions are read from the image header (works even without EXIF).
	if f, err := os.Open(path); err == nil {
		if cfg, _, e := image.DecodeConfig(f); e == nil {
			info.Width = cfg.Width
			info.Height = cfg.Height
		}
		f.Close()
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		// No EXIF block: still return dimensions.
		return info, nil
	}

	if t, err := x.DateTime(); err == nil {
		info.DateTaken = t.Format("2006-01-02 15:04:05")
	}
	make := tagString(x, exif.Make)
	model := tagString(x, exif.Model)
	info.Camera = strings.TrimSpace(strings.Join(nonEmpty(make, model), " "))
	info.Lens = tagString(x, exif.LensModel)

	if v := tagString(x, exif.ExposureTime); v != "" {
		info.Exposure = v + " s"
	}
	if v := ratFloat(x, exif.FNumber); v > 0 {
		info.Aperture = fmt.Sprintf("f/%.1f", v)
	}
	info.ISO = tagString(x, exif.ISOSpeedRatings)
	if v := ratFloat(x, exif.FocalLength); v > 0 {
		info.FocalLength = fmt.Sprintf("%.0f mm", v)
	}
	if lat, lon, err := x.LatLong(); err == nil {
		info.GPS = fmt.Sprintf("%.5f, %.5f", lat, lon)
		info.Lat, info.Lon, info.HasGPS = lat, lon, true
	}

	return info, nil
}

func tagString(x *exif.Exif, name exif.FieldName) string {
	t, err := x.Get(name)
	if err != nil {
		return ""
	}
	s, err := t.StringVal()
	if err != nil {
		// Non-string (e.g. ISO is a short int): fall back to raw string.
		return strings.Trim(t.String(), "\"")
	}
	return strings.TrimSpace(s)
}

func ratFloat(x *exif.Exif, name exif.FieldName) float64 {
	t, err := x.Get(name)
	if err != nil {
		return 0
	}
	num, den, err := ratValue(t)
	if err != nil || den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func ratValue(t *tiff.Tag) (int64, int64, error) {
	return t.Rat2(0)
}

func nonEmpty(vals ...string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}
