package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// Events stores product events in the platform events table; it implements the
// domain's Events port.
type Events struct {
	pool *pgxpool.Pool
	q    *Queries
}

var _ docsbot.Events = (*Events)(nil)

// NewEvents wires Events around an existing pool.
func NewEvents(pool *pgxpool.Pool) *Events {
	return &Events{pool: pool, q: New(pool)}
}

// eventProps is the JSON stored in events.props. Empty fields are left out, and
// the struct is the only way to get a prop in: free text cannot slip through.
type eventProps struct {
	Source    string `json:"source,omitempty"`
	New       *bool  `json:"new,omitempty"`
	Category  string `json:"category,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Operation string `json:"operation,omitempty"`
}

// Record stores one event; an empty user id stores it anonymously.
func (r *Events) Record(ctx context.Context, e docsbot.Event) error {
	return storeEvent(ctx, r.q, e)
}

// RecordStart stores the start event and, for the user's first start with a
// source, the user's attribution, in one transaction. The user row is locked
// first, so parallel starts agree on which one is the first.
func (r *Events) RecordStart(ctx context.Context, user docsbot.UserID, source string) (bool, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return false, err
	}
	var first bool
	err = inTx(ctx, r.pool, func(q *Queries, _ pgx.Tx) error {
		startedAt, err := q.LockUserFirstStart(ctx, uid)
		if err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		first = !startedAt.Valid
		e := docsbot.Event{User: user, Name: docsbot.EventStart, Props: docsbot.EventProps{Source: source, New: &first}}
		if err := storeEvent(ctx, q, e); err != nil {
			return err
		}
		if !first {
			return nil
		}
		if err := q.MarkFirstStart(ctx, uid); err != nil {
			return fmt.Errorf("mark first start: %w", err)
		}
		if source != "" {
			if err := q.SetUserSource(ctx, SetUserSourceParams{ID: uid, Source: pgtype.Text{String: source, Valid: true}}); err != nil {
				return fmt.Errorf("set user source: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return first, nil
}

// storeEvent writes e with q, which may be bound to a transaction.
func storeEvent(ctx context.Context, q *Queries, e docsbot.Event) error {
	var uid pgtype.UUID // invalid: stored as NULL
	if e.User != "" {
		var err error
		if uid, err = parseUUID(string(e.User)); err != nil {
			return err
		}
	}
	props, err := json.Marshal(eventProps{
		Source: e.Props.Source, New: e.Props.New, Category: string(e.Props.Category),
		Kind: string(e.Props.Kind), Operation: string(e.Props.Operation),
	})
	if err != nil {
		return fmt.Errorf("marshal event props: %w", err)
	}
	err = q.InsertEvent(ctx, InsertEventParams{UserID: uid, Bot: docsbot.BotName, Name: string(e.Name), Props: props})
	if err != nil {
		return fmt.Errorf("insert event %s: %w", e.Name, err)
	}
	return nil
}
