package convert

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

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
		out, err := ToJPEG(src)
		require.NoError(t, err)
		requireWhitish(t, out)
	})
	t.Run("Compress JPEG", func(t *testing.T) {
		out, err := Compress(src, 1<<20, JPEG)
		require.NoError(t, err)
		requireWhitish(t, out)
	})
	t.Run("ImagesToPDF", func(t *testing.T) {
		pdf, err := ImagesToPDF([][]byte{src})
		require.NoError(t, err)
		start := bytes.Index(pdf, []byte{0xff, 0xd8, 0xff})
		require.GreaterOrEqual(t, start, 0, "embedded JPEG stream")
		requireWhitish(t, pdf[start:])
	})
}

func TestParsePageRange_garbage(t *testing.T) {
	bad := []string{"", " ", "0", "3-1", "1-", "-1", "-", ",", "1,,2", "1,", ",1", "1-2-3",
		"a", "1;2", "١", "+1", "0-3", "99999999999999999999", "9999999", "1-9999999",
		"1 2", "2-1,3", "1.5"}
	for _, in := range bad {
		t.Run(in, func(t *testing.T) {
			_, err := ParsePageRange(in, 10)
			require.ErrorIs(t, err, ErrBadPageRange)
		})
	}

	_, err := ParsePageRange("1", 0)
	require.ErrorIs(t, err, ErrBadPageRange, "no pages: nothing is valid")
	_, err = ParsePageRange(string(rune(0))+"1", 5)
	require.ErrorIs(t, err, ErrBadPageRange)
	_, err = ParsePageRange("1-"+strconv.Itoa(math.MaxInt32), 10)
	require.ErrorIs(t, err, ErrBadPageRange)
}

func TestParsePageRange_valid(t *testing.T) {
	tests := []struct {
		in   string
		want []int
	}{
		{"1-3,5", []int{1, 2, 3, 5}},
		{" 5 , 1 - 3 ", []int{1, 2, 3, 5}},
		{"3,3,2-3", []int{2, 3}},
		{"10", []int{10}},
		{"1-10", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}},
	}
	for _, tt := range tests {
		got, err := ParsePageRange(tt.in, 10)
		require.NoError(t, err, tt.in)
		require.Equal(t, tt.want, got, tt.in)
	}
}
