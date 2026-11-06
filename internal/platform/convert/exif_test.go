package convert

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"
)

// exifSegment builds a minimal APP1 (Exif/TIFF, big-endian) segment carrying
// only the orientation tag, ready to splice into a JPEG right after its SOI.
func exifSegment(o int) []byte {
	return []byte{
		0xFF, 0xE1, 0x00, 0x22, // APP1 marker + segment length (34)
		'E', 'x', 'i', 'f', 0x00, 0x00, // Exif header
		'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08, // TIFF header, IFD0 at offset 8
		0x00, 0x01, // one IFD entry
		0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, byte(0x00), byte(o), 0x00, 0x00, // orientation = o
		0x00, 0x00, 0x00, 0x00, // no next IFD
	}
}

// jpegWithOrientation encodes img as JPEG and injects an EXIF orientation tag.
func jpegWithOrientation(t *testing.T, img image.Image, o int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	raw := buf.Bytes()

	out := make([]byte, 0, len(raw)+len(exifSegment(o)))
	out = append(out, raw[:2]...) // SOI
	out = append(out, exifSegment(o)...)
	out = append(out, raw[2:]...)
	return out
}

func TestExifOrientation(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 4, 4))

	tests := []struct {
		name string
		data []byte
		want int
	}{
		{"orientation 6", jpegWithOrientation(t, base, 6), 6},
		{"orientation 8", jpegWithOrientation(t, base, 8), 8},
		{"no exif", mustJPEG(t, base, 80), 1},
		{"not a jpeg", []byte("not an image"), 1},
		{"truncated", []byte{0xFF, 0xD8, 0xFF}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, exifOrientation(tt.data))
		})
	}
}

func TestApplyOrientation_dimensions(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2)) // 3 wide, 2 tall
	tests := []struct {
		o     int
		wantW int
		wantH int
	}{
		{1, 3, 2},
		{3, 3, 2},
		{6, 2, 3}, // rotate 90° → dimensions swap
		{8, 2, 3},
		{9, 3, 2}, // out of range → unchanged
	}
	for _, tt := range tests {
		got := applyOrientation(src, tt.o)
		require.Equal(t, tt.wantW, got.Bounds().Dx(), "orientation %d width", tt.o)
		require.Equal(t, tt.wantH, got.Bounds().Dy(), "orientation %d height", tt.o)
	}
}

func TestApplyOrientation_rotate90(t *testing.T) {
	// A 2×1 image: left pixel red, right pixel blue. Orientation 6 rotates it
	// 90° clockwise, so the left (red) pixel moves to the top.
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	red := color.RGBA{255, 0, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	src.Set(0, 0, red)
	src.Set(1, 0, blue)

	got := applyOrientation(src, 6)
	require.Equal(t, 1, got.Bounds().Dx())
	require.Equal(t, 2, got.Bounds().Dy())
	require.Equal(t, red, color.RGBAModel.Convert(got.At(0, 0)))
	require.Equal(t, blue, color.RGBAModel.Convert(got.At(0, 1)))
}

func TestDecodeUpright_appliesExif(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 10, 4)) // 10 wide, 4 tall
	data := jpegWithOrientation(t, src, 6)

	img, err := decodeUpright(data)
	require.NoError(t, err)
	require.Equal(t, 4, img.Bounds().Dx(), "orientation 6 swaps width and height")
	require.Equal(t, 10, img.Bounds().Dy())
}
