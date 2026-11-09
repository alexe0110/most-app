// Package convert makes a PDF from images, shrinks images under a size limit and
// merges, trims and rotates PDFs. Pure: bytes in, bytes out, no I/O.
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

// Output formats for Compress.
const (
	JPEG Format = "jpeg"
	PNG  Format = "png"
)

// maxPixels guards against decompression bombs (a tiny file claiming huge
// dimensions). 50 Mpx is above any phone camera.
const maxPixels = 50_000_000

// Sentinel errors; callers map them to user messages with errors.Is.
var (
	ErrNoImages          = errors.New("convert: no images")
	ErrUnsupportedFormat = errors.New("convert: unsupported image format")
	ErrImageTooLarge     = errors.New("convert: image exceeds pixel limit")
	ErrInvalidFormat     = errors.New("convert: invalid output format")
	ErrInvalidTarget     = errors.New("convert: target size must be positive")
)

// A4 page and margin, in millimetres.
const (
	a4WidthMM  = 210.0
	a4HeightMM = 297.0
	marginMM   = 10.0

	pdfJPEGQuality = 85
)

// ImagesToPDF puts each image on its own A4 page: centred, scaled to the
// margins, aspect kept, EXIF rotation applied. Page order follows the input.
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

// fitWithin scales a wPx×hPx image to fit availW×availH (mm), keeping aspect.
func fitWithin(wPx, hPx int, availW, availH float64) (w, h float64) {
	scale := math.Min(availW/float64(wPx), availH/float64(hPx))
	return float64(wPx) * scale, float64(hPx) * scale
}

// toJPEGQuality is the quality of a plain format conversion: visually lossless
// without the size of the original.
const toJPEGQuality = 90

// ToJPEG converts a JPEG, PNG or WEBP image to JPEG at fixed quality, applying
// EXIF rotation.
func ToJPEG(img []byte) ([]byte, error) {
	src, err := decodeUpright(img)
	if err != nil {
		return nil, err
	}
	return encodeJPEG(src, toJPEGQuality)
}

// Compress re-encodes img to fit maxBytes in the given format. If the target is
// out of reach it returns the smallest it could make, so the caller can check
// the size and tell the user how far it got.
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
