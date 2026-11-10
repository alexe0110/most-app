package docsbot_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// flakyMarkRepo fails the first MarkBackedUp calls, like a worker dying after
// the Drive upload but before the result is saved.
type flakyMarkRepo struct {
	*fakeRepo
	failMarks int
}

func (r *flakyMarkRepo) MarkBackedUp(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, driveFileID string) (int, error) {
	if r.failMarks > 0 {
		r.failMarks--
		return 0, errors.New("db down")
	}
	return r.fakeRepo.MarkBackedUp(ctx, user, id, driveFileID)
}

// idempotentDrive emulates Drive appProperties idempotency: a repeated upload
// for the same DocID returns the file created the first time.
type idempotentDrive struct {
	fakeDrive
	files map[string]docsbot.DriveFile
	docs  []string // DocID of every Upload call
}

func (d *idempotentDrive) Upload(_ context.Context, _ docsbot.UserID, up docsbot.DriveUpload) (docsbot.DriveFile, error) {
	d.docs = append(d.docs, up.DocID)
	if f, ok := d.files[up.DocID]; ok {
		return f, nil
	}
	f := docsbot.DriveFile{ID: "drv-" + up.DocID}
	d.files[up.DocID] = f
	return f, nil
}

// Scenario: the upload reached Drive but MarkBackedUp failed; the retry must
// send the same DocID, so Drive returns the same file (no second copy).
func TestService_Backup_retryAfterLostResultReusesDocID(t *testing.T) {
	repo := &flakyMarkRepo{fakeRepo: newFakeRepo(), failMarks: 1}
	doc := repo.seed(docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, Title: "t", TgFileID: "tg",
	})
	drive := &idempotentDrive{files: map[string]docsbot.DriveFile{}}
	svc := docsbot.NewService(repo, drive, fakeFiles{data: []byte("x")}, &fakeUploads{repo: repo.fakeRepo}, repo, newFakeStatus(repo.fakeRepo))

	_, err := svc.Backup(t.Context(), user, doc.ID)
	require.Error(t, err)
	require.Equal(t, docsbot.BackupUploading, repo.docs[doc.ID].BackupStatus)

	res, err := svc.Backup(t.Context(), user, doc.ID)
	require.NoError(t, err)
	require.False(t, res.Noop)
	require.Equal(t, docsbot.BackupBackedUp, repo.docs[doc.ID].BackupStatus)
	require.Equal(t, "drv-"+string(doc.ID), repo.docs[doc.ID].DriveFileID)
	require.Equal(t, []string{string(doc.ID), string(doc.ID)}, drive.docs)
	require.Len(t, drive.files, 1, "one file on Drive for one document")
}

func TestService_Backup_contextCanceledIsRetryable(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, TgFileID: "tg"})
	var uploads int
	svc := docsbot.NewService(repo, fakeDrive{uploads: &uploads, uploadErr: context.Canceled}, fakeFiles{data: []byte("x")},
		&fakeUploads{repo: repo}, repo, newFakeStatus(repo))

	_, err := svc.Backup(t.Context(), user, doc.ID)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, docsbot.BackupUploading, repo.docs[doc.ID].BackupStatus, "left for the retry or the sweeper")
}

func TestService_SweepStuckUploads_thresholds(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		age        time.Duration
		wantFound  int
		wantFailed bool
	}{
		{name: "exactly 30m is not stuck yet", age: 30 * time.Minute},
		{name: "just over 30m is requeued", age: 30*time.Minute + time.Second, wantFound: 1},
		{name: "just under 24h is requeued", age: 24*time.Hour - time.Second, wantFound: 1},
		{name: "exactly 24h is failed", age: 24 * time.Hour, wantFound: 1, wantFailed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupPending})
			repo.updated = map[docsbot.DocumentID]time.Time{doc.ID: now.Add(-tt.age)}
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo),
				docsbot.WithClock(func() time.Time { return now }))

			res, err := svc.SweepStuckUploads(t.Context())
			require.NoError(t, err)
			require.Equal(t, tt.wantFound, res.Found)
			require.Equal(t, tt.wantFailed, len(res.Failed) == 1)
		})
	}
}

// failingRequeue fails every RequeueUpload.
type failingRequeue struct{ *fakeUploads }

func (failingRequeue) RequeueUpload(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	return errors.New("queue down")
}

func TestService_SweepStuckUploads_requeueErrorIsReturned(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupUploading})
	repo.updated = map[docsbot.DocumentID]time.Time{doc.ID: now.Add(-time.Hour)}
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, failingRequeue{&fakeUploads{repo: repo}}, repo, newFakeStatus(repo),
		docsbot.WithClock(func() time.Time { return now }))

	res, err := svc.SweepStuckUploads(t.Context())
	require.Error(t, err)
	require.Equal(t, 1, res.Found)
	require.Zero(t, res.Requeued)
	require.Equal(t, docsbot.BackupUploading, repo.docs[doc.ID].BackupStatus, "document untouched, next run retries")
}
