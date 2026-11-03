package logging_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/logging"
)

func TestNew_level(t *testing.T) {
	tests := []struct {
		name      string
		level     string
		wantDebug bool
		wantInfo  bool
		wantWarn  bool
	}{
		{"debug enables everything", "debug", true, true, true},
		{"warn drops debug and info", "warn", false, false, true},
		{"garbage falls back to info", "мусор", false, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := logging.New(tt.level)
			ctx := context.Background()

			require.Equal(t, tt.wantDebug, log.Enabled(ctx, slog.LevelDebug))
			require.Equal(t, tt.wantInfo, log.Enabled(ctx, slog.LevelInfo))
			require.Equal(t, tt.wantWarn, log.Enabled(ctx, slog.LevelWarn))
		})
	}
}
