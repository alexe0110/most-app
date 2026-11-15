package convert_test

import (
	"archive/zip"
	"bytes"
	"os"
	"testing"
	"time"

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
	require.Equal(t, "Ann Author", fieldValue(t, info, "Office:Creator"))
	require.Equal(t, "Bob Editor", fieldValue(t, info, "Office:LastModifiedBy"))
	require.Equal(t, "Acme", fieldValue(t, info, "Office:Company"))
	require.Equal(t, "Microsoft Office Word", fieldValue(t, info, "Office:Application"))
	require.Equal(t, 2024, fieldValue(t, info, "Office:Created").(convert.Moment).Time.Year())
	require.Equal(t, []convert.MetaBlock{convert.BlockOfficeProps}, info.Blocks)
	require.False(t, info.Clean())
}

func TestInspect_pdf(t *testing.T) {
	info, err := convert.Inspect(t.Context(), makeInfoPDF(t))

	require.NoError(t, err)
	require.Equal(t, convert.FilePDF, info.Type)
	require.Equal(t, "Ann Author", fieldValue(t, info, "PDF:Author"))
	require.EqualValues(t, 1, fieldValue(t, info, "PDF:Pages"))
	require.Equal(t, "Microsoft Word", fieldValue(t, info, "PDF:Creator"))
	require.Equal(t, convert.ContentText, fieldValue(t, info, "PDF:Content"))
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
// fieldValue is the value of the field with the key; the field must exist.
func fieldValue(t testing.TB, info convert.FileInfo, key string) any {
	t.Helper()
	f, ok := info.Field(key)
	require.True(t, ok, "no field %q", key)
	return f.Value
}

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return data
}

var fixtureNames = []string{
	"gps.jpg", "text.png", "exif.webp", "gps.heic", "word.pdf", "author.docx",
	"iphone.jpg", "iphone.heic", "pdf_scan.pdf", "pdf_ironpdf.pdf", "pdf_active.pdf",
}

func TestInspect_fixtures(t *testing.T) {
	phone := func(t testing.TB, info convert.FileInfo) {
		require.Equal(t, "Apple", fieldValue(t, info, "EXIF:Make"))
		require.Equal(t, "iPhone 15 Pro", fieldValue(t, info, "EXIF:Model"))
		require.Equal(t, "17.3.1", fieldValue(t, info, "EXIF:Software"))
		// The wall clock of the EXIF string, whatever zone the reader assumes.
		taken := fieldValue(t, info, "EXIF:DateTimeOriginal").(convert.Moment).Time
		require.Equal(t, [5]int{2026, 2, 19, 14, 22}, [5]int{taken.Year(), int(taken.Month()), taken.Day(), taken.Hour(), taken.Minute()})
		place := fieldValue(t, info, "GPS:Coordinates").(convert.Coordinates)
		require.InDelta(t, 55.7558, place.Lat, 0.001)
		require.InDelta(t, 37.6178, place.Lon, 0.001)
	}
	iphone := func(t testing.TB, info convert.FileInfo) {
		require.Equal(t, "Apple", fieldValue(t, info, "EXIF:Make"))
		require.Equal(t, "iPhone 17", fieldValue(t, info, "EXIF:Model"))
		require.Equal(t, "26.6", fieldValue(t, info, "EXIF:Software"))
		require.Equal(t, "iPhone 17 back camera 6.765mm f/1.78", fieldValue(t, info, "EXIF:LensModel"))
		require.InDelta(t, 1.78, fieldValue(t, info, "EXIF:FNumber"), 0.001)
		require.InDelta(t, 1.0/120, fieldValue(t, info, "EXIF:ExposureTime"), 0.0001)
		require.EqualValues(t, 64, fieldValue(t, info, "EXIF:ISO"))
		require.InDelta(t, 6.765, fieldValue(t, info, "EXIF:FocalLength"), 0.001)
		require.InDelta(t, 812, fieldValue(t, info, "EXIF:GPSAltitude"), 0.5)
		require.InDelta(t, 245, fieldValue(t, info, "EXIF:GPSImgDirection"), 0.5)
		require.Equal(t, convert.Dimensions{Width: 64, Height: 48}, fieldValue(t, info, "Image:Size"))
		taken := fieldValue(t, info, "EXIF:DateTimeOriginal").(convert.Moment)
		_, offset := taken.Time.Zone()
		require.True(t, taken.HasZone)
		require.Equal(t, 5*3600, offset, "the OffsetTimeOriginal tag gives the zone")
		place := fieldValue(t, info, "GPS:Coordinates").(convert.Coordinates)
		require.InDelta(t, 43.26525, place.Lat, 0.00001)
		require.InDelta(t, 76.97681, place.Lon, 0.00001)
	}
	scan := func(t testing.TB, info convert.FileInfo) {
		require.Equal(t, convert.ContentScan, fieldValue(t, info, "PDF:Content"), "no fonts: the pages are pictures")
		require.EqualValues(t, 1, fieldValue(t, info, "PDF:Pages"))
		require.Equal(t, "A4", fieldValue(t, info, "PDF:PageSize"))
		require.Equal(t, "Scan Station", fieldValue(t, info, "PDF:Creator"))
		require.Equal(t, "Scanner Firmware 3.2", fieldValue(t, info, "PDF:Producer"))
		require.EqualValues(t, 0, fieldValue(t, info, "PDF:IncrementalUpdates"))
		require.Equal(t, false, fieldValue(t, info, "PDF:JavaScript"))
	}
	ironPDF := func(t testing.TB, info convert.FileInfo) {
		require.Equal(t, "IronPdf", fieldValue(t, info, "PDF:Creator"))
		require.Equal(t, "IronPdf v2023.1.11387", fieldValue(t, info, "PDF:Producer"))
		require.EqualValues(t, 4, fieldValue(t, info, "PDF:Pages"))
		require.Equal(t, convert.ContentText, fieldValue(t, info, "PDF:Content"))
		require.EqualValues(t, 1, fieldValue(t, info, "PDF:IncrementalUpdates"), "one save after the first")
		modified := fieldValue(t, info, "PDF:ModDate").(convert.Moment)
		require.True(t, modified.Time.Equal(time.Date(2026, 5, 21, 17, 43, 0, 0, time.UTC)))
		require.Equal(t, "uuid:5f2c0d7e-3a9b-4c1e-8d2f-6b7a9e0c1d42", fieldValue(t, info, "XMP:DocumentID"))
	}
	activePDF := func(t testing.TB, info convert.FileInfo) {
		require.Equal(t, true, fieldValue(t, info, "PDF:JavaScript"))
		require.Equal(t, true, fieldValue(t, info, "PDF:EmbeddedFiles"))
		require.Equal(t, true, fieldValue(t, info, "PDF:Signature"))
		require.EqualValues(t, 1, fieldValue(t, info, "PDF:Annotations"), "the comment, not the link-free form fields")
		require.Equal(t, false, fieldValue(t, info, "PDF:Encrypted"))
	}
	tests := []struct {
		file   string
		typ    convert.FileType
		check  func(t testing.TB, info convert.FileInfo)
		blocks []convert.MetaBlock
	}{
		{"iphone.jpg", convert.FileJPEG, iphone, []convert.MetaBlock{convert.BlockEXIF, convert.BlockXMP}},
		{"iphone.heic", convert.FileHEIC, iphone, []convert.MetaBlock{convert.BlockEXIF, convert.BlockXMP}},
		{"pdf_scan.pdf", convert.FilePDF, scan, []convert.MetaBlock{convert.BlockPDFInfo}},
		{"pdf_ironpdf.pdf", convert.FilePDF, ironPDF, []convert.MetaBlock{convert.BlockPDFInfo, convert.BlockXMP}},
		{"pdf_active.pdf", convert.FilePDF, activePDF, []convert.MetaBlock{convert.BlockPDFInfo}},
		{"gps.jpg", convert.FileJPEG, phone, []convert.MetaBlock{convert.BlockEXIF, convert.BlockXMP}},
		{"text.png", convert.FilePNG, phone, []convert.MetaBlock{convert.BlockEXIF, convert.BlockText}},
		{"exif.webp", convert.FileWebP, phone, []convert.MetaBlock{convert.BlockEXIF, convert.BlockXMP}},
		{"gps.heic", convert.FileHEIC, phone, []convert.MetaBlock{convert.BlockEXIF, convert.BlockXMP}},
		{"word.pdf", convert.FilePDF, func(t testing.TB, i convert.FileInfo) {
			require.Equal(t, "Ann Author", fieldValue(t, i, "PDF:Author"))
			require.Equal(t, "Microsoft Word 2016", fieldValue(t, i, "PDF:Creator"))
			require.Equal(t, "Microsoft: Print To PDF", fieldValue(t, i, "PDF:Producer"))
			require.EqualValues(t, 1, fieldValue(t, i, "PDF:Pages"))
			require.Equal(t, convert.ContentText, fieldValue(t, i, "PDF:Content"))
			require.False(t, i.Encrypted)
			require.Equal(t, 2026, fieldValue(t, i, "PDF:CreationDate").(convert.Moment).Time.Year())
			require.Equal(t, time.February, fieldValue(t, i, "PDF:ModDate").(convert.Moment).Time.Month())
		}, []convert.MetaBlock{convert.BlockPDFInfo, convert.BlockXMP}},
		{"author.docx", convert.FileDOCX, func(t testing.TB, i convert.FileInfo) {
			require.Equal(t, "Ann Author", fieldValue(t, i, "Office:Creator"))
			require.Equal(t, "Bob Editor", fieldValue(t, i, "Office:LastModifiedBy"))
			require.Equal(t, "Acme Ltd", fieldValue(t, i, "Office:Company"))
			require.Equal(t, 2026, fieldValue(t, i, "Office:Created").(convert.Moment).Time.Year())
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
			tt.check(t, info)
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
			require.Equal(t, "Ann Author", fieldValue(t, info, "XMP:Creator"))
		})
	}
}

func TestInspect_sensitiveFields(t *testing.T) {
	appWithPath := `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties">` +
		`<Template>C:\Users\ann\Templates\Normal.dotm</Template><Application>Word</Application></Properties>`
	pathDocx := zipOf(t, [][2]string{{"[Content_Types].xml", "<Types/>"}, {"word/document.xml", "<w/>"}, {"docProps/app.xml", appWithPath}})

	tests := []struct {
		name      string
		data      []byte
		sensitive []string
		open      []string
	}{
		{"photo", fixture(t, "gps.jpg"),
			[]string{"GPS:Coordinates", "EXIF:Make", "EXIF:Model", "XMP:Creator"},
			[]string{"EXIF:Software", "EXIF:DateTimeOriginal", "EXIF:Orientation"}},
		{"iphone photo (jpeg)", fixture(t, "iphone.jpg"),
			[]string{"GPS:Coordinates", "EXIF:GPSAltitude", "EXIF:GPSImgDirection", "EXIF:GPSDateStamp", "EXIF:GPSTimeStamp", "EXIF:Make", "EXIF:Model"},
			[]string{"EXIF:Software", "EXIF:LensModel", "EXIF:DateTimeOriginal", "EXIF:GPSVersionID", "XMP:ContentIdentifier"}},
		{"iphone photo (heic)", fixture(t, "iphone.heic"),
			[]string{"GPS:Coordinates", "EXIF:GPSAltitude", "EXIF:GPSImgDirection", "EXIF:GPSDateStamp", "EXIF:GPSTimeStamp", "EXIF:Make", "EXIF:Model"},
			[]string{"EXIF:Software", "EXIF:LensModel", "EXIF:DateTimeOriginal", "EXIF:GPSVersionID"}},
		{"scan has no people or places", fixture(t, "pdf_scan.pdf"), nil, []string{"PDF:Creator", "PDF:Producer"}},
		{"edited pdf from a converter", fixture(t, "pdf_ironpdf.pdf"), nil, []string{"PDF:Creator", "PDF:Producer", "XMP:CreatorTool"}},
		{"pdf", fixture(t, "word.pdf"),
			[]string{"PDF:Author", "XMP:Creator"},
			[]string{"PDF:Creator", "PDF:Producer", "PDF:Pages"}},
		{"office: people", fixture(t, "author.docx"),
			[]string{"Office:Creator", "Office:LastModifiedBy", "Office:Company"},
			[]string{"Office:Application"}},
		{"office: user name in a path", pathDocx, []string{"Office:Template"}, []string{"Office:Application"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := convert.Inspect(t.Context(), tt.data)

			require.NoError(t, err)
			var got []string
			for _, f := range info.SensitiveFields() {
				got = append(got, f.Key)
			}
			require.ElementsMatch(t, tt.sensitive, got)
			for _, key := range tt.open {
				f, ok := info.Field(key)
				require.True(t, ok, key)
				require.False(t, f.Sensitive, key)
			}
		})
	}
}
