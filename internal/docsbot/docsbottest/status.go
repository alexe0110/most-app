package docsbottest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/alexe0110/most-app/internal/docsbot"
)

type FakeStatus struct {
	mu   sync.Mutex // guards msgs and seq; documents are guarded by repo.mu
	Repo *FakeRepo
	Msgs map[docsbot.StatusMessageID]docsbot.StatusMessage
	seq  int
}

var _ docsbot.StatusMessages = (*FakeStatus)(nil)

func NewFakeStatus(repo *FakeRepo) *FakeStatus {
	return &FakeStatus{Repo: repo, Msgs: map[docsbot.StatusMessageID]docsbot.StatusMessage{}}
}

func (s *FakeStatus) OpenForUser(_ context.Context, user docsbot.UserID) (docsbot.StatusMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best docsbot.StatusMessage
	found := false
	for _, m := range s.Msgs {
		if m.UserID == user && !m.Closed && (!found || m.CreatedAt.After(best.CreatedAt)) {
			best, found = m, true
		}
	}
	return best, found, nil
}

func (s *FakeStatus) Create(_ context.Context, user docsbot.UserID, chatID int64, messageID int, kind docsbot.StatusKind) (docsbot.StatusMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	m := docsbot.StatusMessage{
		ID:        docsbot.StatusMessageID(fmt.Sprintf("sm-%d", s.seq)),
		UserID:    user,
		ChatID:    chatID,
		MessageID: messageID,
		Kind:      kind,
		CreatedAt: time.Now(),
	}
	s.Msgs[m.ID] = m
	return m, nil
}

// PurgeClosed drops closed messages created before olderThan (the fake keeps no close time).
func (s *FakeStatus) PurgeClosed(_ context.Context, olderThan time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, m := range s.Msgs {
		if m.Closed && m.CreatedAt.Before(olderThan) {
			delete(s.Msgs, id)
			n++
		}
	}
	return n, nil
}

func (s *FakeStatus) Close(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID) error {
	return s.update(user, id, func(m *docsbot.StatusMessage) { m.Closed = true })
}

// update applies fn to the user's status message; a foreign one is a silent no-op.
func (s *FakeStatus) update(user docsbot.UserID, id docsbot.StatusMessageID, fn func(*docsbot.StatusMessage)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.Msgs[id]; ok && m.UserID == user {
		fn(&m)
		s.Msgs[id] = m
	}
	return nil
}

func (s *FakeStatus) SetMessageID(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID, messageID int) error {
	return s.update(user, id, func(m *docsbot.StatusMessage) { m.MessageID = messageID })
}

func (s *FakeStatus) Attach(_ context.Context, user docsbot.UserID, doc docsbot.DocumentID, id docsbot.StatusMessageID) error {
	s.Repo.mu.Lock()
	defer s.Repo.mu.Unlock()
	if d, ok := s.Repo.Docs[doc]; ok && d.UserID == user {
		d.StatusMessageID = id
		s.Repo.Docs[doc] = d
	}
	return nil
}

func (s *FakeStatus) DocumentsIn(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID) ([]docsbot.Document, error) {
	s.Repo.mu.Lock()
	defer s.Repo.mu.Unlock()
	var out []docsbot.Document
	for _, d := range s.Repo.Docs {
		if d.UserID == user && d.StatusMessageID == id {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *FakeStatus) ByID(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID) (docsbot.StatusMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.Msgs[id]
	if !ok || m.UserID != user {
		return docsbot.StatusMessage{}, docsbot.ErrNotFound
	}
	return m, nil
}
