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

func TestExportBatches(t *testing.T) {
	photo := func(n int) []docsbot.Document { return repeatDocs(docsbot.KindPhoto, n) }
	file := func(n int) []docsbot.Document { return repeatDocs(docsbot.KindDocument, n) }
	cat := func(parts ...[]docsbot.Document) []docsbot.Document {
		var out []docsbot.Document
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	tests := []struct {
		name string
		in   []docsbot.Document
		want []int // batch sizes
	}{
		{"nothing", nil, nil},
		{"one", photo(1), []int{1}},
		{"ten fit one album", photo(10), []int{10}},
		{"eleven split", photo(11), []int{10, 1}},
		{"twenty five", file(25), []int{10, 10, 5}},
		{"kinds are never mixed", cat(photo(3), file(2), photo(1)), []int{3, 2, 1}},
		{"alternating", cat(photo(1), file(1), photo(1), file(1)), []int{1, 1, 1, 1}},
		{"run continues after a switch", cat(file(1), photo(12)), []int{1, 10, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []int
			total := 0
			for _, batch := range exportBatches(tt.in) {
				got = append(got, len(batch))
				total += len(batch)
				for _, d := range batch {
					require.Equal(t, batch[0].Kind == docsbot.KindPhoto, d.Kind == docsbot.KindPhoto)
				}
			}
			require.Equal(t, tt.want, got)
			require.Equal(t, len(tt.in), total, "nothing lost")
		})
	}
}

func repeatDocs(kind docsbot.Kind, n int) []docsbot.Document {
	out := make([]docsbot.Document, n)
	for i := range out {
		out[i] = docsbot.Document{Kind: kind}
	}
	return out
}
