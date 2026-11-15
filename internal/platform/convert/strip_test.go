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
func makeJPEG(t testing.TB, extra ...[]byte) []byte {
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
	require.Equal(t, "Foo", fieldValue(t, before, "EXIF:Make"))

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	after, err := convert.Inspect(t.Context(), out)
	require.NoError(t, err)
	require.True(t, after.Clean(), "got blocks %v", after.Blocks)
	_, found := after.Field("EXIF:Make")
	require.False(t, found)
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
	_, hasCreator := after.Field("PDF:Creator")
	_, hasProducer := after.Field("PDF:Producer")
	require.False(t, hasCreator || hasProducer)
	require.EqualValues(t, 1, fieldValue(t, after, "PDF:Pages"))
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

func TestStrip_fixtures(t *testing.T) {
	// leaks are values of the fixture that must not survive anywhere in the bytes.
	tests := []struct {
		file  string
		leaks []string
	}{
		{"gps.jpg", []string{"Apple", "iPhone", "17.3.1", "Ann Author", "Photoshop"}},
		{"text.png", []string{"Apple", "iPhone", "Ann Author", "Photoshop"}},
		{"exif.webp", []string{"Apple", "iPhone", "Ann Author", "Photoshop"}},
		{"gps.heic", []string{"Apple", "iPhone", "Ann Author"}},
		{"word.pdf", []string{"Ann Author", "Microsoft", "Photoshop", "Contract draft"}},
		{"author.docx", []string{"Ann Author", "Bob Editor", "Acme", "Microsoft Office"}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			src := fixture(t, tt.file)
			before := bytes.Clone(src)

			out, err := convert.Strip(t.Context(), src)

			require.NoError(t, err)
			require.Equal(t, before, src, "the input is not modified")
			after, err := convert.Inspect(t.Context(), out)
			require.NoError(t, err)
			require.True(t, after.Clean(), "blocks left: %v", after.Blocks)
			require.Empty(t, after.SensitiveFields())
			for _, leak := range tt.leaks {
				require.NotContains(t, string(out), leak)
				require.NotContains(t, utf16LE(out), leak, "UTF-16 strings of a PDF info dictionary")
			}

			again, err := convert.Strip(t.Context(), out)
			require.NoError(t, err)
			if tt.file != "word.pdf" { // pdfcpu renumbers objects on every write
				require.Equal(t, out, again, "cleaning a clean file changes nothing")
			}
			afterAgain, err := convert.Inspect(t.Context(), again)
			require.NoError(t, err)
			require.True(t, afterAgain.Clean())
		})
	}
}

// utf16LE makes the UTF-16BE text of a PDF string comparable with plain ASCII by
// dropping its zero bytes.
func utf16LE(b []byte) string { return string(bytes.ReplaceAll(b, []byte{0}, nil)) }

func TestStrip_gpsJPEGKeepsPixelsAndOrientation(t *testing.T) {
	src := fixture(t, "gps.jpg")

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	require.Equal(t, scanOf(t, src), scanOf(t, out), "from the first SOS to EOI nothing changes")
	require.Less(t, len(out), len(src))
	w, h, ok := convert.ImageSize(src)
	require.True(t, ok)
	require.Equal(t, [2]int{64, 48}, [2]int{w, h})
	upright, err := convert.ToJPEG(t.Context(), out)
	require.NoError(t, err)
	w, h, _ = convert.ImageSize(upright)
	require.Equal(t, [2]int{48, 64}, [2]int{w, h}, "Orientation=6 survives, so the photo is not on its side")
}

func TestStrip_jpegOrientationRows(t *testing.T) {
	tests := []struct {
		name        string
		orientation byte
		wantEXIF    bool
	}{
		{"upright photo leaves no EXIF at all", 1, false},
		{"mirrored", 2, true},
		{"rotated 180", 3, true},
		{"rotated 90", 6, true},
		{"rotated 270", 8, true},
		{"out of range value is dropped", 9, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := convert.Strip(t.Context(), makeJPEG(t, exifSegment(tt.orientation)))

			require.NoError(t, err)
			require.Equal(t, tt.wantEXIF, bytes.Contains(out, []byte("Exif\x00\x00")))
			after, err := convert.Inspect(t.Context(), out)
			require.NoError(t, err)
			require.True(t, after.Clean(), "a kept orientation is not metadata")
		})
	}
}

func TestStrip_jpegWithoutEOIIsCopiedToTheEnd(t *testing.T) {
	whole := makeJPEG(t, exifSegment(1))
	src := whole[:len(whole)-2] // cut off the EOI
	require.Equal(t, []byte{0xFF, 0xD9}, whole[len(whole)-2:])

	before, err := convert.Inspect(t.Context(), src)
	require.NoError(t, err, "Inspect and Strip treat a cut-off image the same way")
	require.False(t, before.Clean())

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	require.Equal(t, scanOf(t, src), scanOf(t, out))
	after, err := convert.Inspect(t.Context(), out)
	require.NoError(t, err)
	require.True(t, after.Clean(), "got blocks %v", after.Blocks)
}

func TestStrip_progressiveJPEGDropsMetadataBetweenScans(t *testing.T) {
	base := makeJPEG(t)
	body := base[:len(base)-2]
	secondScan := bytes.Join([][]byte{
		jpegSegment(0xC4, bytes.Repeat([]byte{1}, 20)), // DHT: needed by the scan
		jpegSegment(0xDA, []byte{1, 1, 0, 0, 63, 0}),   // SOS
		{0x12, 0x34, 0xFF, 0x00, 0x56},                 // entropy data with a stuffed byte
	}, nil)
	src := bytes.Join([][]byte{
		body,
		jpegSegment(0xFE, []byte("SECRET comment")),
		exifSegment(3),
		secondScan,
		{0xFF, 0xD9},
	}, nil)

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	require.NotContains(t, string(out), "SECRET")
	require.NotContains(t, string(out), "Foo")
	want := bytes.Join([][]byte{scanOf(t, body), secondScan, {0xFF, 0xD9}}, nil)
	require.Equal(t, want, scanOf(t, out), "scans, tables and entropy data stay byte for byte")
}

func TestStrip_jpegDropsEverythingAfterEOI(t *testing.T) {
	tests := []struct {
		name  string
		extra []byte
	}{
		{"text", []byte("SECRET-TRAILER")},
		{"second frame with its own EOI", makeJPEG(t, exifSegment(1))},
		{"EOI-like bytes", []byte{0xFF, 0xD9, 0xFF, 0xD9, 'S', 'E', 'C', 'R', 'E', 'T'}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := append(makeJPEG(t), tt.extra...)

			out, err := convert.Strip(t.Context(), src)

			require.NoError(t, err)
			require.NotContains(t, string(out), "SECRET")
			require.True(t, bytes.HasSuffix(out, []byte{0xFF, 0xD9}))
			require.Equal(t, 1, bytes.Count(out, []byte{0xFF, 0xD8}))
		})
	}
}

func TestStrip_pngKeepsPixelsAndOrientation(t *testing.T) {
	src := fixture(t, "text.png")

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	srcImg, err := png.Decode(bytes.NewReader(src))
	require.NoError(t, err)
	outImg, err := png.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, srcImg, outImg, "same pixels")
	i := bytes.Index(out, []byte("eXIf"))
	require.GreaterOrEqual(t, i, 0, "Orientation=3 is kept")
	require.Equal(t, byte(3), out[i+4+8+10], "orientation value of the minimal EXIF")
}

func TestStrip_webpRiffStaysConsistent(t *testing.T) {
	src := fixture(t, "exif.webp")

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	require.Equal(t, "RIFF", string(out[:4]))
	require.Equal(t, uint32(len(out)-8), binary.LittleEndian.Uint32(out[4:8]), "RIFF size matches the file")
	require.Equal(t, "VP8X", string(out[12:16]))
	require.Zero(t, out[20]&(0x08|0x04), "EXIF and XMP flags are cleared")
	require.NotContains(t, string(out), "EXIF")
	require.NotContains(t, string(out), "XMP ")
	require.Contains(t, string(out), "VP8L", "the picture chunk is kept")
}

func TestStrip_pdfDoesNotRestampItself(t *testing.T) {
	out, err := convert.Strip(t.Context(), fixture(t, "word.pdf"))

	require.NoError(t, err)
	for _, key := range []string{"/Producer", "/Creator", "/Author", "/ModDate", "/CreationDate", "/Metadata", "pdfcpu"} {
		require.NotContains(t, string(out), key)
	}
	after, err := convert.Inspect(t.Context(), out)
	require.NoError(t, err)
	require.EqualValues(t, 1, fieldValue(t, after, "PDF:Pages"))
	require.Equal(t, convert.ContentText, fieldValue(t, after, "PDF:Content"), "the page content is untouched")
}

func TestStrip_gpsHEICChangesOnlyMetadataItems(t *testing.T) {
	src := fixture(t, "gps.heic")

	out, err := convert.Strip(t.Context(), src)

	require.NoError(t, err)
	require.Len(t, out, len(src))
	// Exif and XMP are the only places allowed to change; they are separate
	// runs of bytes, far from the container structure and from each other.
	var runs [][2]int
	for i := range src {
		if src[i] == out[i] {
			continue
		}
		if n := len(runs); n > 0 && i-runs[n-1][1] < 64 {
			runs[n-1][1] = i + 1
			continue
		}
		runs = append(runs, [2]int{i, i + 1})
	}
	require.NotEmpty(t, runs)
	require.LessOrEqual(t, len(runs), 2, "runs of changed bytes: %v", runs)
	boxes := bytes.Index(src, []byte("mdat"))
	require.GreaterOrEqual(t, boxes, 0)
	for _, r := range runs {
		require.Greater(t, r[0], boxes, "the box structure before mdat is untouched")
	}
}

// makeIdatHEIC is a container whose Exif item lives in the idat box (iloc
// version 1); method is written to the item's construction_method.
func makeIdatHEIC(exif []byte, method int) (file []byte, exifAt [2]int) {
	infe := box("infe", []byte{2, 0, 0, 0}, be16(1), be16(0), []byte("Exif"), []byte("Exif\x00"))
	iinf := box("iinf", []byte{0, 0, 0, 0}, be16(1), infe)
	iloc := box("iloc", []byte{1, 0, 0, 0}, []byte{0x44, 0x00}, be16(1),
		be16(1), be16(method), be16(0), be16(1), be32(0), be32(len(exif)))
	idat := box("idat", exif)
	meta := box("meta", []byte{0, 0, 0, 0}, iinf, iloc, idat)
	ftyp := box("ftyp", []byte("heic"), be32(0), []byte("mif1heic"))
	file = bytes.Join([][]byte{ftyp, meta, box("mdat", []byte{1, 2, 3})}, nil)
	at := bytes.Index(file, exif)
	return file, [2]int{at, at + len(exif)}
}

func TestStrip_heicConstructionMethods(t *testing.T) {
	exif := append(be32(0), exifTIFF(1)...)
	tests := []struct {
		name   string
		method int
		blank  bool
	}{
		{"idat", 1, true},
		// The item data would be built from other items: there is nothing to
		// blank, and a silent success would leave the Exif where it is.
		{"item offset", 2, false},
		{"unknown", 7, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, at := makeIdatHEIC(exif, tt.method)

			out, err := convert.Strip(t.Context(), src)

			if !tt.blank {
				require.ErrorIs(t, err, convert.ErrUnreadable)
				return
			}
			require.NoError(t, err)
			require.Len(t, out, len(src))
			require.NotContains(t, string(out), "Foo")
			require.Equal(t, src[:at[0]], out[:at[0]])
			require.Equal(t, src[at[1]:], out[at[1]:])
		})
	}
}

// makeExtentHEIC is a container with a 64-byte idat and a 64-byte mdat whose Exif
// item has one extent at off with length n; method is its construction method.
func makeExtentHEIC(method, off, n int) []byte {
	infe := box("infe", []byte{2, 0, 0, 0}, be16(1), be16(0), []byte("Exif"), []byte("Exif\x00"))
	iinf := box("iinf", []byte{0, 0, 0, 0}, be16(1), infe)
	iloc := box("iloc", []byte{1, 0, 0, 0}, []byte{0x44, 0x00}, be16(1),
		be16(1), be16(method), be16(0), be16(1), be32(off), be32(n))
	meta := box("meta", []byte{0, 0, 0, 0}, iinf, iloc, box("idat", bytes.Repeat([]byte{'a'}, 64)))
	ftyp := box("ftyp", []byte("heic"), be32(0), []byte("mif1heic"))
	return bytes.Join([][]byte{ftyp, meta, box("mdat", bytes.Repeat([]byte{'b'}, 64))}, nil)
}

func TestStrip_heicExtentsMustStayInTheirData(t *testing.T) {
	tests := []struct {
		name           string
		method, off, n int
	}{
		{"idat extent runs past the idat body into mdat", 1, 60, 20},
		{"idat extent starts after the body", 1, 70, 4},
		{"file extent inside the meta box", 0, 30, 18},
		{"file extent past the end of the file", 0, 1 << 20, 18},
		{"file extent straddling the start of meta", 0, 20, 18},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := convert.Strip(t.Context(), makeExtentHEIC(tt.method, tt.off, tt.n))

			require.ErrorIs(t, err, convert.ErrUnreadable)
		})
	}
}

func TestStrip_dropsBytesAfterTheEndOfTheImage(t *testing.T) {
	tests := []struct {
		file string
		// valid says the output ends exactly where the picture does.
		valid func(out []byte) bool
	}{
		{"text.png", func(out []byte) bool { return bytes.HasSuffix(out, []byte("IEND\xaeB`\x82")) }},
		{"exif.webp", func(out []byte) bool {
			return uint32(len(out)-8) == binary.LittleEndian.Uint32(out[4:8])
		}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			src := append(fixture(t, tt.file), []byte("TRAILING-SECRET")...)

			out, err := convert.Strip(t.Context(), src)

			require.NoError(t, err)
			require.NotContains(t, string(out), "SECRET")
			require.True(t, tt.valid(out))
		})
	}
}
