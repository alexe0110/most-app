package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alexe0110/most-app/internal/platform/oauth"
)

// OAuthStore persists OAuth states and integrations; it implements oauth.Store.
type OAuthStore struct{ q *Queries }

var _ oauth.Store = (*OAuthStore)(nil)

// NewOAuthStore wires OAuthStore around an existing pool.
func NewOAuthStore(pool *pgxpool.Pool) *OAuthStore {
	return &OAuthStore{q: New(pool)}
}

// This file implements the platform/oauth Store port over the oauth_states and
// integrations tables. The interface is declared by the consumer (platform/oauth).

// SaveState persists a one-time OAuth state.
func (r *OAuthStore) SaveState(ctx context.Context, s oauth.State) error {
	uid, err := parseUUID(s.UserID)
	if err != nil {
		return err
	}
	if err := r.q.CreateOAuthState(ctx, CreateOAuthStateParams{
		State:     s.Value,
		UserID:    uid,
		Bot:       s.Bot,
		Verifier:  s.Verifier,
		ExpiresAt: pgtype.Timestamptz{Time: s.ExpiresAt, Valid: true},
	}); err != nil {
		return fmt.Errorf("create oauth state: %w", err)
	}
	return nil
}

// TakeState fetches and deletes a state, translating "no rows" into the domain
// error the service expects.
func (r *OAuthStore) TakeState(ctx context.Context, state string) (oauth.State, error) {
	row, err := r.q.TakeOAuthState(ctx, state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return oauth.State{}, oauth.ErrStateNotFound
		}
		return oauth.State{}, fmt.Errorf("take oauth state: %w", err)
	}
	return oauth.State{
		Value:     state,
		UserID:    row.UserID.String(),
		Bot:       row.Bot,
		Verifier:  row.Verifier,
		ExpiresAt: row.ExpiresAt.Time,
	}, nil
}

// SaveIntegration upserts the encrypted token and status for (user, bot, provider).
func (r *OAuthStore) SaveIntegration(ctx context.Context, userID, bot, provider, status string, refreshTokenEnc []byte) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	if _, err := r.q.UpsertIntegration(ctx, UpsertIntegrationParams{
		UserID:          uid,
		Bot:             bot,
		Provider:        provider,
		RefreshTokenEnc: refreshTokenEnc,
		Status:          status,
	}); err != nil {
		return fmt.Errorf("upsert integration: %w", err)
	}
	return nil
}

// Integration returns the stored connection or oauth.ErrNoIntegration.
func (r *OAuthStore) Integration(ctx context.Context, userID, bot, provider string) (oauth.Integration, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return oauth.Integration{}, err
	}
	ig, err := r.q.GetIntegration(ctx, GetIntegrationParams{UserID: uid, Bot: bot, Provider: provider})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return oauth.Integration{}, oauth.ErrNoIntegration
		}
		return oauth.Integration{}, fmt.Errorf("get integration: %w", err)
	}
	return oauth.Integration{RefreshTokenEnc: ig.RefreshTokenEnc, Status: ig.Status}, nil
}

// UpdateIntegrationStatus sets the status for (user, bot, provider).
func (r *OAuthStore) UpdateIntegrationStatus(ctx context.Context, userID, bot, provider, status string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	if err := r.q.SetIntegrationStatus(ctx, SetIntegrationStatusParams{
		UserID:   uid,
		Bot:      bot,
		Provider: provider,
		Status:   status,
	}); err != nil {
		return fmt.Errorf("set integration status: %w", err)
	}
	return nil
}
