package convert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// Limits that keep a hostile or huge PDF from exhausting memory or CPU.
const (
	// MaxInputBytes caps one input file of any operation, and the total of a PDF
	// merge. It equals the Bot API download limit tg.MaxFileBytes, not imported
	// to keep this package free of the Telegram SDK.
	MaxInputBytes = 20 << 20
	// MaxPDFBytes is MaxInputBytes under its older, PDF-specific name.
	MaxPDFBytes = MaxInputBytes
	// MaxPDFPages caps the pages of an input (and of a merge result).
	MaxPDFPages = 500
)

// PDF errors; callers map them to user messages with errors.Is.
var (
	ErrPDFInvalid      = errors.New("convert: invalid pdf")
	ErrPDFTooLarge     = errors.New("convert: pdf exceeds size limit")
	ErrPDFTooManyPages = errors.New("convert: pdf exceeds page limit")
	ErrBadPageRange    = errors.New("convert: bad page range")
	ErrBadRotation     = errors.New("convert: rotation must be 90, 180 or 270")
)

// pdfConf is pdfcpu's built-in configuration: it never touches the config
// directory in $HOME, and validates in relaxed mode (real-world PDFs are sloppy).
func pdfConf() *model.Configuration {
	disableConfigDir()
	conf := model.NewStatelessConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	return conf
}

// MergePDF joins the PDFs in order into one.
func MergePDF(ctx context.Context, pdfs [][]byte) ([]byte, error) {
	if len(pdfs) == 0 {
		return nil, ErrPDFInvalid
	}
	total := 0
	for _, p := range pdfs {
		if total += len(p); total > MaxPDFBytes {
			return nil, ErrPDFTooLarge
		}
	}
	var out bytes.Buffer
	err := pdfOp(ctx, func(ctx context.Context) error {
		pages := 0
		readers := make([]io.ReadSeeker, len(pdfs))
		for i, p := range pdfs {
			n, err := pageCount(ctx, p)
			if err != nil {
				return err
			}
			if pages += n; pages > MaxPDFPages {
				return ErrPDFTooManyPages
			}
			readers[i] = bytes.NewReader(p)
		}
		return api.MergeRaw(ctx, readers, &out, false, pdfConf())
	})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// SelectPages keeps (keep) or removes the pages of sel ("1-3,5") and returns the
// rest in page order. Keeping or removing so that no page is left is an error.
func SelectPages(ctx context.Context, pdf []byte, sel string, keep bool) ([]byte, error) {
	if err := checkPDFSize(pdf); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err := pdfOp(ctx, func(ctx context.Context) error {
		n, err := pageCount(ctx, pdf)
		if err != nil {
			return err
		}
		picked, err := ParsePageRange(sel, n)
		if err != nil {
			return err
		}
		if !keep {
			picked = complement(picked, n)
		}
		if len(picked) == 0 {
			return fmt.Errorf("%w: no pages would be left", ErrBadPageRange)
		}
		names := make([]string, len(picked))
		for i, p := range picked {
			names[i] = strconv.Itoa(p)
		}
		return api.Collect(ctx, bytes.NewReader(pdf), &out, names, pdfConf())
	})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// RotatePDF turns every page clockwise by degrees (90, 180 or 270).
func RotatePDF(ctx context.Context, pdf []byte, degrees int) ([]byte, error) {
	if degrees != 90 && degrees != 180 && degrees != 270 {
		return nil, ErrBadRotation
	}
	if err := checkPDFSize(pdf); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	err := pdfOp(ctx, func(ctx context.Context) error {
		if _, err := pageCount(ctx, pdf); err != nil {
			return err
		}
		return api.Rotate(ctx, bytes.NewReader(pdf), &out, degrees, nil, pdfConf())
	})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// PageCount returns the number of pages of a PDF within the limits.
func PageCount(ctx context.Context, pdf []byte) (int, error) {
	if err := checkPDFSize(pdf); err != nil {
		return 0, err
	}
	var n int
	err := pdfOp(ctx, func(ctx context.Context) (err error) {
		n, err = pageCount(ctx, pdf)
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ParsePageRange reads a selection like "1-3, 5" into sorted, distinct page
// numbers within 1..pageCount. Empty parts, reversed or out-of-range pages and
// anything but digits, commas, dashes and spaces are errors.
func ParsePageRange(s string, pageCount int) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("%w: empty", ErrBadPageRange)
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		lo, hi, err := parseRangePart(part)
		if err != nil {
			return nil, err
		}
		if lo < 1 || hi > pageCount {
			return nil, fmt.Errorf("%w: out of 1..%d", ErrBadPageRange, pageCount)
		}
		for p := lo; p <= hi; p++ {
			seen[p] = true
		}
	}
	pages := make([]int, 0, len(seen))
	for p := range seen {
		pages = append(pages, p)
	}
	slices.Sort(pages)
	return pages, nil
}

// parseRangePart reads "5" or "2-4".
func parseRangePart(part string) (lo, hi int, err error) {
	from, to, isRange := strings.Cut(part, "-")
	if lo, err = parsePageNumber(from); err != nil {
		return 0, 0, err
	}
	if !isRange {
		return lo, lo, nil
	}
	if hi, err = parsePageNumber(to); err != nil {
		return 0, 0, err
	}
	if lo > hi {
		return 0, 0, fmt.Errorf("%w: reversed range", ErrBadPageRange)
	}
	return lo, hi, nil
}

func parsePageNumber(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 6 {
		return 0, fmt.Errorf("%w: not a page number", ErrBadPageRange)
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%w: not a page number", ErrBadPageRange)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%w: not a page number", ErrBadPageRange)
	}
	return n, nil
}

// complement lists the pages of 1..n that are not in picked (sorted).
func complement(picked []int, n int) []int {
	var rest []int
	for p := 1; p <= n; p++ {
		if _, found := slices.BinarySearch(picked, p); !found {
			rest = append(rest, p)
		}
	}
	return rest
}

func checkPDFSize(pdf []byte) error {
	switch {
	case len(pdf) == 0:
		return ErrPDFInvalid
	case len(pdf) > MaxPDFBytes:
		return ErrPDFTooLarge
	}
	return nil
}

// pageCount reads and validates a PDF and returns its page count, refusing a
// document over the page limit before anything else is done with it.
func pageCount(ctx context.Context, pdf []byte) (int, error) {
	n, err := api.PageCount(ctx, bytes.NewReader(pdf), pdfConf())
	if err != nil {
		return 0, err
	}
	if n > MaxPDFPages {
		return 0, ErrPDFTooManyPages
	}
	return n, nil
}

// pdfOp runs a PDF operation under the shared cap and maps its errors to this
// package's sentinels.
func pdfOp(ctx context.Context, fn func(ctx context.Context) error) error {
	return mapPDFError(ctx, run(ctx, fn))
}

// mapPDFError turns a pdfcpu error into one of the package sentinels.
func mapPDFError(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil, errors.Is(err, ErrTimeout):
		return ErrTimeout
	case errors.Is(err, ErrPDFTooManyPages), errors.Is(err, ErrBadPageRange), errors.Is(err, ErrPDFInvalid):
		return err
	default:
		return fmt.Errorf("%w: %w", ErrPDFInvalid, err)
	}
}
