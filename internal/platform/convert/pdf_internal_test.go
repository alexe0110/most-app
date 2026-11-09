package convert

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunPDF_panicIsRecoveredAndSlotFreed(t *testing.T) {
	_, err := runPDF(t.Context(), func(context.Context) (int, error) { panic("boom") })

	require.ErrorIs(t, err, ErrPDFInvalid)
	require.Empty(t, pdfSlots, "the slot is free once the call returned")
}

func TestRunPDF_slotHeldUntilWorkReturnsAfterTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})

	_, err := runPDF(ctx, func(context.Context) (int, error) {
		close(started)
		<-release
		return 1, nil
	})

	require.ErrorIs(t, err, ErrPDFTimeout)
	<-started
	require.Len(t, pdfSlots, 1, "the work is still running: its slot stays taken")

	close(release)
	require.Eventually(t, func() bool { return len(pdfSlots) == 0 }, 2*time.Second, 5*time.Millisecond)
}

func TestRunPDF_waitingForSlotRespectsContext(t *testing.T) {
	release := make(chan struct{})
	for range maxConcurrentPDF {
		pdfSlots <- struct{}{}
	}
	defer func() {
		close(release)
		for range maxConcurrentPDF {
			<-pdfSlots
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	_, err := runPDF(ctx, func(context.Context) (int, error) { return 0, nil })

	require.ErrorIs(t, err, ErrPDFTimeout)
}
