package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// seedIntegration stores an active integration with an encrypted refresh token.
func seedIntegration(t *testing.T, svc *Service, store *fakeStore, status string) {
	t.Helper()
	enc, err := svc.enc.encrypt([]byte("refresh-abc"))
	require.NoError(t, err)
	store.integrations[igKey("user-1", "docs", provider)] = Integration{RefreshTokenEnc: enc, Status: status}
}

func TestClient_success(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, nil, &fakeGoogle{})
	seedIntegration(t, svc, store, statusActive)

	client, err := svc.Client(context.Background(), "user-1", "docs")
	require.NoError(t, err)
	require.NotNil(t, client)
}

func TestClient_invalidGrantRevokes(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, nil, &fakeGoogle{invalidGrant: true})
	seedIntegration(t, svc, store, statusActive)

	_, err := svc.Client(context.Background(), "user-1", "docs")
	require.ErrorIs(t, err, ErrAccessRevoked)

	ig, err := store.Integration(context.Background(), "user-1", "docs", provider)
	require.NoError(t, err)
	require.Equal(t, statusRevoked, ig.Status, "a rejected grant marks the integration revoked")
}

func TestClient_alreadyRevoked(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, nil, &fakeGoogle{})
	seedIntegration(t, svc, store, statusRevoked)

	_, err := svc.Client(context.Background(), "user-1", "docs")
	require.ErrorIs(t, err, ErrAccessRevoked, "a revoked integration short-circuits without hitting Google")
}

func TestClient_noIntegration(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, nil, &fakeGoogle{})

	_, err := svc.Client(context.Background(), "user-1", "docs")
	require.ErrorIs(t, err, ErrNoIntegration)
}

func TestRevoke(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, nil, &fakeGoogle{})
	seedIntegration(t, svc, store, statusActive)

	require.NoError(t, svc.Revoke(context.Background(), "user-1", "docs"))

	ig, err := store.Integration(context.Background(), "user-1", "docs", provider)
	require.NoError(t, err)
	require.Equal(t, statusRevoked, ig.Status)
}

func TestRevokeBestEffort(t *testing.T) {
	t.Run("google failure still revokes locally", func(t *testing.T) {
		store := newFakeStore()
		svc := newTestService(t, store, nil, &fakeGoogle{revokeStatus: http.StatusInternalServerError})
		seedIntegration(t, svc, store, statusActive)

		require.NoError(t, svc.RevokeBestEffort(context.Background(), "user-1", "docs"))

		ig, err := store.Integration(context.Background(), "user-1", "docs", provider)
		require.NoError(t, err)
		require.Equal(t, statusRevoked, ig.Status, "a failed Google revoke still disables the integration locally")
	})
	t.Run("no integration is a no-op", func(t *testing.T) {
		store := newFakeStore()
		svc := newTestService(t, store, nil, &fakeGoogle{})

		require.NoError(t, svc.RevokeBestEffort(context.Background(), "user-1", "docs"))
	})
}

func TestClient_alreadyDisconnected(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, nil, &fakeGoogle{})
	seedIntegration(t, svc, store, statusDisconnected)

	_, err := svc.Client(context.Background(), "user-1", "docs")
	require.ErrorIs(t, err, ErrAccessDisconnected)
	require.NotErrorIs(t, err, ErrAccessRevoked)
}

func TestDisconnectBestEffort(t *testing.T) {
	t.Run("marks disconnected", func(t *testing.T) {
		store := newFakeStore()
		svc := newTestService(t, store, nil, &fakeGoogle{})
		seedIntegration(t, svc, store, statusActive)

		require.NoError(t, svc.DisconnectBestEffort(context.Background(), "user-1", "docs"))

		ig, err := store.Integration(context.Background(), "user-1", "docs", provider)
		require.NoError(t, err)
		require.Equal(t, statusDisconnected, ig.Status)
	})
	t.Run("google failure still disconnects locally", func(t *testing.T) {
		store := newFakeStore()
		svc := newTestService(t, store, nil, &fakeGoogle{revokeStatus: http.StatusInternalServerError})
		seedIntegration(t, svc, store, statusActive)

		require.NoError(t, svc.DisconnectBestEffort(context.Background(), "user-1", "docs"))

		ig, err := store.Integration(context.Background(), "user-1", "docs", provider)
		require.NoError(t, err)
		require.Equal(t, statusDisconnected, ig.Status)
	})
	t.Run("no integration is a no-op", func(t *testing.T) {
		store := newFakeStore()
		svc := newTestService(t, store, nil, &fakeGoogle{})

		require.NoError(t, svc.DisconnectBestEffort(context.Background(), "user-1", "docs"))
	})
}

func TestConnected(t *testing.T) {
	tests := []struct {
		name   string
		status string // empty means no integration
		want   bool
	}{
		{name: "active", status: statusActive, want: true},
		{name: "revoked", status: statusRevoked, want: false},
		{name: "disconnected", status: statusDisconnected, want: false},
		{name: "no integration", status: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			svc := newTestService(t, store, nil, &fakeGoogle{})
			if tt.status != "" {
				seedIntegration(t, svc, store, tt.status)
			}

			got, err := svc.Connected(context.Background(), "user-1", "docs")
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// hangingGoogle never answers, so only a client timeout can end a request.
func hangingGoogle(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); ts.Close() })
	return ts
}

func TestNew_defaultClientHasTimeout(t *testing.T) {
	svc := newTestService(t, newFakeStore(), nil, &fakeGoogle{})
	require.Equal(t, requestTimeout, svc.httpClient.Timeout)
}

func TestClient_hungTokenEndpointTimesOut(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, nil, &fakeGoogle{})
	ts := hangingGoogle(t)
	svc.endpoint = oauth2.Endpoint{TokenURL: ts.URL}
	svc.httpClient = &http.Client{Timeout: 50 * time.Millisecond}
	seedIntegration(t, svc, store, statusActive)

	start := time.Now()
	_, err := svc.Client(t.Context(), "user-1", "docs")

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAccessRevoked, "a timeout is not a revoked grant")
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestRevoke_hungEndpointTimesOut(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, nil, &fakeGoogle{})
	svc.revokeURL = hangingGoogle(t).URL
	svc.httpClient = &http.Client{Timeout: 50 * time.Millisecond}
	seedIntegration(t, svc, store, statusActive)

	require.Error(t, svc.Revoke(t.Context(), "user-1", "docs"))
	// The best-effort variant still ends up marked revoked locally.
	require.NoError(t, svc.RevokeBestEffort(t.Context(), "user-1", "docs"))
	ig, err := store.Integration(t.Context(), "user-1", "docs", provider)
	require.NoError(t, err)
	require.Equal(t, statusRevoked, ig.Status)
}
