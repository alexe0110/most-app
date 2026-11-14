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
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// noiseImage builds a w×h image of random pixels. Noise doesn't compress well,
// so the encoded size is big enough to trigger the quality search and downscaling.
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

// pngHeader returns only the signature and IHDR of a PNG claiming the given
// size — enough for DecodeConfig, which is all the bomb guard reads.
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

	out, err := ImagesToPDF(t.Context(), pages)
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
			_, err := ImagesToPDF(t.Context(), tt.images)
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

			out, err := Compress(t.Context(), src, tt.maxBytes, tt.format)
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
	out, err := Compress(t.Context(), src, 200, JPEG)
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
			_, err := Compress(t.Context(), tt.img, tt.maxBytes, tt.format)
			require.ErrorIs(t, err, tt.want)
		})
	}
}

// jpegHeader returns SOI, a JFIF marker and a baseline SOF0 segment claiming
// the given size and nothing else: DecodeConfig accepts it (it stops at SOF
// only for JFIF files), a full decode cannot.
func jpegHeader(w, h int) []byte {
	return []byte{
		0xFF, 0xD8, // SOI
		0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, // APP0
		0xFF, 0xC0, 0x00, 0x0B, 0x08, // SOF0, length 11, 8-bit precision
		byte(h >> 8), byte(h), byte(w >> 8), byte(w),
		0x01, 0x01, 0x11, 0x00, // one component
	}
}

func TestPixelBomb_rejectedBeforeDecode(t *testing.T) {
	// Both headers declare ~2 GP (4 GB of RGBA) in a few dozen bytes.
	bombs := map[string][]byte{
		"png":  pngHeader(60_000, 60_000),
		"jpeg": jpegHeader(60_000, 60_000),
	}
	for name, data := range bombs {
		t.Run(name, func(t *testing.T) {
			require.Less(t, len(data), 100)
			_, err := ImagesToPDF(t.Context(), [][]byte{data})
			require.ErrorIs(t, err, ErrImageTooLarge)
			_, err = Compress(t.Context(), data, 10_000, JPEG)
			require.ErrorIs(t, err, ErrImageTooLarge)
		})
	}
}

// The pixel limit is inclusive: exactly 25 Mpx passes the guard (the header-only
// data then fails the real decode), one pixel row more is a bomb.
func TestPixelLimit_boundary(t *testing.T) {
	tests := []struct {
		name string
		w, h int
		want error
	}{
		{"at the limit", 5_000, 5_000, ErrUnsupportedFormat},
		{"above the limit", 5_001, 5_000, ErrImageTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ToJPEG(t.Context(), pngHeader(tt.w, tt.h))
			require.ErrorIs(t, err, tt.want)
		})
	}
}

// tinyWebP is a 1x1 lossless WEBP.
func tinyWebP(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/tiny.webp")
	require.NoError(t, err)
	return data
}

func TestToJPEG_fromWebP(t *testing.T) {
	out, err := ToJPEG(t.Context(), tinyWebP(t))

	require.NoError(t, err)
	img, format, err := image.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, "jpeg", format)
	require.Equal(t, image.Rect(0, 0, 1, 1), img.Bounds())
}

func TestWebP_isAnInputEverywhere(t *testing.T) {
	webp := tinyWebP(t)

	compressed, err := Compress(t.Context(), webp, 1<<20, JPEG)
	require.NoError(t, err)
	_, format, err := image.Decode(bytes.NewReader(compressed))
	require.NoError(t, err)
	require.Equal(t, "jpeg", format)

	pdf, err := ImagesToPDF(t.Context(), [][]byte{webp, webp})
	require.NoError(t, err)
	require.Contains(t, string(pdf), "/Count 2")
}

func TestToJPEG_rejectsUnknownFormat(t *testing.T) {
	_, err := ToJPEG(t.Context(), []byte("ftypheic not an image"))
	require.ErrorIs(t, err, ErrUnsupportedFormat)
}

func TestImageSize(t *testing.T) {
	var jpg bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 40, 30)), nil))
	var pn bytes.Buffer
	require.NoError(t, png.Encode(&pn, image.NewRGBA(image.Rect(0, 0, 7, 9))))

	tests := []struct {
		name   string
		data   []byte
		w, h   int
		wantOK bool
	}{
		{"jpeg", jpg.Bytes(), 40, 30, true},
		{"png", pn.Bytes(), 7, 9, true},
		{"not an image", []byte("%PDF-1.4"), 0, 0, false},
		{"truncated", jpg.Bytes()[:5], 0, 0, false},
		{"empty", nil, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h, ok := ImageSize(tt.data)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.w, w)
			require.Equal(t, tt.h, h)
		})
	}
}

// transparentPNG is a fully transparent image with RGB zero (black under alpha 0).
func transparentPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 0})
		}
	}
	return mustPNG(t, img)
}

func requireWhitish(t *testing.T, jpg []byte) {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	require.NoError(t, err)
	r, g, b, _ := img.At(20, 20).RGBA()
	for _, c := range []uint32{r >> 8, g >> 8, b >> 8} {
		require.Greater(t, c, uint32(240), "transparent pixel must be flattened onto white, not black")
	}
}

func TestTransparencyFlattenedOnWhite(t *testing.T) {
	src := transparentPNG(t)

	t.Run("ToJPEG", func(t *testing.T) {
		out, err := ToJPEG(t.Context(), src)
		require.NoError(t, err)
		requireWhitish(t, out)
	})
	t.Run("Compress JPEG", func(t *testing.T) {
		out, err := Compress(t.Context(), src, 1<<20, JPEG)
		require.NoError(t, err)
		requireWhitish(t, out)
	})
	t.Run("ImagesToPDF", func(t *testing.T) {
		pdf, err := ImagesToPDF(t.Context(), [][]byte{src})
		require.NoError(t, err)
		start := bytes.Index(pdf, []byte{0xff, 0xd8, 0xff})
		require.GreaterOrEqual(t, start, 0, "embedded JPEG stream")
		requireWhitish(t, pdf[start:])
	})
}
