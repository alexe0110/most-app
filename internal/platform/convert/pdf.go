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
	// MaxPDFBytes caps one input PDF, and the total of a merge.
	MaxPDFBytes = 20 << 20
	// MaxPDFPages caps the pages of an input (and of a merge result).
	MaxPDFPages = 500
	// maxConcurrentPDF is how many PDF operations run at once.
	maxConcurrentPDF = 2
)

// PDF errors; callers map them to user messages with errors.Is.
var (
	ErrPDFInvalid      = errors.New("convert: invalid pdf")
	ErrPDFTooLarge     = errors.New("convert: pdf exceeds size limit")
	ErrPDFTooManyPages = errors.New("convert: pdf exceeds page limit")
	ErrBadPageRange    = errors.New("convert: bad page range")
	ErrPDFTimeout      = errors.New("convert: pdf operation timed out")
	ErrBadRotation     = errors.New("convert: rotation must be 90, 180 or 270")
)

// pdfSlots is the semaphore of running PDF operations: they hold whole documents
// in memory.
var pdfSlots = make(chan struct{}, maxConcurrentPDF)

func init() {
	// Without this pdfcpu creates its config directory under $HOME on first use.
	// The API is marked deprecated, but the stateless mode alone does not stop it.
	api.DisableConfigDir() //nolint:staticcheck // see above
}

// pdfConf is pdfcpu's built-in configuration: it never touches the config
// directory in $HOME, and validates in relaxed mode (real-world PDFs are sloppy).
func pdfConf() *model.Configuration {
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
	return runPDF(ctx, func(ctx context.Context) ([]byte, error) {
		pages := 0
		readers := make([]io.ReadSeeker, len(pdfs))
		for i, p := range pdfs {
			n, err := pageCount(ctx, p)
			if err != nil {
				return nil, err
			}
			if pages += n; pages > MaxPDFPages {
				return nil, ErrPDFTooManyPages
			}
			readers[i] = bytes.NewReader(p)
		}
		var out bytes.Buffer
		if err := api.MergeRaw(ctx, readers, &out, false, pdfConf()); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	})
}

// SelectPages keeps (keep) or removes the pages of sel ("1-3,5") and returns the
// rest in page order. Keeping or removing so that no page is left is an error.
func SelectPages(ctx context.Context, pdf []byte, sel string, keep bool) ([]byte, error) {
	if err := checkPDFSize(pdf); err != nil {
		return nil, err
	}
	return runPDF(ctx, func(ctx context.Context) ([]byte, error) {
		n, err := pageCount(ctx, pdf)
		if err != nil {
			return nil, err
		}
		picked, err := ParsePageRange(sel, n)
		if err != nil {
			return nil, err
		}
		if !keep {
			picked = complement(picked, n)
		}
		if len(picked) == 0 {
			return nil, fmt.Errorf("%w: no pages would be left", ErrBadPageRange)
		}
		names := make([]string, len(picked))
		for i, p := range picked {
			names[i] = strconv.Itoa(p)
		}
		var out bytes.Buffer
		if err := api.Collect(ctx, bytes.NewReader(pdf), &out, names, pdfConf()); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	})
}

// RotatePDF turns every page clockwise by degrees (90, 180 or 270).
func RotatePDF(ctx context.Context, pdf []byte, degrees int) ([]byte, error) {
	if degrees != 90 && degrees != 180 && degrees != 270 {
		return nil, ErrBadRotation
	}
	if err := checkPDFSize(pdf); err != nil {
		return nil, err
	}
	return runPDF(ctx, func(ctx context.Context) ([]byte, error) {
		if _, err := pageCount(ctx, pdf); err != nil {
			return nil, err
		}
		var out bytes.Buffer
		if err := api.Rotate(ctx, bytes.NewReader(pdf), &out, degrees, nil, pdfConf()); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	})
}

// PageCount returns the number of pages of a PDF within the limits.
func PageCount(ctx context.Context, pdf []byte) (int, error) {
	if err := checkPDFSize(pdf); err != nil {
		return 0, err
	}
	return runPDF(ctx, func(ctx context.Context) (int, error) { return pageCount(ctx, pdf) })
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

// runPDF runs fn under the concurrency cap and ctx's deadline. pdfcpu stops on
// the cancelled ctx by itself, but only at its own checkpoints: after a timeout
// the caller gets ErrPDFTimeout at once while fn may keep running for a while,
// and its slot stays taken until fn really returns. That is deliberate: it caps
// the memory of the work still alive, so timed-out calls can't pile up. Errors
// are mapped to this package's sentinels.
func runPDF[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	select {
	case pdfSlots <- struct{}{}:
	case <-ctx.Done():
		return zero, ErrPDFTimeout
	}
	if ctx.Err() != nil {
		<-pdfSlots
		return zero, ErrPDFTimeout
	}

	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		func() {
			defer func() {
				if p := recover(); p != nil { // pdfcpu panics on some malformed input
					r = result{err: fmt.Errorf("%w: %v", ErrPDFInvalid, p)}
				}
			}()
			r.v, r.err = fn(ctx)
		}()
		<-pdfSlots // free the slot before reporting
		done <- r
	}()

	select {
	case r := <-done:
		return r.v, mapPDFError(ctx, r.err)
	case <-ctx.Done():
		return zero, ErrPDFTimeout
	}
}

// mapPDFError turns a pdfcpu error into one of the package sentinels.
func mapPDFError(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ErrPDFTimeout
	case errors.Is(err, ErrPDFTooManyPages), errors.Is(err, ErrBadPageRange), errors.Is(err, ErrPDFInvalid):
		return err
	default:
		return fmt.Errorf("%w: %w", ErrPDFInvalid, err)
	}
}
