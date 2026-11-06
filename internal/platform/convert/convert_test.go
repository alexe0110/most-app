package convert

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

// noiseImage builds a w×h image of deterministic pseudo-random pixels. Noise
// does not compress well, so JPEG/PNG of it is large enough to exercise the
// quality search and downscaling instead of collapsing to a few bytes.
func noiseImage(w, h int) image.Image {
	rng := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255})
		}
	}
	return img
}

func mustJPEG(t *testing.T, img image.Image, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}))
	return buf.Bytes()
}

func mustPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// pngHeader returns just the signature and IHDR of a truecolor PNG claiming the
// given dimensions. It is enough for image.DecodeConfig to report the size,
// which is all the pixel-bomb guard inspects.
func pngHeader(w, h int) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8] = 8 // bit depth
	ihdr[9] = 2 // colour type: truecolor

	_ = binary.Write(&buf, binary.BigEndian, uint32(len(ihdr)))
	chunk := append([]byte("IHDR"), ihdr...)
	buf.Write(chunk)
	_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return buf.Bytes()
}

func TestImagesToPDF(t *testing.T) {
	pages := [][]byte{
		mustJPEG(t, noiseImage(80, 120), 80),
		mustPNG(t, noiseImage(120, 80)), // landscape, different format
		mustJPEG(t, noiseImage(100, 100), 80),
	}

	out, err := ImagesToPDF(pages)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(out, []byte("%PDF")), "output must be a PDF")
	require.Contains(t, string(out), "/Count 3", "one page per image")
}

func TestImagesToPDF_errors(t *testing.T) {
	tests := []struct {
		name   string
		images [][]byte
		want   error
	}{
		{"no images", nil, ErrNoImages},
		{"corrupt data", [][]byte{[]byte("not an image")}, ErrUnsupportedFormat},
		{"pixel bomb", [][]byte{pngHeader(10_000, 10_000)}, ErrImageTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ImagesToPDF(tt.images)
			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestCompress_meetsLimit(t *testing.T) {
	tests := []struct {
		name     string
		format   Format
		maxBytes int
	}{
		{"jpeg", JPEG, 20_000},
		{"png", PNG, 20_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := mustJPEG(t, noiseImage(600, 600), 95)
			require.Greater(t, len(src), tt.maxBytes, "source must start above the limit")

			out, err := Compress(src, tt.maxBytes, tt.format)
			require.NoError(t, err)
			require.LessOrEqual(t, len(out), tt.maxBytes)

			_, _, err = image.Decode(bytes.NewReader(out))
			require.NoError(t, err, "result must still be a decodable image")
		})
	}
}

func TestCompress_unreachableLimitIsHonest(t *testing.T) {
	src := mustJPEG(t, noiseImage(600, 600), 95)

	// 200 bytes is smaller than any usable image; Compress returns the smallest
	// it could make rather than an error, so the caller can report how far it got.
	out, err := Compress(src, 200, JPEG)
	require.NoError(t, err)
	require.Greater(t, len(out), 200)
	require.Less(t, len(out), len(src), "it still shrank the image as far as it could")

	_, _, err = image.Decode(bytes.NewReader(out))
	require.NoError(t, err)
}

func TestCompress_errors(t *testing.T) {
	good := mustJPEG(t, noiseImage(32, 32), 80)
	tests := []struct {
		name     string
		img      []byte
		maxBytes int
		format   Format
		want     error
	}{
		{"unsupported format", []byte("not an image"), 10_000, JPEG, ErrUnsupportedFormat},
		{"invalid output format", good, 10_000, Format("gif"), ErrInvalidFormat},
		{"non-positive target", good, 0, JPEG, ErrInvalidTarget},
		{"pixel bomb", pngHeader(10_000, 10_000), 10_000, JPEG, ErrImageTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compress(tt.img, tt.maxBytes, tt.format)
			require.ErrorIs(t, err, tt.want)
		})
	}
}
