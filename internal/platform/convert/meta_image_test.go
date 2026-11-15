package convert_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// pngWithChunk is a small PNG with one extra tEXt chunk right after IHDR.
func pngWithChunk(t *testing.T, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, 8, 8))))
	b := buf.Bytes()
	const ihdrEnd = 8 + 25
	out := append([]byte{}, b[:ihdrEnd]...)
	out = append(out, pngChunkBytes("tEXt", body)...)
	return append(out, b[ihdrEnd:]...)
}

// exifWithTags is a JPEG whose EXIF IFD0 holds n unknown ASCII tags, or as many
// as the count field says when count is larger than the entries that follow.
func exifWithTags(t *testing.T, n, count int) []byte {
	t.Helper()
	ifd := binary.BigEndian.AppendUint16(nil, uint16(count))
	for i := range n {
		ifd = binary.BigEndian.AppendUint16(ifd, uint16(0xF000+i))
		ifd = binary.BigEndian.AppendUint16(ifd, 2)
		ifd = binary.BigEndian.AppendUint32(ifd, 1)
		ifd = append(ifd, 'a', 0, 0, 0)
	}
	tiff := append([]byte("MM\x00\x2a\x00\x00\x00\x08"), ifd...)
	tiff = append(tiff, 0, 0, 0, 0)
	return makeJPEG(t, append([]byte{0xFF, 0xE1, byte((len(tiff) + 8) >> 8), byte(len(tiff) + 8)}, append([]byte("Exif\x00\x00"), tiff...)...))
}

func TestInspect_hostileImages(t *testing.T) {
	tests := []struct {
		name  string
		data  []byte
		check func(t *testing.T, info convert.FileInfo)
	}{
		{"right-to-left override is removed from a value", pngWithChunk(t, []byte("Author\x00Ann\u202egnp.exe")),
			func(t *testing.T, info convert.FileInfo) {
				require.Equal(t, "Anngnp.exe", fieldValue(t, info, "PNG:Author"))
			}},
		{"control characters are dropped", pngWithChunk(t, []byte("Author\x00a\x01b\x7fc")),
			func(t *testing.T, info convert.FileInfo) {
				require.Equal(t, "abc", fieldValue(t, info, "PNG:Author"))
			}},
		{"a long value is capped", pngWithChunk(t, []byte("Note\x00"+strings.Repeat("я", 1000))),
			func(t *testing.T, info convert.FileInfo) {
				require.Len(t, []rune(fieldValue(t, info, "PNG:Note").(string)), 256)
			}},
		{"a text chunk with an empty value is metadata, not a field", pngWithChunk(t, []byte("Comment\x00")),
			func(t *testing.T, info convert.FileInfo) {
				require.False(t, info.Clean(), "the chunk is there, so the file is not clean")
				_, found := info.Field("PNG:Comment")
				require.False(t, found, "an empty value has nothing to show")
			}},
		{"a thousand unknown tags stay bounded", exifWithTags(t, 1500, 1500),
			func(t *testing.T, info convert.FileInfo) {
				require.LessOrEqual(t, info.FieldCount(), 1100, "the reader stops at its tag limit")
				require.Greater(t, info.FieldCount(), 0)
			}},
		{"a count far beyond the entries is read without a crash", exifWithTags(t, 2, 0xFFFF),
			func(_ *testing.T, _ convert.FileInfo) {}},
		{"a vendor blob is binary, not text", fixture(t, "iphone.jpg"),
			func(t *testing.T, info convert.FileInfo) {
				_, ok := fieldValue(t, info, "EXIF:MakerNoteApple").(convert.Binary)
				require.True(t, ok, "undefined-type data is shown as its size")
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			info, err := convert.Inspect(t.Context(), tt.data)
			if err != nil {
				requireNoCrash(t, err)
				return
			}
			require.Less(t, time.Since(start), 5*time.Second)
			tt.check(t, info)
		})
	}
}

// A PNG can carry as many text chunks as its size allows; the fields kept, and
// the work done for them, stop at a cap, and the rest is only counted.
func TestInspect_manyTextChunksAreCapped(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, 8, 8))))
	b := buf.Bytes()
	const ihdrEnd = 8 + 25
	data := append([]byte{}, b[:ihdrEnd]...)
	for i := range 5000 {
		data = append(data, pngChunkBytes("tEXt", []byte(fmt.Sprintf("Key%05d\x00v", i)))...)
	}
	data = append(data, b[ihdrEnd:]...)

	info, err := convert.Inspect(t.Context(), data)

	require.NoError(t, err)
	kept := 0
	for _, g := range info.Groups {
		kept += len(g.Fields)
	}
	require.LessOrEqual(t, kept, 1000)
	require.Greater(t, info.Dropped, 3000)
	require.Equal(t, kept+info.Dropped, info.FieldCount(), "the rest is counted, not lost")
}
