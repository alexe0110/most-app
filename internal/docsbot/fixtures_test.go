package docsbot_test

import (
	"sync"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
)

const (
	user     docsbot.UserID = "user-1"
	stranger docsbot.UserID = "user-2"
)

func incoming(unique string) docsbot.IncomingFile {
	return docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, FileID: "f-" + unique,
		FileUniqueID: unique, Title: "Паспорт",
	}
}

func edgeService(repo *docsbottest.FakeRepo, drive docsbottest.FakeDrive) (*docsbot.Service, *docsbottest.FakeUploads) {
	uploads := &docsbottest.FakeUploads{Repo: repo}
	return docsbottest.NewService(repo, drive, docsbottest.FakeFiles{}, uploads, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo}), uploads
}

// parallel runs fn n times at once, released together, for races (run with -race).
func parallel(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn(i)
		}()
	}
	close(start)
	wg.Wait()
}

// runParallel runs fn n times at once and returns the results in call order.
func runParallel(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = fn(i)
		}()
	}
	wg.Wait()
	return errs
}
