package docsbot_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func intakeFile(uid string) docsbot.IncomingFile {
	return docsbot.IncomingFile{UserID: user, Kind: docsbot.KindPhoto, FileID: "f-" + uid, FileUniqueID: uid}
}

// seedMany stores n documents of the user with the given status and age.
func seedMany(repo *fakeRepo, n int, status docsbot.Status, createdAt time.Time) {
	for i := range n {
		repo.seed(docsbot.Document{
			UserID: user, Status: status, TgFileUniqueID: fmt.Sprintf("seed-%s-%d-%d", status, createdAt.Unix(), i),
			CreatedAt: createdAt,
		})
	}
}

func TestService_Receive_albumPhotosBecomeSeparateDocuments(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := edgeService(repo, fakeDrive{})

	for _, uid := range []string{"p1", "p2", "p3"} {
		in := intakeFile(uid)
		in.MediaGroupID = "album-1"
		rep, err := svc.Receive(t.Context(), in)
		require.NoError(t, err)
		require.IsType(t, docsbot.NeedCategory{}, rep)
	}

	pending, err := repo.PendingByMediaGroup(t.Context(), user, "album-1")
	require.NoError(t, err)
	require.Len(t, pending, 3)
}

func TestService_Receive_albumWithDuplicate(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, TgFileUniqueID: "p2", Category: docsbot.CatOther})
	svc, _ := edgeService(repo, fakeDrive{})

	var kinds []string
	for _, uid := range []string{"p1", "p2", "p3"} {
		in := intakeFile(uid)
		in.MediaGroupID = "album-1"
		rep, err := svc.Receive(t.Context(), in)
		require.NoError(t, err)
		kinds = append(kinds, fmt.Sprintf("%T", rep))
	}

	require.Equal(t, []string{"docsbot.NeedCategory", "docsbot.AlreadySaved", "docsbot.NeedCategory"}, kinds)
	pending, err := repo.PendingByMediaGroup(t.Context(), user, "album-1")
	require.NoError(t, err)
	require.Len(t, pending, 2, "the duplicate must not create a document")
}

func TestService_ChooseCategoryGroup_savesWholeAlbum(t *testing.T) {
	tests := []struct {
		name      string
		connected bool
		wantQueue int
	}{
		{"drive off", false, 0},
		{"drive on", true, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			svc, uploads := edgeService(repo, fakeDrive{connected: tt.connected})
			var first docsbot.DocumentID
			for _, uid := range []string{"p1", "p2", "p3"} {
				in := intakeFile(uid)
				in.MediaGroupID = "album-1"
				rep, err := svc.Receive(t.Context(), in)
				require.NoError(t, err)
				if first == "" {
					first = rep.(docsbot.NeedCategory).Doc.ID
				}
			}
			// A pending document of another album and of another user stay untouched.
			other := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, MediaGroupID: "album-2", TgFileUniqueID: "x"})
			foreign := repo.seed(docsbot.Document{UserID: stranger, Status: docsbot.StatusPending, MediaGroupID: "album-1", TgFileUniqueID: "y"})

			got, err := svc.ChooseCategoryGroup(t.Context(), user, first, docsbot.CatReceipts)
			require.NoError(t, err)

			require.False(t, got.Already)
			require.Len(t, got.Docs, 3)
			for _, d := range got.Docs {
				require.Equal(t, docsbot.StatusSaved, repo.docs[d.ID].Status)
				require.Equal(t, docsbot.CatReceipts, repo.docs[d.ID].Category)
			}
			require.Len(t, uploads.calls, tt.wantQueue)
			require.Equal(t, docsbot.StatusPending, repo.docs[other.ID].Status)
			require.Equal(t, docsbot.StatusPending, repo.docs[foreign.ID].Status)

			// A second tap is a no-op, flagged so the handler does not redraw.
			again, err := svc.ChooseCategoryGroup(t.Context(), user, first, docsbot.CatReceipts)
			require.NoError(t, err)
			require.True(t, again.Already)
			require.Empty(t, again.Docs)
			require.Len(t, uploads.calls, tt.wantQueue)
		})
	}
}

func TestService_ChooseCategoryGroup_rejectsBadInput(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "a"})
	svc, _ := edgeService(repo, fakeDrive{})

	_, err := svc.ChooseCategoryGroup(t.Context(), user, doc.ID, "nope")
	require.ErrorIs(t, err, docsbot.ErrUnknownCategory)
	_, err = svc.ChooseCategoryGroup(t.Context(), stranger, doc.ID, docsbot.CatOther)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
}

func TestService_ChooseCategory_tooLargeStaysInTelegram(t *testing.T) {
	tests := []struct {
		name       string
		size       int64
		wantStatus docsbot.BackupStatus
		wantQueued int
	}{
		{"above limit", docsbot.MaxDriveBytes + 1, docsbot.BackupTooLarge, 0},
		{"at limit", docsbot.MaxDriveBytes, docsbot.BackupPending, 1},
		{"unknown size goes to the worker", 0, docsbot.BackupPending, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, FileSize: tt.size, TgFileUniqueID: "a"})
			svc, uploads := edgeService(repo, fakeDrive{connected: true})

			saved, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatOther)

			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, saved.Doc.BackupStatus)
			require.Equal(t, tt.wantStatus, repo.docs[doc.ID].BackupStatus)
			require.Len(t, uploads.calls, tt.wantQueued)
		})
	}
}

func TestService_ChooseCategory_bigFileWithoutDriveIsPlainSaved(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, FileSize: docsbot.MaxDriveBytes * 2, TgFileUniqueID: "a"})
	svc, _ := edgeService(repo, fakeDrive{})

	saved, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatOther)

	require.NoError(t, err)
	require.Equal(t, docsbot.BackupNone, saved.Doc.BackupStatus)
}

func TestService_ChooseCategoryGroup_tooLargeInAlbum(t *testing.T) {
	repo := newFakeRepo()
	svc, uploads := edgeService(repo, fakeDrive{connected: true})
	small := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, MediaGroupID: "g", FileSize: 10, TgFileUniqueID: "a"})
	big := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, MediaGroupID: "g", FileSize: docsbot.MaxDriveBytes + 1, TgFileUniqueID: "b"})

	_, err := svc.ChooseCategoryGroup(t.Context(), user, small.ID, docsbot.CatOther)

	require.NoError(t, err)
	require.Equal(t, docsbot.BackupTooLarge, repo.docs[big.ID].BackupStatus)
	require.Equal(t, []docsbot.DocumentID{small.ID}, uploads.calls)
}

func TestService_NotBackedUpCount_excludesTooLarge(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupNone, TgFileUniqueID: "a"})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupTooLarge, TgFileUniqueID: "b"})
	svc, _ := edgeService(repo, fakeDrive{connected: true})

	n, err := svc.NotBackedUpCount(t.Context(), user)

	require.NoError(t, err)
	require.Equal(t, 1, n, "a too_large document is not offered for transfer")
}

func TestService_Receive_duplicateAndDeleteSemantics(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := edgeService(repo, fakeDrive{})
	ctx := t.Context()

	first, err := svc.Receive(ctx, intakeFile("dup"))
	require.NoError(t, err)
	pending := first.(docsbot.NeedCategory).Doc

	again, err := svc.Receive(ctx, intakeFile("dup"))
	require.NoError(t, err)
	require.Equal(t, docsbot.NeedCategory{Doc: pending}, again, "pending file re-asks the category")

	_, err = svc.ChooseCategory(ctx, user, pending.ID, docsbot.CatOther)
	require.NoError(t, err)
	saved, err := svc.Receive(ctx, intakeFile("dup"))
	require.NoError(t, err)
	require.IsType(t, docsbot.AlreadySaved{}, saved)

	_, err = svc.Delete(ctx, user, pending.ID)
	require.NoError(t, err)
	fresh, err := svc.Receive(ctx, intakeFile("dup"))
	require.NoError(t, err)
	need, ok := fresh.(docsbot.NeedCategory)
	require.True(t, ok, "deletion is hard: the same file can be saved again")
	require.NotEqual(t, pending.ID, need.Doc.ID)
}

func TestService_Receive_multiPagePDFIsOneDocument(t *testing.T) {
	// Not reproducible: Telegram delivers a multi-page PDF as one document
	// message, and Receive stores exactly one document per message.
	repo := newFakeRepo()
	svc, _ := edgeService(repo, fakeDrive{})

	rep, err := svc.Receive(t.Context(), docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, FileID: "pdf", FileUniqueID: "pdf-u",
		Title: "scan.pdf", FileSize: 5 << 20, MIME: "application/pdf",
	})

	require.NoError(t, err)
	require.IsType(t, docsbot.NeedCategory{}, rep)
	require.Len(t, repo.docs, 1)
}

func TestService_Receive_limits(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	tests := []struct {
		name  string
		setup func(*fakeRepo)
		want  docsbot.Reply
	}{
		{"under all limits", func(r *fakeRepo) { seedMany(r, 19, docsbot.StatusPending, old) }, docsbot.NeedCategory{}},
		{"20 pending", func(r *fakeRepo) { seedMany(r, 20, docsbot.StatusPending, old) }, docsbot.TooManyPending{}},
		{"29 in the last minute", func(r *fakeRepo) { seedMany(r, 29, docsbot.StatusSaved, now.Add(-10*time.Second)) }, docsbot.NeedCategory{}},
		{"30 in the last minute", func(r *fakeRepo) { seedMany(r, 30, docsbot.StatusSaved, now.Add(-10*time.Second)) }, docsbot.RateLimited{}},
		{"30 but older than a minute", func(r *fakeRepo) { seedMany(r, 30, docsbot.StatusSaved, now.Add(-2*time.Minute)) }, docsbot.NeedCategory{}},
		{"4999 total", func(r *fakeRepo) { seedMany(r, 4999, docsbot.StatusSaved, old) }, docsbot.NeedCategory{}},
		{"5000 total", func(r *fakeRepo) { seedMany(r, 5000, docsbot.StatusSaved, old) }, docsbot.QuotaExceeded{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			tt.setup(repo)
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo),
				docsbot.WithClock(func() time.Time { return now }))
			before := len(repo.docs)

			rep, err := svc.Receive(t.Context(), intakeFile("new"))

			require.NoError(t, err)
			require.IsType(t, tt.want, rep)
			if _, ok := rep.(docsbot.NeedCategory); !ok {
				require.Len(t, repo.docs, before, "a rejected file must not be stored")
			}
		})
	}
}

func TestService_Receive_duplicateBypassesLimits(t *testing.T) {
	repo := newFakeRepo()
	seedMany(repo, 20, docsbot.StatusPending, time.Now().Add(-time.Hour))
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, TgFileUniqueID: "known"})
	svc, _ := edgeService(repo, fakeDrive{})

	rep, err := svc.Receive(t.Context(), intakeFile("known"))

	require.NoError(t, err)
	require.IsType(t, docsbot.AlreadySaved{}, rep)
}

func TestService_PurgeStalePending(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	repo := newFakeRepo()
	stale := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "a", CreatedAt: now.Add(-25 * time.Hour)})
	fresh := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "b", CreatedAt: now.Add(-23 * time.Hour)})
	oldSaved := repo.seed(docsbot.Document{UserID: stranger, Status: docsbot.StatusSaved, TgFileUniqueID: "c", CreatedAt: now.Add(-48 * time.Hour)})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo),
		docsbot.WithClock(func() time.Time { return now }))

	n, err := svc.PurgeStalePending(t.Context())

	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NotContains(t, repo.docs, stale.ID)
	require.Contains(t, repo.docs, fresh.ID)
	require.Contains(t, repo.docs, oldSaved.ID, "saved documents are never purged")
}
