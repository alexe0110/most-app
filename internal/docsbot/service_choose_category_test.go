package docsbot_test

import (
	"cmp"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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

func TestService_defaultTitle_usesLocation(t *testing.T) {
	now := time.Date(2026, 11, 8, 23, 30, 0, 0, time.UTC) // already 9 Nov in Almaty (UTC+5)
	tests := []struct {
		name string
		opts []docsbot.Option
		want string
	}{
		{"default Almaty", nil, "Медицина — 09.11.2026"},
		{"fixed UTC", []docsbot.Option{docsbot.WithLocation(time.UTC)}, "Медицина — 08.11.2026"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "u"})
			opts := append([]docsbot.Option{
				docsbot.WithClock(func() time.Time { return now }),
			}, tt.opts...)
			svc := newService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo}, opts...)

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatMedical)
			require.NoError(t, err)
			require.Equal(t, tt.want, got.Doc.Title)
		})
	}
}

// racingUploads runs beforeSave right before SaveChosen, to play a concurrent
// request (parallel tap, purge job) that wins the race.
type racingUploads struct {
	*fakeUploads
	beforeSave func()
}

func (r racingUploads) SaveChosen(ctx context.Context, c docsbot.SaveChoice) (bool, error) {
	r.beforeSave()
	return r.fakeUploads.SaveChosen(ctx, c)
}

func TestService_ChooseCategory_lostRace(t *testing.T) {
	t.Run("parallel tap saved it first: flagged Already, nothing queued", func(t *testing.T) {
		repo := newFakeRepo()
		doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "a"})
		uploads := &fakeUploads{repo: repo}
		racing := racingUploads{fakeUploads: uploads, beforeSave: func() {
			d := repo.docs[doc.ID]
			d.Status, d.Category = docsbot.StatusSaved, docsbot.CatPassport
			repo.docs[doc.ID] = d
		}}
		svc := newService(repo, fakeDrive{connected: true}, fakeFiles{}, racing, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

		got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatOther)

		require.NoError(t, err)
		require.True(t, got.Already)
		require.Equal(t, docsbot.CatPassport, got.Doc.Category, "the winner's category stays")
		require.Empty(t, uploads.calls)
	})
	t.Run("purged meanwhile: ErrNotFound", func(t *testing.T) {
		repo := newFakeRepo()
		doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "a"})
		racing := racingUploads{fakeUploads: &fakeUploads{repo: repo}, beforeSave: func() { delete(repo.docs, doc.ID) }}
		svc := newService(repo, fakeDrive{}, fakeFiles{}, racing, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

		_, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatOther)

		require.ErrorIs(t, err, docsbot.ErrNotFound)
	})
	t.Run("fresh save is not flagged", func(t *testing.T) {
		repo := newFakeRepo()
		doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "a"})
		svc, _ := edgeService(repo, fakeDrive{})

		got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatOther)

		require.NoError(t, err)
		require.False(t, got.Already)
	})
}

func TestService_ChooseCategory(t *testing.T) {
	tests := []struct {
		name        string
		savedBefore int // extra saved docs already present
		connected   bool
		size        int64
		cat         docsbot.Category
		wantErr     error
		wantOffer   bool
		wantBackup  docsbot.BackupStatus
	}{
		{name: "first document offers Drive", cat: docsbot.CatPassport, wantOffer: true},
		{name: "second document does not offer", savedBefore: 1, cat: docsbot.CatPassport},
		{name: "fifth document offers Drive", savedBefore: 4, cat: docsbot.CatAuto, wantOffer: true},
		{name: "connected queues backup, never offers", connected: true, cat: docsbot.CatPassport, wantBackup: docsbot.BackupPending},
		{name: "connected, file above the Drive limit: too large, no upload", connected: true, size: docsbot.MaxDriveBytes + 1,
			cat: docsbot.CatPassport, wantBackup: docsbot.BackupTooLarge},
		{name: "unknown category rejected", cat: docsbot.Category("bogus"), wantErr: docsbot.ErrUnknownCategory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			for i := 0; i < tt.savedBefore; i++ {
				repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
			}
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "Паспорт", TgFileUniqueID: "u", FileSize: tt.size})
			uploads := &fakeUploads{repo: repo}
			svc := newService(repo, fakeDrive{connected: tt.connected}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, tt.cat)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantOffer, got.OfferDrive)
			require.Equal(t, docsbot.StatusSaved, repo.docs[doc.ID].Status)
			require.Equal(t, tt.cat, repo.docs[doc.ID].Category)
			wantBackup := cmp.Or(tt.wantBackup, docsbot.BackupNone)
			require.Equal(t, wantBackup, repo.docs[doc.ID].BackupStatus)
			require.Equal(t, wantBackup, got.Doc.BackupStatus, "the reply carries the stored state")
			if wantBackup == docsbot.BackupPending {
				require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.calls, "upload enqueued once")
			} else {
				require.Empty(t, uploads.calls)
			}
		})
	}
}

func TestService_ChooseCategory_defaultTitle(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	const base = "Медицина — 08.11.2026"
	taken := func(titles ...string) []docsbot.Document {
		var out []docsbot.Document
		for _, title := range titles {
			out = append(out, docsbot.Document{
				UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatMedical, Title: title,
			})
		}
		return out
	}
	tests := []struct {
		name      string
		title     string
		existing  []docsbot.Document
		wantTitle string
	}{
		{name: "blank title gets default", title: "", wantTitle: base},
		{name: "whitespace title gets default", title: "   ", wantTitle: base},
		{name: "first collision adds (2)", existing: taken(base), wantTitle: base + " (2)"},
		{name: "second collision adds (3)", existing: taken(base, base+" (2)"), wantTitle: base + " (3)"},
		{name: "same title in another category is no collision", existing: []docsbot.Document{{
			UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: base,
		}}, wantTitle: base},
		{name: "existing title is kept", title: "Анализы.pdf", existing: taken(base), wantTitle: "Анализы.pdf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			for _, d := range tt.existing {
				repo.seed(d)
			}
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: tt.title, TgFileUniqueID: "u"})
			svc := newService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo}, docsbot.WithClock(func() time.Time { return now }))

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatMedical)
			require.NoError(t, err)
			require.Equal(t, tt.wantTitle, got.Doc.Title)
			require.Equal(t, tt.wantTitle, repo.docs[doc.ID].Title)
		})
	}
}

func TestService_ChooseCategory_idempotent(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, TgFileUniqueID: "u",
	})
	svc := newService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatReceipts)
	require.NoError(t, err)
	require.False(t, got.OfferDrive)
	require.Equal(t, docsbot.CatPassport, repo.docs[doc.ID].Category, "a second tap must not change the category")
}

// A repeated tap on a saved document changes and queues nothing, whatever its
// backup state: new documents are saved and queued in one transaction, so there
// is no half-saved state for the tap to repair.
func TestService_ChooseCategory_alreadySavedQueuesNothing(t *testing.T) {
	tests := []struct {
		name      string
		connected bool
		status    docsbot.BackupStatus
	}{
		{"no backup, Drive connected", true, docsbot.BackupNone},
		{"Drive not connected", false, docsbot.BackupNone},
		{"already pending", true, docsbot.BackupPending},
		{"already backed up", true, docsbot.BackupBackedUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther,
				BackupStatus: tt.status, Title: "Паспорт", TgFileUniqueID: "u"})
			uploads := &fakeUploads{repo: repo}
			svc := newService(repo, fakeDrive{connected: tt.connected}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)

			require.NoError(t, err)
			require.True(t, got.Already)
			require.Empty(t, uploads.calls)
			require.Equal(t, tt.status, repo.docs[doc.ID].BackupStatus)
			require.Equal(t, docsbot.CatOther, repo.docs[doc.ID].Category)
		})
	}
}

func TestService_ChooseCategory_enqueueFailureLeavesPendingThenRetry(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "Паспорт", TgFileUniqueID: "u"})
	uploads := &fakeUploads{repo: repo, enqueueErr: errors.New("queue down")}
	svc := newService(repo, fakeDrive{connected: true}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	_, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)
	require.Error(t, err, "the upload cannot be queued")
	require.Equal(t, docsbot.StatusPending, repo.docs[doc.ID].Status, "nothing was saved")

	uploads.enqueueErr = nil
	got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)

	require.NoError(t, err)
	require.False(t, got.Already)
	require.Equal(t, docsbot.BackupPending, got.Doc.BackupStatus)
	require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.calls)
}
