package convert

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// holdSlots takes every slot until the returned function is called.
func holdSlots(t *testing.T) (release func()) {
	t.Helper()
	for range maxConcurrent {
		slots <- struct{}{}
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			for range maxConcurrent {
				<-slots
			}
		})
	}
	t.Cleanup(release)
	return release
}

func TestRun_panicIsRecoveredAndSlotFreed(t *testing.T) {
	err := pdfOp(t.Context(), func(context.Context) error { panic("boom") })

	require.ErrorIs(t, err, ErrPDFInvalid)
	require.Empty(t, slots, "the slot is free once the call returned")
}

func TestRun_slotHeldUntilWorkReturnsAfterTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})

	err := run(ctx, func(context.Context) error {
		close(started)
		<-release
		return nil
	})

	require.ErrorIs(t, err, ErrTimeout)
	<-started
	require.Len(t, slots, 1, "the work is still running: its slot stays taken")

	close(release)
	require.Eventually(t, func() bool { return len(slots) == 0 }, 2*time.Second, 5*time.Millisecond)
}

func TestRun_neverRunsMoreThanTheCap(t *testing.T) {
	var mu sync.Mutex
	var running, peak int
	var wg sync.WaitGroup
	for range 3 * maxConcurrent {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := run(t.Context(), func(context.Context) error {
				mu.Lock()
				running++
				peak = max(peak, running)
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				running--
				mu.Unlock()
				return nil
			})
			require.NoError(t, err)
		}()
	}
	wg.Wait()

	require.Equal(t, maxConcurrent, peak, "work overlaps up to the cap and not beyond")
}

// Image and PDF operations share one cap: with every slot taken each of them
// waits for a slot and gives up with the context.
func TestOperations_shareOneSemaphore(t *testing.T) {
	jpg := mustJPEG(t, noiseImage(16, 16), 80)
	pdf, err := ImagesToPDF(t.Context(), [][]byte{jpg})
	require.NoError(t, err)

	tests := []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"ImagesToPDF", func(ctx context.Context) error { _, err := ImagesToPDF(ctx, [][]byte{jpg}); return err }},
		{"ToJPEG", func(ctx context.Context) error { _, err := ToJPEG(ctx, jpg); return err }},
		{"Compress", func(ctx context.Context) error { _, err := Compress(ctx, jpg, 1<<20, JPEG); return err }},
		{"PageCount", func(ctx context.Context) error { _, err := PageCount(ctx, pdf); return err }},
		{"MergePDF", func(ctx context.Context) error { _, err := MergePDF(ctx, [][]byte{pdf, pdf}); return err }},
		{"SelectPages", func(ctx context.Context) error { _, err := SelectPages(ctx, pdf, "1", true); return err }},
		{"RotatePDF", func(ctx context.Context) error { _, err := RotatePDF(ctx, pdf, 90); return err }},
	}
	holdSlots(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()

			require.ErrorIs(t, tt.call(ctx), ErrTimeout)
		})
	}
}

func TestOperations_workDoesNotStartOnExpiredContext(t *testing.T) {
	jpg := mustJPEG(t, noiseImage(16, 16), 80)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := ToJPEG(ctx, jpg)

	require.ErrorIs(t, err, ErrTimeout)
	require.Empty(t, slots)
}
