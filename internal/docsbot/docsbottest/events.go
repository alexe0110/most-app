package docsbottest

import (
	"context"

	"github.com/alexe0110/most-app/internal/docsbot"
)

var _ docsbot.Events = (*FakeRepo)(nil)

// Record stores the event, or fails with EventsErr.
func (r *FakeRepo) Record(_ context.Context, e docsbot.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.EventsErr != nil {
		return r.EventsErr
	}
	r.Events = append(r.Events, e)
	return nil
}

// RecordStart mirrors the transaction: the start event carries source and first,
// and nothing is stored when EventsErr is set.
func (r *FakeRepo) RecordStart(_ context.Context, user docsbot.UserID, source string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.EventsErr != nil {
		return false, r.EventsErr
	}
	if r.Started == nil {
		r.Started = map[docsbot.UserID]bool{}
	}
	first := !r.Started[user]
	r.Started[user] = true
	r.Events = append(r.Events, docsbot.Event{
		User: user, Name: docsbot.EventStart, Props: docsbot.EventProps{Source: source, New: &first},
	})
	return first, nil
}

// EventNames lists the recorded event names, in order.
func (r *FakeRepo) EventNames() []docsbot.EventName {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]docsbot.EventName, 0, len(r.Events))
	for _, e := range r.Events {
		names = append(names, e.Name)
	}
	return names
}
