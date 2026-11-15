package convert_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

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
			require.True(t, info.Encrypted)
			if userPW != "" {
				_, found := info.Field("PDF:Author")
				require.False(t, found, "nothing can be read without the password")
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
	require.Equal(t, "<b>Ann</b> & Cognp.exe", fieldValue(t, info, "Office:Creator"), "raw text, no invisible overrides; HTML escaping is the renderer's job")
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

func TestInspectAndStrip_pdfPageLimit(t *testing.T) {
	src := makePDF(t, convert.MaxPDFPages+1)

	_, err := convert.Inspect(t.Context(), src)
	require.ErrorIs(t, err, convert.ErrPDFTooManyPages)

	_, err = convert.Strip(t.Context(), src)
	require.ErrorIs(t, err, convert.ErrPDFTooManyPages)
}
