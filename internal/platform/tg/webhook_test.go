package tg

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWebhookHandler_secret(t *testing.T) {
	const secret = "s3cr3t"

	tests := []struct {
		name         string
		header       string // "" means no header sent
		wantCode     int
		wantDelegate bool
	}{
		{"missing secret", "", http.StatusUnauthorized, false},
		{"wrong secret", "nope", http.StatusUnauthorized, false},
		{"correct secret", secret, http.StatusOK, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var delegated bool
			inner := func(w http.ResponseWriter, _ *http.Request) {
				delegated = true
				w.WriteHeader(http.StatusOK)
			}
			h := webhookHandler(secret, inner)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, webhookPath, strings.NewReader("{}"))
			if tt.header != "" {
				req.Header.Set("X-Telegram-Bot-Api-Secret-Token", tt.header)
			}
			rec := httptest.NewRecorder()

			h(rec, req)

			require.Equal(t, tt.wantCode, rec.Code)
			require.Equal(t, tt.wantDelegate, delegated, "delegates to the SDK handler only on a valid secret")
		})
	}
}
