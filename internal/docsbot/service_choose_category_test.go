package docsbot_test

import (
	"cmp"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
)

func TestService_parallelChooseCategory(t *testing.T) {
	t.Parallel()
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "Скан"})
	svc, uploads := edgeService(repo, docsbottest.FakeDrive{IsConnected: true})

	errs := runParallel(20, func(int) error {
		_, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)
		return err
	})

	for _, err := range errs {
		require.NoError(t, err)
	}
	got := repo.Docs[doc.ID]
	require.Equal(t, docsbot.StatusSaved, got.Status)
	require.Equal(t, docsbot.CatPassport, got.Category)
	require.Equal(t, docsbot.BackupPending, got.BackupStatus)
	require.NotEmpty(t, uploads.Calls)
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
			repo := docsbottest.NewFakeRepo()
			doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "u"})
			opts := append([]docsbot.Option{
				docsbot.WithClock(func() time.Time { return now }),
			}, tt.opts...)
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo}, opts...)

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatMedical)
			require.NoError(t, err)
			require.Equal(t, tt.want, got.Doc.Title)
		})
	}
}

// racingUploads runs beforeSave right before SaveChosen, to play a concurrent
// request (parallel tap, purge job) that wins the race.
type racingUploads struct {
	*docsbottest.FakeUploads
	beforeSave func()
}

func (r racingUploads) SaveChosen(ctx context.Context, c docsbot.SaveChoice) (bool, error) {
	r.beforeSave()
	return r.FakeUploads.SaveChosen(ctx, c)
}

func TestService_ChooseCategory_lostRace(t *testing.T) {
	t.Run("parallel tap saved it first: flagged Already, nothing queued", func(t *testing.T) {
		repo := docsbottest.NewFakeRepo()
		doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "a"})
		uploads := &docsbottest.FakeUploads{Repo: repo}
		racing := racingUploads{FakeUploads: uploads, beforeSave: func() {
			d := repo.Docs[doc.ID]
			d.Status, d.Category = docsbot.StatusSaved, docsbot.CatPassport
			repo.Docs[doc.ID] = d
		}}
		svc := docsbottest.NewService(repo, docsbottest.FakeDrive{IsConnected: true}, docsbottest.FakeFiles{}, racing, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

		got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatOther)

		require.NoError(t, err)
		require.True(t, got.Already)
		require.Equal(t, docsbot.CatPassport, got.Doc.Category, "the winner's category stays")
		require.Empty(t, uploads.Calls)
	})
	t.Run("purged meanwhile: ErrNotFound", func(t *testing.T) {
		repo := docsbottest.NewFakeRepo()
		doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "a"})
		racing := racingUploads{FakeUploads: &docsbottest.FakeUploads{Repo: repo}, beforeSave: func() { delete(repo.Docs, doc.ID) }}
		svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, racing, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

		_, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatOther)

		require.ErrorIs(t, err, docsbot.ErrNotFound)
	})
	t.Run("fresh save is not flagged", func(t *testing.T) {
		repo := docsbottest.NewFakeRepo()
		doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "a"})
		svc, _ := edgeService(repo, docsbottest.FakeDrive{})

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
			repo := docsbottest.NewFakeRepo()
			for i := 0; i < tt.savedBefore; i++ {
				repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
			}
			doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "Паспорт", TgFileUniqueID: "u", FileSize: tt.size})
			uploads := &docsbottest.FakeUploads{Repo: repo}
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{IsConnected: tt.connected}, docsbottest.FakeFiles{}, uploads, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, tt.cat)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantOffer, got.OfferDrive)
			require.Equal(t, docsbot.StatusSaved, repo.Docs[doc.ID].Status)
			require.Equal(t, tt.cat, repo.Docs[doc.ID].Category)
			wantBackup := cmp.Or(tt.wantBackup, docsbot.BackupNone)
			require.Equal(t, wantBackup, repo.Docs[doc.ID].BackupStatus)
			require.Equal(t, wantBackup, got.Doc.BackupStatus, "the reply carries the stored state")
			if wantBackup == docsbot.BackupPending {
				require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.Calls, "upload enqueued once")
			} else {
				require.Empty(t, uploads.Calls)
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
			repo := docsbottest.NewFakeRepo()
			for _, d := range tt.existing {
				repo.Seed(d)
			}
			doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: tt.title, TgFileUniqueID: "u"})
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo}, docsbot.WithClock(func() time.Time { return now }))

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatMedical)
			require.NoError(t, err)
			require.Equal(t, tt.wantTitle, got.Doc.Title)
			require.Equal(t, tt.wantTitle, repo.Docs[doc.ID].Title)
		})
	}
}

func TestService_ChooseCategory_idempotent(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, TgFileUniqueID: "u",
	})
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatReceipts)
	require.NoError(t, err)
	require.False(t, got.OfferDrive)
	require.Equal(t, docsbot.CatPassport, repo.Docs[doc.ID].Category, "a second tap must not change the category")
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
			repo := docsbottest.NewFakeRepo()
			doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther,
				BackupStatus: tt.status, Title: "Паспорт", TgFileUniqueID: "u"})
			uploads := &docsbottest.FakeUploads{Repo: repo}
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{IsConnected: tt.connected}, docsbottest.FakeFiles{}, uploads, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)

			require.NoError(t, err)
			require.True(t, got.Already)
			require.Empty(t, uploads.Calls)
			require.Equal(t, tt.status, repo.Docs[doc.ID].BackupStatus)
			require.Equal(t, docsbot.CatOther, repo.Docs[doc.ID].Category)
		})
	}
}

func TestService_ChooseCategory_enqueueFailureLeavesPendingThenRetry(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "Паспорт", TgFileUniqueID: "u"})
	uploads := &docsbottest.FakeUploads{Repo: repo, EnqueueErr: errors.New("queue down")}
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{IsConnected: true}, docsbottest.FakeFiles{}, uploads, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	_, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)
	require.Error(t, err, "the upload cannot be queued")
	require.Equal(t, docsbot.StatusPending, repo.Docs[doc.ID].Status, "nothing was saved")

	uploads.EnqueueErr = nil
	got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)

	require.NoError(t, err)
	require.False(t, got.Already)
	require.Equal(t, docsbot.BackupPending, got.Doc.BackupStatus)
	require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.Calls)
}

func TestService_ChooseCategory_firstFlag(t *testing.T) {
	tests := []struct {
		name      string
		connected bool
		saved     int // documents the user saved before
		want      bool
	}{
		{"first document, drive off", false, 0, true},
		{"first document, drive on", true, 0, true},
		{"second document", false, 1, false},
		{"second document, drive on", true, 1, false},
		{"fifth document", false, 4, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			for i := range tt.saved {
				repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, TgFileUniqueID: "old" + string(rune('a'+i))})
			}
			doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "new"})
			svc, _ := edgeService(repo, docsbottest.FakeDrive{IsConnected: tt.connected})

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)
			require.NoError(t, err)
			require.Equal(t, tt.want, got.First)

			// A repeated tap saves nothing, so it must not announce a first document again.
			again, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)
			require.NoError(t, err)
			require.True(t, again.Already)
			require.False(t, again.First)
		})
	}
}

func TestService_ChooseCategory_firstFlagOtherUsersDoNotCount(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	repo.Seed(docsbot.Document{UserID: stranger, Status: docsbot.StatusSaved, TgFileUniqueID: "theirs"})
	doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "mine"})
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})

	got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)

	require.NoError(t, err)
	require.True(t, got.First)
}

func TestService_ChooseCategoryGroup_firstFlag(t *testing.T) {
	tests := []struct {
		name      string
		connected bool
		saved     int
		want      bool
	}{
		{"album is the first documents, drive off", false, 0, true},
		{"album is the first documents, drive on", true, 0, true},
		{"album after an existing document", false, 1, false},
		{"album after an existing document, drive on", true, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			for i := range tt.saved {
				repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, TgFileUniqueID: "old" + string(rune('a'+i))})
			}
			svc, _ := edgeService(repo, docsbottest.FakeDrive{IsConnected: tt.connected})
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

			got, err := svc.ChooseCategoryGroup(t.Context(), user, first, docsbot.CatReceipts)
			require.NoError(t, err)
			require.Len(t, got.Docs, 3)
			require.Equal(t, tt.want, got.First)

			again, err := svc.ChooseCategoryGroup(t.Context(), user, first, docsbot.CatReceipts)
			require.NoError(t, err)
			require.True(t, again.Already)
			require.False(t, again.First, "a repeated tap must not announce a first document")
		})
	}
}
