package telegram

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestExportGap_scalesWithAlbumSize(t *testing.T) {
	d := &docs{pauseFile: 50 * time.Millisecond, pauseAlbum: time.Second}
	require.Equal(t, 50*time.Millisecond, d.exportGap(1))
	require.Equal(t, 2*time.Second, d.exportGap(2))
	require.Equal(t, 10*time.Second, d.exportGap(10))
}

func TestExportGuard(t *testing.T) {
	d := &docs{exports: map[docsbot.UserID]bool{}}
	require.True(t, d.startExport("a"))
	require.False(t, d.startExport("a"), "one export at a time per user")
	require.True(t, d.startExport("b"), "other users are independent")
	d.finishExport("a")
	require.True(t, d.startExport("a"), "released after finish")
}

func TestRetryOnRateLimit(t *testing.T) {
	d := &docs{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	limited := &bot.TooManyRequestsError{Message: "slow down", RetryAfter: 0}
	boom := io.ErrUnexpectedEOF
	tests := []struct {
		name      string
		results   []error
		wantCalls int
		wantErr   error
	}{
		{"ok", []error{nil}, 1, nil},
		{"other error is not retried", []error{boom}, 1, boom},
		{"429 then ok", []error{limited, nil}, 2, nil},
		{"429 twice gives up", []error{limited, limited}, 2, limited},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			err := d.retryOnRateLimit(t.Context(), func() error {
				calls++
				return tt.results[calls-1]
			})
			require.Equal(t, tt.wantCalls, calls)
			require.Equal(t, tt.wantErr, err)
		})
	}
}

func TestRenameStore_dropMessage(t *testing.T) {
	s := newRenameStore(time.Now)
	s.set(1, "a", 100, 70)
	s.set(2, "b", 100, 70)
	s.set(3, "c", 100, 71)
	s.dropMessage(100, 70)
	_, ok := s.take(1)
	require.False(t, ok)
	_, ok = s.take(2)
	require.False(t, ok)
	_, ok = s.take(3)
	require.True(t, ok, "another message is untouched")
}
