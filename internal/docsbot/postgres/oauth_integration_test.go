//go:build integration

package postgres_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
	"github.com/alexe0110/most-app/internal/platform/oauth"
)

func TestRepo_OAuthState_oneShot(t *testing.T) {
	repo := postgres.NewRepo(dbtest.New(t))
	ctx := t.Context()
	u := makeUser(t, repo, 700)

	st := oauth.State{
		Value:     "state-700",
		UserID:    u.ID.String(),
		Bot:       "docs",
		Verifier:  "verifier-700",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	require.NoError(t, repo.SaveState(ctx, st))

	got, err := repo.TakeState(ctx, "state-700")
	require.NoError(t, err)
	require.Equal(t, u.ID.String(), got.UserID)
	require.Equal(t, "docs", got.Bot)
	require.Equal(t, "verifier-700", got.Verifier)

	// Second take finds nothing: the state is single-use.
	_, err = repo.TakeState(ctx, "state-700")
	require.ErrorIs(t, err, oauth.ErrStateNotFound)
}

func TestRepo_Integration_statusRoundTrip(t *testing.T) {
	repo := postgres.NewRepo(dbtest.New(t))
	ctx := t.Context()
	u := makeUser(t, repo, 701)

	require.NoError(t, repo.SaveIntegration(ctx, u.ID.String(), "docs", "google", "active", []byte{0x01, 0x02}))

	ig, err := repo.Integration(ctx, u.ID.String(), "docs", "google")
	require.NoError(t, err)
	require.Equal(t, "active", ig.Status)
	require.Equal(t, []byte{0x01, 0x02}, ig.RefreshTokenEnc)

	require.NoError(t, repo.UpdateIntegrationStatus(ctx, u.ID.String(), "docs", "google", "revoked"))
	ig, err = repo.Integration(ctx, u.ID.String(), "docs", "google")
	require.NoError(t, err)
	require.Equal(t, "revoked", ig.Status)

	_, err = repo.Integration(ctx, u.ID.String(), "docs", "unknown")
	require.ErrorIs(t, err, oauth.ErrNoIntegration)
}

// TestOAuthFlow_tokenEncryptedAtRest runs a full callback against a DB-backed
// store and a fake Google, then reads the raw column to prove the refresh token
// is never stored in the clear.
func TestOAuthFlow_tokenEncryptedAtRest(t *testing.T) {
	const refreshPlaintext = "refresh-super-secret"

	pool := dbtest.New(t)
	repo := postgres.NewRepo(pool)
	ctx := t.Context()
	u := makeUser(t, repo, 702)

	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600,"refresh_token":"`+refreshPlaintext+`"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(google.Close)

	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x07}, 32))
	svc, err := oauth.New(oauth.Config{
		ClientID: "id", ClientSecret: "secret", TokenEncKey: key,
		PublicURL: "https://most.example", BotURL: "https://t.me/most_files_bot",
	}, repo, nil, slog.New(slog.DiscardHandler),
		oauth.WithEndpoint(oauth2.Endpoint{AuthURL: google.URL + "/auth", TokenURL: google.URL + "/token"}),
	)
	require.NoError(t, err)

	// Seed a valid state in the DB, as AuthURL would.
	require.NoError(t, repo.SaveState(ctx, oauth.State{
		Value:     "state-702",
		UserID:    u.ID.String(),
		Bot:       "docs",
		Verifier:  oauth2.GenerateVerifier(),
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}))

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, oauth.CallbackPath+"?code=auth-code&state=state-702", nil)
	rec := httptest.NewRecorder()
	svc.Callback()(rec, req)
	require.Equal(t, http.StatusFound, rec.Code)

	var raw []byte
	require.NoError(t, pool.QueryRow(ctx,
		"select refresh_token_enc from integrations where user_id = $1", u.ID).Scan(&raw))
	require.NotEmpty(t, raw)
	require.NotContains(t, string(raw), refreshPlaintext, "refresh token must be encrypted at rest")
}
