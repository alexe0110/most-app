package convert

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"

	// JPEG, PNG and WEBP decoders; other formats (HEIC, GIF, …) fail to decode.
	_ "golang.org/x/image/webp"
	_ "image/jpeg"
	_ "image/png"
)

const (
	minQuality    = 10
	maxQuality    = 95
	scaleFactor   = 0.8 // each step keeps 80% of the size
	maxScaleSteps = 12
	minDimension  = 32 // don't shrink below this
)

// decodeUpright decodes bytes, checks the pixel limit, and applies EXIF
// rotation. A decode failure (e.g. HEIC) maps to ErrUnsupportedFormat.
func decodeUpright(data []byte) (image.Image, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrUnsupportedFormat
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, ErrImageTooLarge
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrUnsupportedFormat
	}

	orientation := 1
	if format == "jpeg" {
		orientation = exifOrientation(data)
	}
	return applyOrientation(img, orientation), nil
}

// compressJPEG picks the best quality that fits; if the lowest still overflows,
// it scales down step by step and returns the smallest it managed.
func compressJPEG(img image.Image, maxBytes int) ([]byte, error) {
	best, err := fitJPEGQuality(img, maxBytes)
	if err != nil {
		return nil, err
	}
	if len(best) <= maxBytes {
		return best, nil
	}

	cur := img
	for step := 0; step < maxScaleSteps && !tooSmall(cur); step++ {
		cur = scaleDown(cur, scaleFactor)
		candidate, err := fitJPEGQuality(cur, maxBytes)
		if err != nil {
			return nil, err
		}
		best = candidate
		if len(candidate) <= maxBytes {
			return candidate, nil
		}
	}
	return best, nil
}

// fitJPEGQuality binary-searches for the highest quality that fits maxBytes.
// If even minQuality overflows, it returns that (smallest at this size).
func fitJPEGQuality(img image.Image, maxBytes int) ([]byte, error) {
	floor, err := encodeJPEG(img, minQuality)
	if err != nil {
		return nil, err
	}
	if len(floor) > maxBytes {
		return floor, nil
	}

	fit := floor
	lo, hi := minQuality+1, maxQuality
	for lo <= hi {
		mid := (lo + hi) / 2
		enc, err := encodeJPEG(img, mid)
		if err != nil {
			return nil, err
		}
		if len(enc) <= maxBytes {
			fit = enc
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return fit, nil
}

// compressPNG scales the image down until it fits maxBytes. PNG is lossless, so
// size is the only lever.
func compressPNG(img image.Image, maxBytes int) ([]byte, error) {
	best, err := encodePNG(img)
	if err != nil {
		return nil, err
	}
	if len(best) <= maxBytes {
		return best, nil
	}

	cur := img
	for step := 0; step < maxScaleSteps && !tooSmall(cur); step++ {
		cur = scaleDown(cur, scaleFactor)
		candidate, err := encodePNG(cur)
		if err != nil {
			return nil, err
		}
		best = candidate
		if len(candidate) <= maxBytes {
			return candidate, nil
		}
	}
	return best, nil
}

// flattenOnWhite composites a transparent image onto white: JPEG has no alpha,
// and dropping it would turn transparent pixels black. Opaque images are
// returned as is.
func flattenOnWhite(img image.Image) image.Image {
	if o, ok := img.(interface{ Opaque() bool }); ok && o.Opaque() {
		return img
	}
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, b, img, b.Min, draw.Over)
	return dst
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flattenOnWhite(img), &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// scaleDown resizes img by factor with a high-quality kernel.
func scaleDown(img image.Image, factor float64) image.Image {
	b := img.Bounds()
	w := max(int(float64(b.Dx())*factor), 1)
	h := max(int(float64(b.Dy())*factor), 1)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// tooSmall reports that img is too small to shrink further.
func tooSmall(img image.Image) bool {
	b := img.Bounds()
	return b.Dx() <= minDimension || b.Dy() <= minDimension
}
