package docsbot_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestService_ChooseCategory(t *testing.T) {
	tests := []struct {
		name          string
		savedBefore   int // extra saved docs already present
		connected     bool
		cat           docsbot.Category
		wantErr       error
		wantOffer     bool
		wantBackupPed bool
	}{
		{name: "first document offers Drive", cat: docsbot.CatPassport, wantOffer: true},
		{name: "second document does not offer", savedBefore: 1, cat: docsbot.CatPassport},
		{name: "fifth document offers Drive", savedBefore: 4, cat: docsbot.CatAuto, wantOffer: true},
		{name: "connected queues backup, never offers", connected: true, cat: docsbot.CatPassport, wantBackupPed: true},
		{name: "unknown category rejected", cat: docsbot.Category("bogus"), wantErr: docsbot.ErrUnknownCategory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			for i := 0; i < tt.savedBefore; i++ {
				repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
			}
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "Паспорт", TgFileUniqueID: "u"})
			uploads := &fakeUploads{repo: repo}
			svc := docsbot.NewService(repo, fakeDrive{connected: tt.connected}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, tt.cat)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantOffer, got.OfferDrive)
			require.Equal(t, docsbot.StatusSaved, repo.docs[doc.ID].Status)
			require.Equal(t, tt.cat, repo.docs[doc.ID].Category)
			if tt.wantBackupPed {
				require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.calls, "upload enqueued once")
				require.Equal(t, docsbot.BackupPending, repo.docs[doc.ID].BackupStatus)
			} else {
				require.Empty(t, uploads.calls)
				require.Equal(t, docsbot.BackupNone, repo.docs[doc.ID].BackupStatus)
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
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo}, docsbot.WithClock(func() time.Time { return now }))

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
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatReceipts)
	require.NoError(t, err)
	require.False(t, got.OfferDrive)
	require.Equal(t, docsbot.CatPassport, repo.docs[doc.ID].Category, "a second tap must not change the category")
}

// A failed upload enqueue leaves the document saved with no backup. The next tap
// on a category must queue the upload instead of reporting "already saved" only.
func TestService_ChooseCategory_retryQueuesUpload(t *testing.T) {
	tests := []struct {
		name       string
		connected  bool
		status     docsbot.BackupStatus
		wantQueued bool
	}{
		{"saved, no backup, Drive connected", true, docsbot.BackupNone, true},
		{"Drive not connected", false, docsbot.BackupNone, false},
		{"already pending", true, docsbot.BackupPending, false},
		{"already backed up", true, docsbot.BackupBackedUp, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther,
				BackupStatus: tt.status, Title: "Паспорт", TgFileUniqueID: "u"})
			uploads := &fakeUploads{repo: repo}
			svc := docsbot.NewService(repo, fakeDrive{connected: tt.connected}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)

			require.NoError(t, err)
			require.True(t, got.Already)
			if tt.wantQueued {
				require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.calls)
				require.Equal(t, docsbot.BackupPending, got.Doc.BackupStatus)
			} else {
				require.Empty(t, uploads.calls)
			}
		})
	}
}

func TestService_ChooseCategory_enqueueFailureThenRetry(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "Паспорт", TgFileUniqueID: "u"})
	uploads := &fakeUploads{repo: repo, enqueueErr: errors.New("queue down")}
	svc := docsbot.NewService(repo, fakeDrive{connected: true}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	_, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)
	require.Error(t, err, "first tap saves the document but cannot queue the upload")

	uploads.enqueueErr = nil
	got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatPassport)

	require.NoError(t, err)
	require.Equal(t, docsbot.BackupPending, got.Doc.BackupStatus)
	require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.calls)
}
