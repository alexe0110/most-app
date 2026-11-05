package oauth

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestAuthURL(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, nil, &fakeGoogle{})

	raw, err := svc.AuthURL(context.Background(), "user-1", "docs")
	require.NoError(t, err)

	u, err := url.Parse(raw)
	require.NoError(t, err)
	q := u.Query()
	require.Equal(t, "client-id", q.Get("client_id"))
	require.Equal(t, "https://most.example"+CallbackPath, q.Get("redirect_uri"))
	require.Equal(t, scopeDriveFile, q.Get("scope"))
	require.Equal(t, "offline", q.Get("access_type"))
	require.Equal(t, "consent", q.Get("prompt"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.NotEmpty(t, q.Get("code_challenge"))

	state := q.Get("state")
	require.NotEmpty(t, state)
	saved, ok := store.states[state]
	require.True(t, ok, "state must be persisted")
	require.Equal(t, "user-1", saved.UserID)
	require.Equal(t, "docs", saved.Bot)
	require.NotEmpty(t, saved.Verifier)
	require.WithinDuration(t, time.Now().Add(stateTTL), saved.ExpiresAt, time.Minute)
}

func validState(value string) State {
	return State{
		Value:     value,
		UserID:    "user-1",
		Bot:       "docs",
		Verifier:  oauth2.GenerateVerifier(),
		ExpiresAt: time.Now().Add(stateTTL),
	}
}

func TestCallback_success(t *testing.T) {
	store := newFakeStore()
	var connectedUser string
	onConnected := func(_ context.Context, userID string) error {
		connectedUser = userID
		return nil
	}
	svc := newTestService(t, store, onConnected, &fakeGoogle{})
	store.states["state-1"] = validState("state-1")

	rec := doCallback(t, svc, "?code=auth-code&state=state-1")

	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "https://t.me/most_files_bot", rec.Header().Get("Location"))

	ig, err := store.Integration(context.Background(), "user-1", "docs", provider)
	require.NoError(t, err)
	require.Equal(t, statusActive, ig.Status)
	require.NotEmpty(t, ig.RefreshTokenEnc)
	require.NotContains(t, string(ig.RefreshTokenEnc), refreshPlaintext, "stored token must be encrypted")
	require.Equal(t, "user-1", connectedUser)
	_, ok := store.states["state-1"]
	require.False(t, ok, "state must be consumed")
}

func TestCallback_rejected(t *testing.T) {
	tests := []struct {
		name  string
		seed  *State // state to seed before the call, if any
		query string
	}{
		{"user denied", nil, "?error=access_denied"},
		{"missing code", ptr(validState("st")), "?state=st"},
		{"unknown state", nil, "?code=auth-code&state=nope"},
		{"expired state", ptr(expiredState("old")), "?code=auth-code&state=old"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			svc := newTestService(t, store, failConnect(t), &fakeGoogle{})
			if tt.seed != nil {
				store.states[tt.seed.Value] = *tt.seed
			}

			rec := doCallback(t, svc, tt.query)

			require.Equal(t, http.StatusFound, rec.Code)
			require.Empty(t, store.integrations, "nothing is stored on a rejected callback")
		})
	}
}

func TestCallback_reusedStateRejected(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, nil, &fakeGoogle{})
	store.states["st"] = validState("st")

	first := doCallback(t, svc, "?code=auth-code&state=st")
	require.Equal(t, http.StatusFound, first.Code)
	require.Len(t, store.integrations, 1)

	// Wipe the stored integration so a second (wrong) save would show up.
	store.integrations = map[string]Integration{}

	second := doCallback(t, svc, "?code=auth-code&state=st")
	require.Equal(t, http.StatusFound, second.Code)
	require.Empty(t, store.integrations, "a reused state must not store anything")
}

func TestCallback_noRefreshToken(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(t, store, failConnect(t), &fakeGoogle{omitRefresh: true})
	store.states["st"] = validState("st")

	rec := doCallback(t, svc, "?code=auth-code&state=st")

	require.Equal(t, http.StatusFound, rec.Code)
	require.Empty(t, store.integrations)
}

func expiredState(value string) State {
	s := validState(value)
	s.ExpiresAt = time.Now().Add(-time.Minute)
	return s
}

func ptr(s State) *State { return &s }

// failConnect returns an OnConnected that fails the test if it is ever called.
func failConnect(t *testing.T) OnConnected {
	return func(context.Context, string) error {
		t.Error("OnConnected must not run on a rejected callback")
		return nil
	}
}
