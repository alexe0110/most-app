// Package docsbottest holds in-memory fakes of the docsbot ports, shared by the
// tests of the domain and of its Telegram adapters. Import it from _test.go
// files only (depguard enforces this).
package docsbottest

import (
	"context"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// TestUserID is a user id in the UUID form the database uses, for tests that
// seed documents of one user.
const TestUserID = "11111111-1111-1111-1111-111111111111"

// NewService is the one place tests build a Service from fakes. uploads doubles
// as the JobCanceler when it is one (FakeUploads records the cancels); repo doubles
// as the Events port too (FakeRepo records the events).
func NewService(repo repoWithEvents, drive docsbot.Drive, files docsbot.FileSource, uploads docsbot.Uploads, folders docsbot.Folders, status docsbot.StatusMessages, syncs docsbot.Syncs, opts ...docsbot.Option) *docsbot.Service {
	jobs, ok := uploads.(docsbot.JobCanceler)
	if !ok {
		jobs = NoJobs{}
	}
	return docsbot.NewService(docsbot.Ports{
		Repo: repo, Drive: drive, Files: files, Uploads: uploads, Jobs: jobs,
		Folders: folders, Status: status, Syncs: syncs, Events: repo,
	}, opts...)
}

// repoWithEvents is the repository fake with the events port next to it, as
// FakeRepo is.
type repoWithEvents interface {
	docsbot.Repo
	docsbot.Events
}

// NewDeps builds a Service over fresh fakes with the chosen Drive state and
// returns the repository behind it. Files are never reached and uploads accept
// every choice without a queue.
func NewDeps(drive FakeDrive) (*docsbot.Service, *FakeRepo) {
	repo := NewFakeRepo()
	repo.Status = NewFakeStatus(repo)
	uploads := &FakeUploads{Repo: repo}
	return NewService(repo, drive, NoFiles{}, uploads, repo, repo.Status, &FakeSyncs{Repo: repo}), repo
}

// NoEvents is an Events port that records nothing.
type NoEvents struct{}

func (NoEvents) Record(context.Context, docsbot.Event) error { return nil }
func (NoEvents) RecordStart(context.Context, docsbot.UserID, string) (bool, error) {
	return false, nil
}

// NoJobs is a JobCanceler that cancels nothing.
type NoJobs struct{}

func (NoJobs) CancelUploads(context.Context, docsbot.UserID) error  { return nil }
func (NoJobs) CancelUserJobs(context.Context, docsbot.UserID) error { return nil }
