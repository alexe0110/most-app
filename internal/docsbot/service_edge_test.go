package docsbot_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

const stranger docsbot.UserID = "user-2"

func edgeService(repo *fakeRepo, drive fakeDrive) (*docsbot.Service, *fakeUploads) {
	uploads := &fakeUploads{repo: repo}
	return docsbot.NewService(repo, drive, fakeFiles{}, uploads, repo, newFakeStatus(repo)), uploads
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

func TestService_RetryBackup_onlyFromFailed(t *testing.T) {
	tests := []struct {
		name    string
		status  docsbot.BackupStatus
		owner   docsbot.UserID
		wantErr error
	}{
		{"failed is retried", docsbot.BackupFailed, user, nil},
		{"backed up", docsbot.BackupBackedUp, user, docsbot.ErrNotRetryable},
		{"uploading", docsbot.BackupUploading, user, docsbot.ErrNotRetryable},
		{"pending", docsbot.BackupPending, user, docsbot.ErrNotRetryable},
		{"none", docsbot.BackupNone, user, docsbot.ErrNotRetryable},
		{"too large", docsbot.BackupTooLarge, user, docsbot.ErrNotRetryable},
		{"someone else's failed document", docsbot.BackupFailed, stranger, docsbot.ErrNotRetryable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{UserID: tt.owner, Status: docsbot.StatusSaved, BackupStatus: tt.status})
			svc, uploads := edgeService(repo, fakeDrive{connected: true})

			err := svc.RetryBackup(t.Context(), user, doc.ID)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Empty(t, uploads.requeued)
				require.Equal(t, tt.status, repo.docs[doc.ID].BackupStatus, "state must stay untouched")
				return
			}
			require.NoError(t, err)
			require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.requeued)
			require.Equal(t, docsbot.BackupPending, repo.docs[doc.ID].BackupStatus)
		})
	}
}

func TestService_RetryBackup_doubleTapQueuesOnce(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	svc, uploads := edgeService(repo, fakeDrive{connected: true})

	require.NoError(t, svc.RetryBackup(t.Context(), user, doc.ID))
	require.ErrorIs(t, svc.RetryBackup(t.Context(), user, doc.ID), docsbot.ErrNotRetryable)
	require.Len(t, uploads.requeued, 1)
}

func TestService_RetryBackup_parallelTapsQueueOnce(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	svc, uploads := edgeService(repo, fakeDrive{connected: true})

	errs := runParallel(20, func(int) error { return svc.RetryBackup(t.Context(), user, doc.ID) })

	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		require.ErrorIs(t, err, docsbot.ErrNotRetryable)
	}
	require.Equal(t, 1, ok)
	require.Len(t, uploads.requeued, 1)
}

// searchSpy records the query that reaches the repository.
type searchSpy struct {
	*fakeRepo
	calls int
	query string
}

func (s *searchSpy) Search(ctx context.Context, user docsbot.UserID, q string, limit int) ([]docsbot.Document, error) {
	s.calls++
	s.query = q
	return s.fakeRepo.Search(ctx, user, q, limit)
}

func TestService_Search_normalizesQuery(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantBlank bool
		wantQuery string
	}{
		{"trimmed", "  паспорт \n", false, "паспорт"},
		{"empty", "", true, ""},
		{"only spaces", "  \t\n ", true, ""},
		{"cut to 100 runes", strings.Repeat("я", 10_000), false, strings.Repeat("я", 100)},
		{"emoji counted as runes", strings.Repeat("😀", 150), false, strings.Repeat("😀", 100)},
		{"exactly 100 is kept", strings.Repeat("a", 100), false, strings.Repeat("a", 100)},
		{"metacharacters pass through unchanged", `100%_\`, false, `100%_\`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &searchSpy{fakeRepo: newFakeRepo()}
			svc := docsbot.NewService(spy, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: spy.fakeRepo}, spy, newFakeStatus(spy.fakeRepo))

			found, err := svc.Search(t.Context(), user, tt.query)

			require.NoError(t, err)
			require.Equal(t, tt.wantBlank, found.BlankQuery)
			if tt.wantBlank {
				require.Zero(t, spy.calls, "a blank query must not hit the repository")
				return
			}
			require.Equal(t, tt.wantQuery, spy.query)
			require.Equal(t, tt.wantQuery, found.Query)
		})
	}
}

func TestService_SetTitleCategoryDelete_missingIsNotFound(t *testing.T) {
	repo := newFakeRepo()
	foreign := repo.seed(docsbot.Document{UserID: stranger, Status: docsbot.StatusSaved, Title: "x", Category: docsbot.CatOther})
	svc, _ := edgeService(repo, fakeDrive{})

	_, err := svc.Rename(t.Context(), user, foreign.ID, "new")
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = svc.Recategorize(t.Context(), user, foreign.ID, docsbot.CatPassport)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	require.ErrorIs(t, svc.Delete(t.Context(), user, foreign.ID), docsbot.ErrNotFound)
	require.ErrorIs(t, svc.Delete(t.Context(), user, "nope"), docsbot.ErrNotFound)

	require.Equal(t, "x", repo.docs[foreign.ID].Title, "foreign document untouched")
}

func TestService_MarkBackupFailed_ignoresForeignDocument(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupUploading})
	svc, _ := edgeService(repo, fakeDrive{})

	require.NoError(t, svc.MarkBackupFailed(t.Context(), stranger, doc.ID))
	require.Equal(t, docsbot.BackupUploading, repo.docs[doc.ID].BackupStatus)
}

func TestService_parallelDelete(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
	svc, _ := edgeService(repo, fakeDrive{})

	errs := runParallel(20, func(int) error { return svc.Delete(t.Context(), user, doc.ID) })

	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		require.ErrorIs(t, err, docsbot.ErrNotFound)
	}
	require.Equal(t, 1, ok, "exactly one delete wins, the rest see the document gone")
}

func TestService_parallelDeleteAndRename(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "old"})
	svc, _ := edgeService(repo, fakeDrive{})

	errs := runParallel(20, func(i int) error {
		if i == 10 {
			return svc.Delete(t.Context(), user, doc.ID)
		}
		_, err := svc.Rename(t.Context(), user, doc.ID, "new")
		return err
	})

	for _, err := range errs {
		if err != nil {
			require.ErrorIs(t, err, docsbot.ErrNotFound)
		}
	}
	require.NotContains(t, repo.docs, doc.ID)
}

func TestService_parallelRecategorize(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	svc, _ := edgeService(repo, fakeDrive{})

	errs := runParallel(20, func(i int) error {
		cat := docsbot.CatPassport
		if i%2 == 0 {
			cat = docsbot.CatReceipts
		}
		_, err := svc.Recategorize(t.Context(), user, doc.ID, cat)
		return err
	})

	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Contains(t, []docsbot.Category{docsbot.CatPassport, docsbot.CatReceipts}, repo.docs[doc.ID].Category)
}

func TestService_parallelRename(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "old"})
	svc, _ := edgeService(repo, fakeDrive{})

	errs := runParallel(20, func(int) error {
		_, err := svc.Rename(t.Context(), user, doc.ID, "new")
		return err
	})

	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, "new", repo.docs[doc.ID].Title)
}

func TestService_parallelChooseCategory(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "Скан"})
	svc, uploads := edgeService(repo, fakeDrive{connected: true})

	errs := runParallel(20, func(int) error {
		_, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)
		return err
	})

	for _, err := range errs {
		require.NoError(t, err)
	}
	got := repo.docs[doc.ID]
	require.Equal(t, docsbot.StatusSaved, got.Status)
	require.Equal(t, docsbot.CatPassport, got.Category)
	require.Equal(t, docsbot.BackupPending, got.BackupStatus)
	require.NotEmpty(t, uploads.calls)
}

func TestService_TransferSaved_secondTapHasNothingToTransfer(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupFailed})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupPending})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupUploading})
	svc, uploads := edgeService(repo, fakeDrive{connected: true})

	view, ok, err := svc.TransferSaved(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, view.Lines, 2, "only none and failed are transferred")

	_, ok, err = svc.TransferSaved(t.Context(), user, 5, 51)
	require.NoError(t, err)
	require.False(t, ok, "the second tap must not open a second status message")
	require.Len(t, uploads.requeued, 2)
}

func TestService_parallelTransferSaved_exactlyOnce(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	for range 5 {
		repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	}
	status := newFakeStatus(repo)
	uploads := &fakeUploads{repo: repo}
	svc := docsbot.NewService(repo, fakeDrive{connected: true}, fakeFiles{}, uploads, repo, status)

	var mu sync.Mutex
	started := 0
	errs := runParallel(10, func(i int) error {
		_, ok, err := svc.TransferSaved(t.Context(), user, 5, 50+i)
		mu.Lock()
		defer mu.Unlock()
		if ok {
			started++
		}
		return err
	})

	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, started, "exactly one tap starts the transfer")
	require.Len(t, status.msgs, 1, "and only one status message exists")
	require.Len(t, uploads.requeued, 5, "each document is queued once")
	for _, d := range repo.docs {
		require.Equal(t, docsbot.BackupPending, d.BackupStatus)
	}
}

func TestService_parallelRetryFailed_exactlyOnce(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	for range 3 {
		repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	}
	status := newFakeStatus(repo)
	uploads := &fakeUploads{repo: repo}
	svc := docsbot.NewService(repo, fakeDrive{connected: true}, fakeFiles{}, uploads, repo, status)

	var mu sync.Mutex
	started := 0
	runParallel(10, func(i int) error {
		_, ok, err := svc.RetryFailed(t.Context(), user, 5, 50+i)
		mu.Lock()
		defer mu.Unlock()
		if ok {
			started++
		}
		return err
	})

	require.Equal(t, 1, started)
	require.Len(t, status.msgs, 1)
	require.Len(t, uploads.requeued, 3)
}

func TestService_DeleteAllData_revokeFailureDoesNotBlockDeletion(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
	boom := errors.New("google unreachable")
	svc, uploads := edgeService(repo, fakeDrive{revokeErr: boom})

	err := svc.DeleteAllData(t.Context(), user)

	require.ErrorIs(t, err, docsbot.ErrRevokeIncomplete)
	require.ErrorIs(t, err, boom)
	require.Equal(t, []docsbot.UserID{user}, uploads.cancels)
	require.Equal(t, []docsbot.UserID{user}, repo.deleted, "data must be deleted despite the revoke failure")
	require.Empty(t, repo.docs)
}
