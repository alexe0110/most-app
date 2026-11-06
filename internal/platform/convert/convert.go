// Package convert turns image bytes into other image bytes: it lays photos out
// as a PDF and shrinks images under a size budget. It is pure and does no I/O —
// bytes in, bytes out — so it is cheap to unit test and knows nothing about
// Telegram. HEIC decoding and PDF re-compression are intentionally left out
// (they need native libraries); see issue #39.
package convert

import (
	"bytes"
	"errors"
	"fmt"
	"image/jpeg"
	"math"
	"strconv"

	"github.com/go-pdf/fpdf"
)

// Format is the output encoding Compress produces.
type Format string

// Supported output formats for Compress.
const (
	JPEG Format = "jpeg"
	PNG  Format = "png"
)

// maxPixels caps an image's decoded area to guard against decompression bombs:
// a tiny file that claims enormous dimensions. 50 Mpx is well above any phone
// camera yet small enough to decode safely.
const maxPixels = 50_000_000

// Errors returned by the package. They are sentinels so callers can map them to
// user-facing messages with errors.Is.
var (
	ErrNoImages          = errors.New("convert: no images")
	ErrUnsupportedFormat = errors.New("convert: unsupported image format")
	ErrImageTooLarge     = errors.New("convert: image exceeds pixel limit")
	ErrInvalidFormat     = errors.New("convert: invalid output format")
	ErrInvalidTarget     = errors.New("convert: target size must be positive")
)

// A4 page geometry in millimetres, with a uniform margin around the image.
const (
	a4WidthMM  = 210.0
	a4HeightMM = 297.0
	marginMM   = 10.0

	// pdfJPEGQuality re-encodes each page's image; 85 keeps photos crisp while
	// keeping the PDF small.
	pdfJPEGQuality = 85
)

// ImagesToPDF lays each image on its own A4 page, centred and scaled to fit the
// page margins while preserving aspect ratio. EXIF orientation is applied so
// photos come out upright, and page order matches the input order. Every image
// is re-encoded as JPEG, which bakes in the orientation and keeps the file
// small — fine for the "photos to PDF" use case.
func ImagesToPDF(images [][]byte) ([]byte, error) {
	if len(images) == 0 {
		return nil, ErrNoImages
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(marginMM, marginMM, marginMM)

	for i, raw := range images {
		img, err := decodeUpright(raw)
		if err != nil {
			return nil, fmt.Errorf("image %d: %w", i+1, err)
		}

		var enc bytes.Buffer
		if err := jpeg.Encode(&enc, img, &jpeg.Options{Quality: pdfJPEGQuality}); err != nil {
			return nil, fmt.Errorf("encode image %d: %w", i+1, err)
		}

		name := "img" + strconv.Itoa(i)
		pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: "JPG"}, &enc)
		pdf.AddPage()
		w, h := fitWithin(img.Bounds().Dx(), img.Bounds().Dy(), a4WidthMM-2*marginMM, a4HeightMM-2*marginMM)
		pdf.ImageOptions(name, (a4WidthMM-w)/2, (a4HeightMM-h)/2, w, h, false, fpdf.ImageOptions{ImageType: "JPG"}, 0, "")
	}

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return out.Bytes(), nil
}

// fitWithin returns the millimetre size of a wPx×hPx image scaled to fit inside
// availW×availH while keeping its aspect ratio.
func fitWithin(wPx, hPx int, availW, availH float64) (w, h float64) {
	scale := math.Min(availW/float64(wPx), availH/float64(hPx))
	return float64(wPx) * scale, float64(hPx) * scale
}

// Compress re-encodes img so the result does not exceed maxBytes, in the given
// format. For JPEG it binary-searches the quality, then scales the image down if
// even the lowest quality overflows. For PNG (lossless) it scales down directly.
// It returns the best result it could produce: when the target is unreachable it
// returns the smallest encoding it managed, so the caller can compare the size
// against maxBytes and tell the user how far it got.
func Compress(img []byte, maxBytes int, format Format) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, ErrInvalidTarget
	}
	src, err := decodeUpright(img)
	if err != nil {
		return nil, err
	}

	switch format {
	case JPEG:
		return compressJPEG(src, maxBytes)
	case PNG:
		return compressPNG(src, maxBytes)
	default:
		return nil, ErrInvalidFormat
	}
}
