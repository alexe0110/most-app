package oauth

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
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

func TestConnected(t *testing.T) {
	tests := []struct {
		name   string
		status string // empty means no integration
		want   bool
	}{
		{name: "active", status: statusActive, want: true},
		{name: "revoked", status: statusRevoked, want: false},
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
