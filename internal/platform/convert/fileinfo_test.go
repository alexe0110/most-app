package convert_test

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/go-pdf/fpdf"
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
