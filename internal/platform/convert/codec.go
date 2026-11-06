package convert

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"

	// Register the decoders we support. Anything else (HEIC, GIF, TIFF, …)
	// fails image.Decode and is reported as ErrUnsupportedFormat.
	_ "image/jpeg"
	_ "image/png"
)

// JPEG quality search bounds and the scaling used when quality alone can't make
// an image fit.
const (
	minQuality    = 10
	maxQuality    = 95
	scaleFactor   = 0.8 // each downscale step keeps 80% of the dimensions
	maxScaleSteps = 12
	minDimension  = 32 // stop scaling down before the image becomes unusable
)

// decodeUpright decodes image bytes, rejects decompression bombs via the pixel
// limit, and applies EXIF orientation so the returned image is visually upright.
// Any decode failure (including unsupported formats like HEIC) maps to
// ErrUnsupportedFormat.
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

// compressJPEG finds the highest JPEG quality whose output fits maxBytes. If
// even the lowest quality overflows, it scales the image down in steps and
// retries, returning the smallest encoding it managed.
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

// fitJPEGQuality binary-searches the JPEG quality for the largest value whose
// encoding fits maxBytes. If even minQuality overflows it returns that
// minimum-quality encoding (the smallest this resolution can produce).
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

// compressPNG shrinks a PNG by scaling the image down until it fits maxBytes or
// hits the size floor. PNG is lossless, so dimensions are the only lever.
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

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
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

// scaleDown returns img resized by factor using a high-quality kernel.
func scaleDown(img image.Image, factor float64) image.Image {
	b := img.Bounds()
	w := max(int(float64(b.Dx())*factor), 1)
	h := max(int(float64(b.Dy())*factor), 1)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// tooSmall reports whether img has shrunk past the point worth compressing.
func tooSmall(img image.Image) bool {
	b := img.Bounds()
	return b.Dx() <= minDimension || b.Dy() <= minDimension
}
