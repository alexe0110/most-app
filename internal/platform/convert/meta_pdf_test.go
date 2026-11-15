package convert_test

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"testing"

	"github.com/go-pdf/fpdf"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// pdfWith builds a one-page PDF; setup may add scripts and attachments.
func pdfWith(t *testing.T, font bool, setup func(*fpdf.Fpdf)) []byte {
	t.Helper()
	p := fpdf.New("P", "mm", "A4", "")
	if setup != nil {
		setup(p)
	}
	p.AddPage()
	if font {
		p.SetFont("Helvetica", "", 12)
		p.Cell(40, 10, "hello")
	}
	var buf bytes.Buffer
	require.NoError(t, p.Output(&buf))
	return buf.Bytes()
}

// appendUpdate saves the file the way an editor does: new Info object, new xref
// section and trailer appended after the original bytes.
func appendUpdate(t *testing.T, pdf []byte) []byte {
	t.Helper()
	startxref := regexp.MustCompile(`startxref\s+(\d+)`).FindSubmatch(pdf)
	trailer := regexp.MustCompile(`/Size (\d+)\s*/Root (\d+) 0 R`).FindSubmatch(pdf)
	require.NotNil(t, startxref)
	require.NotNil(t, trailer, "unexpected trailer layout of the generator")
	size, _ := strconv.Atoi(string(trailer[1]))

	out := bytes.Clone(pdf)
	objOffset := len(out)
	out = fmt.Appendf(out, "\n%d 0 obj\n<</Author (Editor)>>\nendobj\n", size)
	xrefOffset := len(out)
	out = fmt.Appendf(out, "xref\n%d 1\n%010d 00000 n \ntrailer\n<</Size %d/Root %s 0 R/Info %d 0 R/Prev %s>>\nstartxref\n%d\n%%%%EOF\n",
		size, objOffset+1, size+1, trailer[2], size, startxref[1], xrefOffset)
	return out
}

func TestInspect_pdfFacts(t *testing.T) {
	tests := []struct {
		name  string
		pdf   []byte
		field string
		want  any
	}{
		{"fonts: text", pdfWith(t, true, nil), "PDF:Content", convert.ContentText},
		{"no fonts: scan", pdfWith(t, false, nil), "PDF:Content", convert.ContentScan},
		{"A4", pdfWith(t, true, nil), "PDF:PageSize", "A4"},
		{"no script", pdfWith(t, true, nil), "PDF:JavaScript", false},
		{"script", pdfWith(t, true, func(p *fpdf.Fpdf) { p.SetJavascript("app.alert(1);") }), "PDF:JavaScript", true},
		{"no attachment", pdfWith(t, true, nil), "PDF:EmbeddedFiles", false},
		{"attachment", pdfWith(t, true, func(p *fpdf.Fpdf) {
			p.SetAttachments([]fpdf.Attachment{{Content: []byte("x"), Filename: "a.txt"}})
		}), "PDF:EmbeddedFiles", true},
		{"not signed", pdfWith(t, true, nil), "PDF:Signature", false},
		{"original save", pdfWith(t, true, nil), "PDF:IncrementalUpdates", int64(0)},
		{"one edit after creation", appendUpdate(t, pdfWith(t, true, nil)), "PDF:IncrementalUpdates", int64(1)},
		{"a title that spells startxref is not an edit", pdfWith(t, true, func(p *fpdf.Fpdf) {
			p.SetTitle("startxref", false)
		}), "PDF:IncrementalUpdates", int64(0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := convert.Inspect(t.Context(), tt.pdf)

			require.NoError(t, err)
			require.Equal(t, tt.want, fieldValue(t, info, tt.field))
		})
	}
}

func TestInspect_pdfXMPFieldsAreListed(t *testing.T) {
	info, err := convert.Inspect(t.Context(), fixture(t, "word.pdf"))

	require.NoError(t, err)
	require.Equal(t, "Ann Author", fieldValue(t, info, "XMP:Creator"))
	f, _ := info.Field("XMP:Creator")
	require.True(t, f.Sensitive)
}
