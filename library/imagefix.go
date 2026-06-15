package library

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"os"

	"github.com/disintegration/imaging"
)

// ErrUndecodable marks an image whose bytes were read successfully but could not
// be decoded even after a best-effort repair. It is distinct from a transient
// I/O error (file vanished, slow/unreachable storage): only ErrUndecodable
// signals genuinely broken content that should be quarantined rather than
// retried on every scan. Callers test it with errors.Is.
var ErrUndecodable = errors.New("undecodable image")

// decodeImageResilient reads and decodes the source image, applying a
// best-effort pure-Go repair when the strict standard decoder rejects the file.
//
// Go's image/jpeg is deliberately strict, so files that lenient viewers (libjpeg
// via XnView, etc.) render fine — e.g. a missing EOI marker or trailing garbage
// appended after the image — fail outright. We retry once on a normalized copy
// of the bytes. Genuinely corrupt entropy data ("bad Huffman code", truncated
// "short Huffman data") cannot be reconstructed in pure Go and surfaces as
// ErrUndecodable so the caller can quarantine the file.
//
// A failure to READ the bytes (missing file, storage error) is returned as-is,
// NOT wrapped in ErrUndecodable, so a transient glitch never quarantines an
// otherwise-good photo.
func decodeImageResilient(src string) (image.Image, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, err // transient I/O — caller must not quarantine
	}

	if img, derr := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true)); derr == nil {
		return img, nil
	} else {
		if fixed, ok := repairJPEG(data); ok {
			if img, ferr := imaging.Decode(bytes.NewReader(fixed), imaging.AutoOrientation(true)); ferr == nil {
				return img, nil
			}
		}
		return nil, fmt.Errorf("%w: %v", ErrUndecodable, derr)
	}
}

// repairJPEG returns a normalized copy of JPEG bytes for a second decode
// attempt, reporting whether any change was made. It fixes the two structural
// defects a strict decoder rejects but a lenient one tolerates:
//
//   - trailing bytes after the end-of-image (EOI) marker — trimmed off;
//   - a missing EOI marker entirely — appended.
//
// It deliberately does NOT attempt to reconstruct corrupt or truncated entropy
// data (that needs libjpeg-style error concealment, unavailable in pure Go), so
// such files still fail the retry and are quarantined.
func repairJPEG(data []byte) ([]byte, bool) {
	// SOI marker: every JPEG starts with FF D8. If it doesn't, this isn't a
	// JPEG we know how to normalize.
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, false
	}
	if idx := bytes.LastIndex(data, []byte{0xFF, 0xD9}); idx >= 0 {
		// EOI present: trim anything after it; if it already ends cleanly there
		// is nothing to fix.
		if idx+2 < len(data) {
			return data[:idx+2], true
		}
		return nil, false
	}
	// No EOI at all: append one so the decoder sees a complete stream.
	out := make([]byte, 0, len(data)+2)
	out = append(out, data...)
	out = append(out, 0xFF, 0xD9)
	return out, true
}
