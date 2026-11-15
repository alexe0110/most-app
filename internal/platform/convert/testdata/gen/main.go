//go:build ignore

// Generator of the metadata fixtures in the parent directory. Run from the
// repository root:
//
//	go run internal/platform/convert/testdata/gen/main.go
//
// The files are synthetic, so no real person's data is in the repository. The
// HEIC fixture is not made here: it is converted from gps.jpg (see README.md).
package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

const outDir = "internal/platform/convert/testdata"

const xmpPacket = `<?xpacket begin="` + "\xEF\xBB\xBF" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/">
<xmp:CreatorTool>Adobe Photoshop 25.0</xmp:CreatorTool>
<dc:creator><rdf:Seq><rdf:li>Ann Author</rdf:li></rdf:Seq></dc:creator>
</rdf:Description></rdf:RDF></x:xmpmeta>
<?xpacket end="w"?>`

func main() {
	write("gps.jpg", gpsJPEG())
	write("text.png", textPNG())
	write("exif.webp", exifWebP())
	write("word.pdf", wordPDF())
	write("author.docx", authorDOCX())
}

func write(name string, data []byte) {
	if err := os.WriteFile(filepath.Join(outDir, name), data, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%-12s %6d bytes\n", name, len(data))
}

func picture(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255})
		}
	}
	return img
}

// tag is one TIFF entry; val is the already encoded value (big-endian) and
// count the number of elements of the type.
type tag struct {
	id    uint16
	typ   uint16
	count uint32
	val   []byte
}

func ascii(id uint16, s string) tag {
	return tag{id, 2, uint32(len(s) + 1), append([]byte(s), 0)}
}

func short(id, v uint16) tag { return tag{id, 3, 1, binary.BigEndian.AppendUint16(nil, v)} }

func long(id uint16, v uint32) tag { return tag{id, 4, 1, binary.BigEndian.AppendUint32(nil, v)} }

func rationals(id uint16, num ...uint32) tag {
	var v []byte
	for _, n := range num {
		v = binary.BigEndian.AppendUint32(v, n)
		v = binary.BigEndian.AppendUint32(v, 1)
	}
	return tag{id, 5, uint32(len(num)), v}
}

// ifd encodes entries placed at offset base of the TIFF; values longer than four
// bytes follow the table.
func ifd(base int, tags []tag) []byte {
	tableLen := 2 + 12*len(tags) + 4
	out := binary.BigEndian.AppendUint16(nil, uint16(len(tags)))
	var data []byte
	for _, t := range tags {
		out = binary.BigEndian.AppendUint16(out, t.id)
		out = binary.BigEndian.AppendUint16(out, t.typ)
		out = binary.BigEndian.AppendUint32(out, t.count)
		if len(t.val) <= 4 {
			out = append(out, t.val...)
			out = append(out, make([]byte, 4-len(t.val))...)
			continue
		}
		out = binary.BigEndian.AppendUint32(out, uint32(base+tableLen+len(data)))
		data = append(data, t.val...)
		if len(data)%2 == 1 {
			data = append(data, 0)
		}
	}
	out = append(out, 0, 0, 0, 0) // no next IFD
	return append(out, data...)
}

// exifTIFF is a TIFF with camera, software, orientation, shooting time and GPS
// (55 45 21 N, 37 37 4 E). The sub-IFDs follow IFD0, so their offsets are found
// from the sizes of the tables built with placeholder pointers.
func exifTIFF(orientation uint16) []byte {
	ifd0 := func(exifAt, gpsAt int) []byte {
		return ifd(8, []tag{
			ascii(0x010F, "Apple"),
			ascii(0x0110, "iPhone 15 Pro"),
			short(0x0112, orientation),
			ascii(0x0131, "17.3.1"),
			long(0x8769, uint32(exifAt)),
			long(0x8825, uint32(gpsAt)),
		})
	}
	exif := func(at int) []byte { return ifd(at, []tag{ascii(0x9003, "2026:02:19 14:22:05")}) }
	gps := func(at int) []byte {
		return ifd(at, []tag{ascii(1, "N"), rationals(2, 55, 45, 21), ascii(3, "E"), rationals(4, 37, 37, 4)})
	}

	exifAt := 8 + len(ifd0(0, 0))
	gpsAt := exifAt + len(exif(0))
	out := []byte{'M', 'M', 0, 42, 0, 0, 0, 8}
	out = append(out, ifd0(exifAt, gpsAt)...)
	out = append(out, exif(exifAt)...)
	return append(out, gps(gpsAt)...)
}

func segment(marker byte, payload []byte) []byte {
	seg := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(2+len(payload)))
	return append(seg, payload...)
}

// gpsJPEG is a photo "taken on a phone, rotated": EXIF with GPS and Orientation=6
// plus an XMP packet.
func gpsJPEG() []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, picture(64, 48), &jpeg.Options{Quality: 80}); err != nil {
		log.Fatal(err)
	}
	b := buf.Bytes()
	out := append([]byte{}, b[:2]...)
	out = append(out, segment(0xE1, append([]byte("Exif\x00\x00"), exifTIFF(6)...))...)
	out = append(out, segment(0xE1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), xmpPacket...))...)
	return append(out, b[2:]...)
}

func pngChunk(kind string, body []byte) []byte {
	c := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	c = append(c, kind...)
	c = append(c, body...)
	return binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(c[4:]))
}

// textPNG has tEXt, iTXt (XMP) and eXIf chunks after IHDR.
func textPNG() []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, picture(32, 32)); err != nil {
		log.Fatal(err)
	}
	b := buf.Bytes()
	const ihdrEnd = 8 + 25
	out := append([]byte{}, b[:ihdrEnd]...)
	out = append(out, pngChunk("tEXt", []byte("Author\x00Ann Author"))...)
	out = append(out, pngChunk("tEXt", []byte("Software\x00Adobe Photoshop 25.0"))...)
	xmp := append([]byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00"), xmpPacket...)
	out = append(out, pngChunk("iTXt", xmp)...)
	out = append(out, pngChunk("eXIf", exifTIFF(3))...)
	return append(out, b[ihdrEnd:]...)
}

func riffChunk(kind string, body []byte) []byte {
	c := append([]byte(kind), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(c[4:], uint32(len(body)))
	c = append(c, body...)
	if len(body)%2 == 1 {
		c = append(c, 0)
	}
	return c
}

// exifWebP wraps the 1x1 lossless picture of tiny.webp into an extended WebP
// with EXIF and XMP chunks.
func exifWebP() []byte {
	tiny, err := os.ReadFile(filepath.Join(outDir, "tiny.webp"))
	if err != nil {
		log.Fatal(err)
	}
	vp8x := []byte{0x08 | 0x04, 0, 0, 0, 0, 0, 0, 0, 0, 0} // EXIF and XMP flags, 1x1 canvas
	body := []byte("WEBP")
	body = append(body, riffChunk("VP8X", vp8x)...)
	body = append(body, tiny[12:]...) // the VP8L chunk
	body = append(body, riffChunk("EXIF", exifTIFF(1))...)
	body = append(body, riffChunk("XMP ", []byte(xmpPacket))...)
	out := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
	return append(out, body...)
}

// wordPDF is a PDF that looks exported from Word: Info with author and creator,
// and an XMP stream.
func wordPDF() []byte {
	p := fpdf.New("P", "mm", "A4", "")
	p.SetAuthor("Ann Author", true)
	p.SetCreator("Microsoft Word 2016", true)
	p.SetProducer("Microsoft: Print To PDF", true)
	p.SetTitle("Contract draft", true)
	p.SetCreationDate(time.Date(2026, 2, 19, 14, 22, 0, 0, time.UTC))
	p.SetModificationDate(time.Date(2026, 2, 20, 9, 0, 0, 0, time.UTC))
	p.SetXmpMetadata([]byte(xmpPacket))
	p.SetFont("Helvetica", "", 12)
	p.AddPage()
	p.Cell(40, 10, "Hello")
	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		log.Fatal(err)
	}
	return linkXMP(buf.Bytes())
}

// linkXMP points the catalog at the XMP stream. fpdf writes the stream but never
// references it, so the catalog's empty /Names entry is overwritten in place with
// the same number of bytes, which keeps every xref offset valid.
func linkXMP(pdf []byte) []byte {
	obj := regexp.MustCompile(`(\d+) 0 obj\n<< /Type /Metadata`).FindSubmatch(pdf)
	names := regexp.MustCompile(`/Names <<\n/EmbeddedFiles << /Names \[\n\s*\n\] >>\n>>`).FindIndex(pdf)
	if obj == nil || names == nil {
		log.Fatal("unexpected fpdf output")
	}
	ref := fmt.Sprintf("/Metadata %s 0 R", obj[1])
	patch := []byte(ref + strings.Repeat(" ", names[1]-names[0]-len(ref)))
	return append(append(append([]byte{}, pdf[:names[0]]...), patch...), pdf[names[1]:]...)
}

// authorDOCX is a minimal valid Word package with author, last editor, company
// and dates.
func authorDOCX() []byte {
	parts := [][2]string{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/><Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/></Relationships>`},
		{"word/document.xml", `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Hello</w:t></w:r></w:p></w:body></w:document>`},
		{"docProps/core.xml", `<?xml version="1.0" encoding="UTF-8"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:creator>Ann Author</dc:creator><cp:lastModifiedBy>Bob Editor</cp:lastModifiedBy><dcterms:created xsi:type="dcterms:W3CDTF">2026-02-19T14:22:00Z</dcterms:created><dcterms:modified xsi:type="dcterms:W3CDTF">2026-02-20T09:00:00Z</dcterms:modified></cp:coreProperties>`},
		{"docProps/app.xml", `<?xml version="1.0" encoding="UTF-8"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>Microsoft Office Word</Application><Company>Acme Ltd</Company></Properties>`},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: p[0], Method: zip.Deflate, Modified: time.Date(2026, 2, 19, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			log.Fatal(err)
		}
		if _, err := w.Write([]byte(p[1])); err != nil {
			log.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}
	return buf.Bytes()
}
