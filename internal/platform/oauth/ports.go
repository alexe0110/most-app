package oauth

import (
	"context"
	"time"
)

// State is a one-time OAuth state with its PKCE verifier, bound to a user and a
// bot of the series.
type State struct {
	Value     string
	UserID    string // internal user id (UUID)
	Bot       string
	Verifier  string // PKCE code verifier
	ExpiresAt time.Time
}

// Integration is a stored provider connection: the encrypted refresh token and
// its status.
type Integration struct {
	RefreshTokenEnc []byte
	Status          string
}

// Store persists OAuth states and provider integrations. The interface is
// declared here, at the consumer; docsbot/postgres implements it. provider is
// passed explicitly so the adapter stays generic over the integrations table.
type Store interface {
	SaveState(ctx context.Context, s State) error
	// TakeState fetches and deletes a state in one shot, returning
	// ErrStateNotFound if it is missing (unknown or already used).
	TakeState(ctx context.Context, state string) (State, error)

	SaveIntegration(ctx context.Context, userID, bot, provider, status string, refreshTokenEnc []byte) error
	// Integration returns the stored connection or ErrNoIntegration.
	Integration(ctx context.Context, userID, bot, provider string) (Integration, error)
	UpdateIntegrationStatus(ctx context.Context, userID, bot, provider, status string) error
}

// OnConnected is called after a successful Drive connection. The bot provides
// it to notify the user in Telegram; a returned error is logged, not fatal.
type OnConnected func(ctx context.Context, userID string) error
