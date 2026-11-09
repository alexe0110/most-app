package convert_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/go-pdf/fpdf"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// makePDF builds a PDF of n pages; page i says "page i".
func makePDF(t *testing.T, n int) []byte {
	t.Helper()
	p := fpdf.New("P", "mm", "A4", "")
	p.SetFont("Helvetica", "", 12)
	for i := 1; i <= n; i++ {
		p.AddPage()
		p.Cell(40, 10, "page "+string(rune('0'+i%10)))
	}
	var buf bytes.Buffer
	require.NoError(t, p.Output(&buf))
	return buf.Bytes()
}

func pagesOf(t *testing.T, pdf []byte) int {
	t.Helper()
	n, err := convert.PageCount(t.Context(), pdf)
	require.NoError(t, err)
	return n
}

func TestMergePDF(t *testing.T) {
	out, err := convert.MergePDF(t.Context(), [][]byte{makePDF(t, 2), makePDF(t, 3), makePDF(t, 1)})

	require.NoError(t, err)
	require.Equal(t, 6, pagesOf(t, out))
}

func TestMergePDF_limits(t *testing.T) {
	_, err := convert.MergePDF(t.Context(), nil)
	require.ErrorIs(t, err, convert.ErrPDFInvalid)

	big := make([]byte, convert.MaxPDFBytes/2+1)
	_, err = convert.MergePDF(t.Context(), [][]byte{big, big})
	require.ErrorIs(t, err, convert.ErrPDFTooLarge, "the total counts, not each file")

	_, err = convert.MergePDF(t.Context(), [][]byte{makePDF(t, 300), makePDF(t, 300)})
	require.ErrorIs(t, err, convert.ErrPDFTooManyPages, "the result would exceed the page limit")
}

func TestSelectPages(t *testing.T) {
	tests := []struct {
		name string
		sel  string
		keep bool
		want int
	}{
		{"keep range", "1-2", true, 2},
		{"keep scattered", "1, 3,5", true, 3},
		{"keep duplicates", "2,2,1-2", true, 2},
		{"remove range", "2-3", false, 3},
		{"remove one", "1", false, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := convert.SelectPages(t.Context(), makePDF(t, 5), tt.sel, tt.keep)
			require.NoError(t, err)
			require.Equal(t, tt.want, pagesOf(t, out))
		})
	}
}

func TestSelectPages_cannotEmptyTheDocument(t *testing.T) {
	_, err := convert.SelectPages(t.Context(), makePDF(t, 3), "1-3", false)
	require.ErrorIs(t, err, convert.ErrBadPageRange)
}

func TestSelectPages_badRange(t *testing.T) {
	_, err := convert.SelectPages(t.Context(), makePDF(t, 3), "4", true)
	require.ErrorIs(t, err, convert.ErrBadPageRange)
}

func TestRotatePDF(t *testing.T) {
	for _, deg := range []int{90, 180, 270} {
		out, err := convert.RotatePDF(t.Context(), makePDF(t, 2), deg)
		require.NoError(t, err)
		require.Equal(t, 2, pagesOf(t, out))

		ctx, err := api.ReadContext(t.Context(), bytes.NewReader(out), model.NewDefaultConfiguration())
		require.NoError(t, err)
		require.NoError(t, api.ValidateContext(t.Context(), ctx))
		for page := 1; page <= 2; page++ {
			_, _, inh, err := ctx.PageDict(t.Context(), page, false)
			require.NoError(t, err)
			require.Equal(t, deg, inh.Rotate, "page %d", page)
		}
	}
}

func TestRotatePDF_badDegrees(t *testing.T) {
	_, err := convert.RotatePDF(t.Context(), makePDF(t, 1), 45)
	require.ErrorIs(t, err, convert.ErrBadRotation)
}

func TestPDF_garbageIsInvalid(t *testing.T) {
	garbage := []byte("this is definitely not a pdf")

	_, err := convert.PageCount(t.Context(), garbage)
	require.ErrorIs(t, err, convert.ErrPDFInvalid)
	_, err = convert.RotatePDF(t.Context(), garbage, 90)
	require.ErrorIs(t, err, convert.ErrPDFInvalid)
	_, err = convert.SelectPages(t.Context(), garbage, "1", true)
	require.ErrorIs(t, err, convert.ErrPDFInvalid)
	_, err = convert.MergePDF(t.Context(), [][]byte{garbage, garbage})
	require.ErrorIs(t, err, convert.ErrPDFInvalid)
	_, err = convert.PageCount(t.Context(), nil)
	require.ErrorIs(t, err, convert.ErrPDFInvalid)
}

func TestPDF_tooManyPages(t *testing.T) {
	_, err := convert.PageCount(t.Context(), makePDF(t, 501))
	require.ErrorIs(t, err, convert.ErrPDFTooManyPages)

	n, err := convert.PageCount(t.Context(), makePDF(t, 500))
	require.NoError(t, err)
	require.Equal(t, 500, n)
}

func TestPDF_tooLarge(t *testing.T) {
	_, err := convert.PageCount(t.Context(), make([]byte, convert.MaxPDFBytes+1))
	require.ErrorIs(t, err, convert.ErrPDFTooLarge)
}

func TestPDF_cancelledContextTimesOut(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := convert.RotatePDF(ctx, makePDF(t, 1), 90)
	require.ErrorIs(t, err, convert.ErrPDFTimeout)

	ctx, cancel = context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	_, err = convert.PageCount(ctx, makePDF(t, 1))
	require.ErrorIs(t, err, convert.ErrPDFTimeout)
}

func TestParsePageRange(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		n       int
		want    []int
		wantErr bool
	}{
		{"single", "3", 5, []int{3}, false},
		{"range", "2-4", 5, []int{2, 3, 4}, false},
		{"mixed with spaces", " 1 - 2 , 5 ", 5, []int{1, 2, 5}, false},
		{"unsorted and duplicates", "5,1,1-2", 5, []int{1, 2, 5}, false},
		{"single-page range", "2-2", 5, []int{2}, false},
		{"empty", "", 5, nil, true},
		{"blank", "   ", 5, nil, true},
		{"empty part", "1,,2", 5, nil, true},
		{"trailing comma", "1,", 5, nil, true},
		{"reversed", "4-2", 5, nil, true},
		{"zero", "0", 5, nil, true},
		{"over the end", "1-6", 5, nil, true},
		{"negative", "-3", 5, nil, true},
		{"open range", "2-", 5, nil, true},
		{"double dash", "1--3", 5, nil, true},
		{"letters", "a-b", 5, nil, true},
		{"words", "все", 5, nil, true},
		{"huge", "99999999999999999999", 5, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := convert.ParsePageRange(tt.in, tt.n)
			if tt.wantErr {
				require.ErrorIs(t, err, convert.ErrBadPageRange)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
