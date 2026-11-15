package convert

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

// maxConcurrent is how many image or PDF operations run at once: they hold whole
// decoded images or documents in memory, so one cap serves both kinds.
const maxConcurrent = 2

// ErrTimeout means the operation did not finish before the context ended, or no
// slot became free in time.
var ErrTimeout = errors.New("convert: operation timed out")

// slots is the semaphore of running operations. It is process-wide on purpose:
// the cap protects the memory of the whole process, not of one caller.
var slots = make(chan struct{}, maxConcurrent)

// configDirOnce runs api.DisableConfigDir once. Without it pdfcpu creates its
// config directory under $HOME on first use, and the stateless configuration
// alone does not stop that. The API is deprecated but has no replacement, and
// it sets global state in pdfcpu, so it must not run on every call. A
// package-level sync.Once replaces an init(): importing the package stays
// side-effect free.
var configDirOnce sync.Once

func disableConfigDir() {
	configDirOnce.Do(api.DisableConfigDir) //nolint:staticcheck // see above
}

// run executes fn under the concurrency cap and ctx's deadline. fn reports its
// result through the caller's variables, and the caller must read them only when
// run returned nil: after a timeout fn may still be running and writing.
//
// The decoders and pdfcpu stop on a cancelled ctx only at their own checkpoints,
// so after a timeout the caller gets ErrTimeout at once while fn may keep running
// for a while, and its slot stays taken until fn really returns. That is
// deliberate: it caps the memory of the work still alive, so timed-out calls
// can't pile up. A panic inside fn (pdfcpu does that on some malformed input)
// becomes an error.
func run(ctx context.Context, fn func(ctx context.Context) error) error {
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return ErrTimeout
	}
	if ctx.Err() != nil {
		<-slots
		return ErrTimeout
	}

	done := make(chan error, 1)
	go func() {
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("convert: panic: %v", p)
				}
			}()
			err = fn(ctx)
		}()
		<-slots // free the slot before reporting
		done <- err
	}()

	select {
	case err := <-done:
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ErrTimeout
		}
		return err
	case <-ctx.Done():
		return ErrTimeout
	}
}

// metaOp runs a metadata operation under the shared cap and reduces its failures
// to the sentinels of fileinfo.go: a timeout stays ErrTimeout, and a panic or any
// parser error that is not already one of them means the file is unreadable.
func metaOp(ctx context.Context, fn func(ctx context.Context) error) error {
	err := run(ctx, fn)
	switch {
	case err == nil,
		errors.Is(err, ErrTimeout),
		errors.Is(err, ErrUnsupportedFormat),
		errors.Is(err, ErrUnreadable),
		errors.Is(err, ErrEncrypted),
		errors.Is(err, ErrPDFTooManyPages):
		return err
	default:
		return unreadable(err)
	}
}
