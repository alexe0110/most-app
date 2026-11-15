package convert_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-pdf/fpdf"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// zipOf builds a zip with the given parts, in order.
func zipOf(t *testing.T, parts [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.Create(p[0])
		require.NoError(t, err)
		_, err = w.Write([]byte(p[1]))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

const docxCore = `<?xml version="1.0" encoding="UTF-8"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/">
<dc:creator>Ann Author</dc:creator><cp:lastModifiedBy>Bob Editor</cp:lastModifiedBy>
<dcterms:created>2024-03-01T10:00:00Z</dcterms:created><dcterms:modified>2024-03-02T11:30:00Z</dcterms:modified>
</cp:coreProperties>`

const docxApp = `<?xml version="1.0" encoding="UTF-8"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Company>Acme</Company><Application>Microsoft Office Word</Application></Properties>`

func makeDOCX(t *testing.T) []byte {
	t.Helper()
	return zipOf(t, [][2]string{
		{"[Content_Types].xml", `<Types/>`},
		{"word/document.xml", `<w:document/>`},
		{"docProps/core.xml", docxCore},
		{"docProps/app.xml", docxApp},
	})
}

func makeInfoPDF(t *testing.T) []byte {
	t.Helper()
	p := fpdf.New("P", "mm", "A4", "")
	p.SetAuthor("Ann Author", true)
	p.SetCreator("Microsoft Word", true)
	p.SetFont("Helvetica", "", 12)
	p.AddPage()
	p.Cell(40, 10, "hello")
	var buf bytes.Buffer
	require.NoError(t, p.Output(&buf))
	return buf.Bytes()
}

func TestDetectType(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want convert.FileType
		ok   bool
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, convert.FileJPEG, true},
		{"png", []byte("\x89PNG\r\n\x1a\n...."), convert.FilePNG, true},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), convert.FileWebP, true},
		{"heic", []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), convert.FileHEIC, true},
		{"heif by compatible brand", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mif1heic"), convert.FileHEIC, true},
		{"avif is not heic", []byte("\x00\x00\x00\x18ftypavif\x00\x00\x00\x00mif1avif"), "", false},
		{"mp4 is not heic", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00isomiso2"), "", false},
		{"pdf", []byte("%PDF-1.7\n"), convert.FilePDF, true},
		{"docx", makeDOCX(t), convert.FileDOCX, true},
		{"xlsx", zipOf(t, [][2]string{{"[Content_Types].xml", "<Types/>"}, {"xl/workbook.xml", "<w/>"}}), convert.FileXLSX, true},
		{"pptx", zipOf(t, [][2]string{{"[Content_Types].xml", "<Types/>"}, {"ppt/presentation.xml", "<p/>"}}), convert.FilePPTX, true},
		{"plain zip", zipOf(t, [][2]string{{"a.txt", "x"}}), "", false},
		{"text", []byte("hello"), "", false},
		{"empty", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := convert.DetectType(tt.data)

			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestInspect_unsupported(t *testing.T) {
	_, err := convert.Inspect(t.Context(), []byte("not a file at all"))

	require.ErrorIs(t, err, convert.ErrUnsupportedFormat)
}

func TestInspect_office(t *testing.T) {
	info, err := convert.Inspect(t.Context(), makeDOCX(t))

	require.NoError(t, err)
	require.Equal(t, convert.FileDOCX, info.Type)
	require.Equal(t, "Ann Author", info.People.Author)
	require.Equal(t, "Bob Editor", info.People.LastModifiedBy)
	require.Equal(t, "Acme", info.People.Company)
	require.Equal(t, "Microsoft Office Word", info.Capture.Software)
	require.Equal(t, 2024, info.Dates.Created.Year())
	require.Equal(t, []convert.MetaBlock{convert.BlockOfficeProps}, info.Blocks)
	require.False(t, info.Clean())
}

func TestInspect_pdf(t *testing.T) {
	info, err := convert.Inspect(t.Context(), makeInfoPDF(t))

	require.NoError(t, err)
	require.Equal(t, convert.FilePDF, info.Type)
	require.Equal(t, "Ann Author", info.People.Author)
	require.NotNil(t, info.PDF)
	require.Equal(t, 1, info.PDF.Pages)
	require.Equal(t, "Microsoft Word", info.PDF.Creator)
	require.False(t, info.PDF.Scanned)
	require.Contains(t, info.Blocks, convert.BlockPDFInfo)
}

func TestInspect_corruptImageIsUnreadable(t *testing.T) {
	for name, data := range map[string][]byte{
		"jpeg": {0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF, 0x00},
		"png":  []byte("\x89PNG\r\n\x1a\n\x00\x00\xFF\xFFIHDR"),
		"webp": []byte("RIFF\xFF\xFF\xFF\x7FWEBPVP8 "),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := convert.Inspect(t.Context(), data)

			require.ErrorIs(t, err, convert.ErrUnreadable)
		})
	}
}

func TestInspectAndStrip_sizeLimit(t *testing.T) {
	big := append([]byte{0xFF, 0xD8, 0xFF}, make([]byte, convert.MaxInputBytes)...)

	_, err := convert.Inspect(t.Context(), big)
	require.ErrorIs(t, err, convert.ErrFileTooLarge)

	_, err = convert.Strip(t.Context(), big)
	require.ErrorIs(t, err, convert.ErrFileTooLarge)
}

// fixture reads a file made by testdata/gen/main.go.
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return data
}

var fixtureNames = []string{"gps.jpg", "text.png", "exif.webp", "gps.heic", "word.pdf", "author.docx"}

func TestInspect_fixtures(t *testing.T) {
	phone := func(info convert.FileInfo) {
		require.Equal(t, "Apple", info.Capture.Make)
		require.Equal(t, "iPhone 15 Pro", info.Capture.Model)
		require.Equal(t, "17.3.1", info.Capture.Software)
		// The wall clock of the EXIF string, whatever zone the reader assumes.
		taken := info.Capture.Taken
		require.Equal(t, [5]int{2026, 2, 19, 14, 22}, [5]int{taken.Year(), int(taken.Month()), taken.Day(), taken.Hour(), taken.Minute()})
		require.NotNil(t, info.Location)
		require.InDelta(t, 55.7558, info.Location.Lat, 0.001)
		require.InDelta(t, 37.6178, info.Location.Lon, 0.001)
	}
	tests := []struct {
		file   string
		typ    convert.FileType
		check  func(info convert.FileInfo)
		blocks []convert.MetaBlock
	}{
		{"gps.jpg", convert.FileJPEG, phone, []convert.MetaBlock{convert.BlockEXIF, convert.BlockXMP}},
		{"text.png", convert.FilePNG, phone, []convert.MetaBlock{convert.BlockEXIF, convert.BlockText}},
		{"exif.webp", convert.FileWebP, phone, []convert.MetaBlock{convert.BlockEXIF, convert.BlockXMP}},
		{"gps.heic", convert.FileHEIC, phone, []convert.MetaBlock{convert.BlockEXIF, convert.BlockXMP}},
		{"word.pdf", convert.FilePDF, func(i convert.FileInfo) {
			require.Equal(t, "Ann Author", i.People.Author)
			require.NotNil(t, i.PDF)
			require.Equal(t, "Microsoft Word 2016", i.PDF.Creator)
			require.Equal(t, "Microsoft: Print To PDF", i.PDF.Producer)
			require.Equal(t, 1, i.PDF.Pages)
			require.False(t, i.PDF.Scanned)
			require.False(t, i.PDF.Encrypted)
			require.Equal(t, 2026, i.Dates.Created.Year())
			require.Equal(t, time.February, i.Dates.Modified.Month())
		}, []convert.MetaBlock{convert.BlockPDFInfo, convert.BlockXMP}},
		{"author.docx", convert.FileDOCX, func(i convert.FileInfo) {
			require.Equal(t, "Ann Author", i.People.Author)
			require.Equal(t, "Bob Editor", i.People.LastModifiedBy)
			require.Equal(t, "Acme Ltd", i.People.Company)
			require.Equal(t, 2026, i.Dates.Created.Year())
		}, []convert.MetaBlock{convert.BlockOfficeProps}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			data := fixture(t, tt.file)

			info, err := convert.Inspect(t.Context(), data)

			require.NoError(t, err)
			require.Equal(t, tt.typ, info.Type)
			require.Equal(t, len(data), info.Size)
			require.ElementsMatch(t, tt.blocks, info.Blocks)
			tt.check(info)
		})
	}
}

// The author of a photo may be only in XMP (dc:creator), as Photoshop and
// Lightroom exports have it.
func TestInspect_authorFromXMP(t *testing.T) {
	for _, name := range []string{"gps.jpg", "exif.webp"} {
		t.Run(name, func(t *testing.T) {
			info, err := convert.Inspect(t.Context(), fixture(t, name))

			require.NoError(t, err)
			require.Equal(t, "Ann Author", info.People.Author)
		})
	}
}

func TestInspectAndStrip_brokenFilesAreUnreadable(t *testing.T) {
	zeros := make([]byte, 64)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "jpeg: no scan", data: append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x04, 0x00, 0x00}, zeros...)},
		{name: "jpeg: segment longer than file", data: []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x7F, 0xFF, 'E', 'x'}},
		{name: "png: garbage after signature", data: append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0xAB}, 40)...)},
		{name: "png: chunk longer than file", data: append([]byte("\x89PNG\r\n\x1a\n"), 0x7F, 0xFF, 0xFF, 0xFF, 'I', 'H', 'D', 'R')},
		{name: "webp: size past the end", data: []byte("RIFF\xFF\xFF\xFF\x7FWEBPVP8X\xFF\xFF\xFF\x7F")},
		{name: "heic: no meta box", data: append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), zeros...)},
		{name: "heic: box bigger than file", data: []byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic\x7F\xFF\xFF\xFFmeta")},
		{name: "pdf: garbage", data: append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{0xAB}, 80)...)},
		{name: "pdf: header only", data: []byte("%PDF-1.7\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := convert.Inspect(t.Context(), tt.data)
			require.ErrorIs(t, err, convert.ErrUnreadable)
			require.NotContains(t, err.Error(), "panic")

			_, err = convert.Strip(t.Context(), tt.data)
			require.ErrorIs(t, err, convert.ErrUnreadable)
		})
	}
}

func TestInspectAndStrip_truncatedFilesNeverPanic(t *testing.T) {
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			data := fixture(t, name)
			step := max(len(data)/200, 1)
			for cut := 0; cut < len(data); cut += step {
				_, err := convert.Inspect(t.Context(), data[:cut])
				requireNoCrash(t, err)

				_, err = convert.Strip(t.Context(), data[:cut])
				requireNoCrash(t, err)
			}
		})
	}
}

// requireNoCrash accepts success or a sentinel error and rejects a parser panic,
// which run turns into ErrUnreadable with a "panic" cause.
func requireNoCrash(t testing.TB, err error) {
	t.Helper()
	if err == nil {
		return
	}
	require.True(t, errors.Is(err, convert.ErrUnreadable) || errors.Is(err, convert.ErrUnsupportedFormat) ||
		errors.Is(err, convert.ErrEncrypted) || errors.Is(err, convert.ErrTimeout), "unexpected error: %v", err)
	require.False(t, strings.Contains(err.Error(), "panic"), "parser panicked: %v", err)
}

func TestInspect_sizeLimitIsExact(t *testing.T) {
	atLimit := append([]byte{0xFF, 0xD8, 0xFF, 0xD9}, make([]byte, convert.MaxInputBytes-4)...)

	_, err := convert.Inspect(t.Context(), atLimit)

	require.NotErrorIs(t, err, convert.ErrFileTooLarge, "the limit itself is allowed")
}

func TestInspectAndStrip_cancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			data := fixture(t, name)

			_, err := convert.Inspect(ctx, data)
			require.ErrorIs(t, err, convert.ErrTimeout)

			_, err = convert.Strip(ctx, data)
			require.ErrorIs(t, err, convert.ErrTimeout)
		})
	}
}

func FuzzInspectStrip(f *testing.F) {
	for _, name := range fixtureNames {
		f.Add(fixture(f, name))
	}
	f.Add(makeJPEG(f, exifSegment(6)))
	f.Fuzz(func(t *testing.T, data []byte) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := convert.Inspect(ctx, data)
		requireNoCrash(t, err)

		out, err := convert.Strip(ctx, data)
		requireNoCrash(t, err)
		if err != nil {
			return
		}
		// A copy the bot would send must read back as clean.
		after, err := convert.Inspect(ctx, out)
		requireNoCrash(t, err)
		if err == nil {
			require.True(t, after.Clean(), "blocks left after Strip: %v", after.Blocks)
		}
	})
}

// encryptedPDF protects the info PDF with the given user password; an empty one
// leaves only the owner password, which still marks the file as encrypted.
func encryptedPDF(t *testing.T, userPW string) []byte {
	t.Helper()
	conf := model.NewDefaultConfiguration()
	conf.UserPW, conf.OwnerPW = userPW, "owner-pw"
	var out bytes.Buffer
	require.NoError(t, api.Encrypt(t.Context(), bytes.NewReader(makeInfoPDF(t)), &out, conf))
	return out.Bytes()
}

func TestInspectAndStrip_encryptedPDF(t *testing.T) {
	for name, userPW := range map[string]string{"user password": "user-pw", "owner password only": ""} {
		t.Run(name, func(t *testing.T) {
			data := encryptedPDF(t, userPW)

			info, err := convert.Inspect(t.Context(), data)

			require.NoError(t, err, "an encrypted PDF is a fact about the file, not a failure")
			require.Equal(t, convert.FilePDF, info.Type)
			require.NotNil(t, info.PDF)
			require.True(t, info.PDF.Encrypted)
			if userPW != "" {
				require.Empty(t, info.People.Author, "nothing can be read without the password")
			}

			_, err = convert.Strip(t.Context(), data)
			require.ErrorIs(t, err, convert.ErrEncrypted)
		})
	}
}

// bombDOCX is a package whose core.xml declares a huge title but compresses to a few
// kilobytes.
func bombDOCX(t *testing.T, payloadBytes int) []byte {
	t.Helper()
	core := `<?xml version="1.0"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` +
		strings.Repeat("a", payloadBytes) + `</dc:title><dc:creator>Ann</dc:creator></cp:coreProperties>`
	return zipOf(t, [][2]string{
		{"[Content_Types].xml", `<Types/>`},
		{"word/document.xml", `<w:document/>`},
		{"docProps/core.xml", core},
	})
}

func TestInspectAndStrip_officeBomb(t *testing.T) {
	data := bombDOCX(t, 32<<20)
	require.Less(t, len(data), 1<<20, "the package itself is small")

	start := time.Now()
	_, err := convert.Inspect(t.Context(), data)

	require.ErrorIs(t, err, convert.ErrUnreadable, "a part past the read cap is refused, not read to the end")
	require.Less(t, time.Since(start), 5*time.Second)

	out, err := convert.Strip(t.Context(), data)
	require.NoError(t, err, "stripping copies entries raw and never inflates the bomb")
	info, err := convert.Inspect(t.Context(), out)
	require.NoError(t, err)
	require.True(t, info.Clean())
}

func TestInspect_officeValuesAreEscapedForDisplay(t *testing.T) {
	core := `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<dc:creator>&lt;b&gt;Ann&lt;/b&gt; &amp; Co&#x202E;gnp.exe</dc:creator></cp:coreProperties>`
	data := zipOf(t, [][2]string{{"[Content_Types].xml", "<Types/>"}, {"word/document.xml", "<w/>"}, {"docProps/core.xml", core}})

	info, err := convert.Inspect(t.Context(), data)

	require.NoError(t, err)
	require.Equal(t, "<b>Ann</b> & Cognp.exe", info.People.Author, "raw text, no invisible overrides; HTML escaping is the renderer's job")
}

// Editors and macOS screenshots put only an XMP packet (an iTXt chunk) into a
// PNG; the metadata reader doesn't look there, so the chunk must be counted
// from the container, or the file is called clean while it is not.
func TestInspect_pngWithOnlyXMPIsNotClean(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, 8, 8))))
	b := buf.Bytes()
	xmp := append([]byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00"),
		`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:creator><rdf:Seq><rdf:li>Ann Author</rdf:li></rdf:Seq></dc:creator></rdf:Description></rdf:RDF></x:xmpmeta>`...)
	src := append(append(append([]byte{}, b[:33]...), pngChunkBytes("iTXt", xmp)...), b[33:]...)

	info, err := convert.Inspect(t.Context(), src)

	require.NoError(t, err)
	require.False(t, info.Clean(), "the XMP chunk is metadata")

	out, err := convert.Strip(t.Context(), src)
	require.NoError(t, err)
	require.NotContains(t, string(out), "Ann Author", "and Strip does remove it")
}
