package convert_test

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// exifTIFF is a big-endian TIFF with Make "Foo" and the given orientation.
func exifTIFF(orientation byte) []byte {
	return []byte{
		'M', 'M', 0, 42, 0, 0, 0, 8,
		0, 2,
		0x01, 0x0F, 0, 2, 0, 0, 0, 4, 'F', 'o', 'o', 0,
		0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, orientation, 0, 0,
		0, 0, 0, 0,
	}
}

func jpegSegment(marker byte, payload []byte) []byte {
	seg := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(2+len(payload)))
	return append(seg, payload...)
}

// makeJPEG encodes a small picture and puts extra segments right after SOI.
func makeJPEG(t *testing.T, extra ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, 16, 8)), nil))
	b := buf.Bytes()
	out := append([]byte{}, b[:2]...)
	for _, e := range extra {
		out = append(out, e...)
	}
	return append(out, b[2:]...)
}

func exifSegment(orientation byte) []byte {
	return jpegSegment(0xE1, append([]byte("Exif\x00\x00"), exifTIFF(orientation)...))
}

// scanOf returns the bytes from the first SOS to the end of the picture.
func scanOf(t *testing.T, jpg []byte) []byte {
	t.Helper()
	i := bytes.Index(jpg, []byte{0xFF, 0xDA})
	require.GreaterOrEqual(t, i, 0)
	return jpg[i:]
}

func TestStrip_jpeg(t *testing.T) {
	src := makeJPEG(t,
		exifSegment(6),
		jpegSegment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>")),
		jpegSegment(0xED, []byte("Photoshop 3.0\x00secret")),
		jpegSegment(0xFE, []byte("a comment")),
	)
	before, err := convert.Inspect(t.Context(), src)
	require.NoError(t, err)
	require.False(t, before.Clean())
	require.Equal(t, "Foo", before.Capture.Make)

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	after, err := convert.Inspect(t.Context(), out)
	require.NoError(t, err)
	require.True(t, after.Clean(), "got blocks %v", after.Blocks)
	require.Empty(t, after.Capture.Make)
	require.Equal(t, scanOf(t, src), scanOf(t, out), "the picture is copied byte for byte")
	require.NotContains(t, string(out), "secret")
	_, _, err = image.Decode(bytes.NewReader(out))
	require.NoError(t, err)

	// ToJPEG bakes the EXIF rotation into the pixels: a kept orientation turns 16x8 into 8x16.
	upright, err := convert.ToJPEG(t.Context(), out)
	require.NoError(t, err)
	w, h, ok := convert.ImageSize(upright)
	require.True(t, ok)
	require.Equal(t, [2]int{8, 16}, [2]int{w, h}, "a rotated photo stays upright")
}

func TestStrip_jpegCutsTrailingFrame(t *testing.T) {
	src := append(makeJPEG(t, exifSegment(1)), makeJPEG(t, exifSegment(1))...)

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	require.Equal(t, 1, bytes.Count(out, []byte{0xFF, 0xD8}), "only the first frame remains")
}

func pngChunkBytes(kind string, body []byte) []byte {
	c := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	c = append(c, kind...)
	c = append(c, body...)
	return binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(c[4:]))
}

func TestStrip_png(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, 8, 8))))
	b := buf.Bytes()
	// IHDR ends at 8 (signature) + 25.
	src := append([]byte{}, b[:33]...)
	src = append(src, pngChunkBytes("tEXt", []byte("Author\x00Ann"))...)
	src = append(src, pngChunkBytes("eXIf", exifTIFF(3))...)
	src = append(src, b[33:]...)
	before, err := convert.Inspect(t.Context(), src)
	require.NoError(t, err)
	require.Contains(t, before.Blocks, convert.BlockText)

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	after, err := convert.Inspect(t.Context(), out)
	require.NoError(t, err)
	require.True(t, after.Clean(), "got blocks %v", after.Blocks)
	require.NotContains(t, string(out), "Ann")
	_, err = png.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	require.Contains(t, string(out), "eXIf", "the orientation is kept")
}

func TestStrip_pdf(t *testing.T) {
	src := makeInfoPDF(t)

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	after, err := convert.Inspect(t.Context(), out)
	require.NoError(t, err)
	require.True(t, after.Clean(), "got blocks %v", after.Blocks)
	require.Empty(t, after.PDF.Creator)
	require.Empty(t, after.PDF.Producer)
	require.Equal(t, 1, after.PDF.Pages)
}

func TestStrip_office(t *testing.T) {
	out, err := convert.Strip(t.Context(), makeDOCX(t))

	require.NoError(t, err)
	after, err := convert.Inspect(t.Context(), out)
	require.NoError(t, err)
	require.True(t, after.Clean(), "got blocks %v", after.Blocks)
	require.Equal(t, convert.FileDOCX, after.Type)

	zr, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	require.NoError(t, err)
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			require.NoError(t, err)
			body, err := io.ReadAll(rc)
			require.NoError(t, err)
			require.Equal(t, "<w:document/>", string(body), "the content is untouched")
		}
	}
	require.Equal(t, []string{"[Content_Types].xml", "word/document.xml", "docProps/core.xml", "docProps/app.xml"}, names)
}

func TestStrip_unsupportedAndBroken(t *testing.T) {
	_, err := convert.Strip(t.Context(), []byte("plain text"))
	require.ErrorIs(t, err, convert.ErrUnsupportedFormat)

	_, err = convert.Strip(t.Context(), []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x08, 'E'})
	require.ErrorIs(t, err, convert.ErrUnreadable)
}

// box builds an ISO base media box.
func box(kind string, parts ...[]byte) []byte {
	body := bytes.Join(parts, nil)
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(body)))
	b = append(b, kind...)
	return append(b, body...)
}

func be32(v int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) }
func be16(v int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(v)) }

// makeHEIC builds a container with an Exif item and an XMP item in mdat, after a
// picture payload; it returns the file and the [start, end) of both items.
func makeHEIC(exif, xmpBody, picture []byte) (file []byte, exifAt, xmpAt [2]int) {
	build := func(exifOff, xmpOff int) []byte {
		infeExif := box("infe", []byte{2, 0, 0, 0}, be16(1), be16(0), []byte("Exif"), []byte("Exif\x00"))
		infeXMP := box("infe", []byte{2, 0, 0, 0}, be16(2), be16(0), []byte("mime"), []byte("XMP\x00application/rdf+xml\x00"))
		iinf := box("iinf", []byte{0, 0, 0, 0}, be16(2), infeExif, infeXMP)
		iloc := box("iloc", []byte{0, 0, 0, 0}, []byte{0x44, 0x00}, be16(2),
			be16(1), be16(0), be16(1), be32(exifOff), be32(len(exif)),
			be16(2), be16(0), be16(1), be32(xmpOff), be32(len(xmpBody)))
		meta := box("meta", []byte{0, 0, 0, 0}, iinf, iloc)
		ftyp := box("ftyp", []byte("heic"), be32(0), []byte("mif1heic"))
		return bytes.Join([][]byte{ftyp, meta, box("mdat", picture, exif, xmpBody)}, nil)
	}
	probe := build(0, 0)
	mdatBody := len(probe) - len(picture) - len(exif) - len(xmpBody)
	exifOff := mdatBody + len(picture)
	xmpOff := exifOff + len(exif)
	return build(exifOff, xmpOff), [2]int{exifOff, exifOff + len(exif)}, [2]int{xmpOff, xmpOff + len(xmpBody)}
}

func TestStrip_heicBlanksItemsInPlace(t *testing.T) {
	exif := append(be32(0), exifTIFF(1)...)
	xmpBody := []byte(`<?xpacket begin="" id="x"?><x:xmpmeta xmlns:x="adobe:ns:meta/"><secret/></x:xmpmeta>` + string(bytes.Repeat([]byte{' '}, 200)) + `<?xpacket end="w"?>`)
	picture := bytes.Repeat([]byte{0xAB}, 300)
	src, exifAt, xmpAt := makeHEIC(exif, xmpBody, picture)
	ft, ok := convert.DetectType(src)
	require.True(t, ok)
	require.Equal(t, convert.FileHEIC, ft)

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	require.Len(t, out, len(src), "nothing moves")
	require.Equal(t, src[:exifAt[0]], out[:exifAt[0]], "bytes before the items")
	require.Equal(t, src[exifAt[1]:xmpAt[0]], out[exifAt[1]:xmpAt[0]], "bytes between the items")
	require.Equal(t, src[xmpAt[1]:], out[xmpAt[1]:], "bytes after the items")
	require.NotContains(t, string(out), "Foo")
	require.NotContains(t, string(out), "secret")
	require.Equal(t, []byte{0, 0, 0, 0, 'I', 'I', 42, 0}, out[exifAt[0]:exifAt[0]+8])

	dec := xml.NewDecoder(bytes.NewReader(out[xmpAt[0]:xmpAt[1]]))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err, "the blank XMP stays well-formed")
	}
}

func TestStrip_heicOutOfBounds(t *testing.T) {
	src, exifAt, _ := makeHEIC(append(be32(0), exifTIFF(1)...), bytes.Repeat([]byte{' '}, 40), []byte{1})

	_, err := convert.Strip(t.Context(), src[:exifAt[0]+4])

	require.ErrorIs(t, err, convert.ErrUnreadable)
}
