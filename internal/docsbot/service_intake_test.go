package docsbot_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
)

func TestService_Receive_normalizesTitle(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	in := incoming("norm")
	in.Title = " a\nb\t"
	reply, err := svc.Receive(t.Context(), in)
	require.NoError(t, err)
	require.Equal(t, "a b", reply.(docsbot.NeedCategory).Doc.Title)

	in = incoming("blank")
	in.Title = "\n \n"
	reply, err = svc.Receive(t.Context(), in)
	require.NoError(t, err)
	require.Empty(t, reply.(docsbot.NeedCategory).Doc.Title, "blank stays empty: the default title is set on save")
}

func TestService_ChooseCategoryGroup_parallelTapsSaveOnce(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	var first docsbot.DocumentID
	for _, uid := range []string{"p1", "p2", "p3"} {
		d := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, MediaGroupID: "alb", TgFileUniqueID: uid})
		first = d.ID
	}
	svc, uploads := edgeService(repo, docsbottest.FakeDrive{IsConnected: true})

	var mu sync.Mutex
	saved := 0
	runParallel(8, func(int) error {
		got, err := svc.ChooseCategoryGroup(t.Context(), user, first, docsbot.CatReceipts)
		mu.Lock()
		defer mu.Unlock()
		saved += len(got.Docs)
		return err
	})

	require.Equal(t, 3, saved, "every document is saved by exactly one tap")
	require.Len(t, uploads.Calls, 3, "and queued once")
}

func TestService_Receive(t *testing.T) {
	saved := docsbot.Document{
		ID: "d-saved", UserID: user, Status: docsbot.StatusSaved,
		Category: docsbot.CatPassport, Title: "Паспорт", TgFileUniqueID: "dup-saved",
	}
	pending := docsbot.Document{
		ID: "d-pending", UserID: user, Status: docsbot.StatusPending,
		Title: "Паспорт", TgFileUniqueID: "dup-pending",
	}

	tests := []struct {
		name   string
		seed   *docsbot.Document
		unique string
		assert func(t *testing.T, rep docsbot.Reply, repo *docsbottest.FakeRepo)
	}{
		{
			name:   "new file is created and needs a category",
			unique: "fresh",
			assert: func(t *testing.T, rep docsbot.Reply, repo *docsbottest.FakeRepo) {
				nc, ok := rep.(docsbot.NeedCategory)
				require.True(t, ok)
				require.Equal(t, docsbot.StatusPending, nc.Doc.Status)
				require.Len(t, repo.Docs, 1)
			},
		},
		{
			name:   "pending duplicate re-asks the category",
			seed:   &pending,
			unique: "dup-pending",
			assert: func(t *testing.T, rep docsbot.Reply, repo *docsbottest.FakeRepo) {
				nc, ok := rep.(docsbot.NeedCategory)
				require.True(t, ok)
				require.Equal(t, docsbot.DocumentID("d-pending"), nc.Doc.ID)
				require.Len(t, repo.Docs, 1, "no new row")
			},
		},
		{
			name:   "saved duplicate reports already saved",
			seed:   &saved,
			unique: "dup-saved",
			assert: func(t *testing.T, rep docsbot.Reply, _ *docsbottest.FakeRepo) {
				as, ok := rep.(docsbot.AlreadySaved)
				require.True(t, ok)
				require.Equal(t, docsbot.DocumentID("d-saved"), as.Doc.ID)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			if tt.seed != nil {
				repo.Seed(*tt.seed)
			}
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})
			rep, err := svc.Receive(t.Context(), incoming(tt.unique))
			require.NoError(t, err)
			tt.assert(t, rep, repo)
		})
	}
}

func intakeFile(uid string) docsbot.IncomingFile {
	return docsbot.IncomingFile{UserID: user, Kind: docsbot.KindPhoto, FileID: "f-" + uid, FileUniqueID: uid}
}

// seedMany stores n documents of the user with the given status and age.
func seedMany(repo *docsbottest.FakeRepo, n int, status docsbot.Status, createdAt time.Time) {
	for i := range n {
		repo.Seed(docsbot.Document{
			UserID: user, Status: status, TgFileUniqueID: fmt.Sprintf("seed-%s-%d-%d", status, createdAt.Unix(), i),
			CreatedAt: createdAt,
		})
	}
}

func TestService_Receive_albumPhotosBecomeSeparateDocuments(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})

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
	repo := docsbottest.NewFakeRepo()
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, TgFileUniqueID: "p2", Category: docsbot.CatOther})
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})

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
			repo := docsbottest.NewFakeRepo()
			svc, uploads := edgeService(repo, docsbottest.FakeDrive{IsConnected: tt.connected})
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
			other := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, MediaGroupID: "album-2", TgFileUniqueID: "x"})
			foreign := repo.Seed(docsbot.Document{UserID: stranger, Status: docsbot.StatusPending, MediaGroupID: "album-1", TgFileUniqueID: "y"})

			got, err := svc.ChooseCategoryGroup(t.Context(), user, first, docsbot.CatReceipts)
			require.NoError(t, err)

			require.False(t, got.Already)
			require.Len(t, got.Docs, 3)
			for _, d := range got.Docs {
				require.Equal(t, docsbot.StatusSaved, repo.Docs[d.ID].Status)
				require.Equal(t, docsbot.CatReceipts, repo.Docs[d.ID].Category)
			}
			require.Len(t, uploads.Calls, tt.wantQueue)
			require.Equal(t, docsbot.StatusPending, repo.Docs[other.ID].Status)
			require.Equal(t, docsbot.StatusPending, repo.Docs[foreign.ID].Status)

			// A second tap is a no-op, flagged so the handler does not redraw.
			again, err := svc.ChooseCategoryGroup(t.Context(), user, first, docsbot.CatReceipts)
			require.NoError(t, err)
			require.True(t, again.Already)
			require.Empty(t, again.Docs)
			require.Len(t, uploads.Calls, tt.wantQueue)
		})
	}
}

func TestService_ChooseCategoryGroup_rejectsBadInput(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "a"})
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})

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
			repo := docsbottest.NewFakeRepo()
			doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, FileSize: tt.size, TgFileUniqueID: "a"})
			svc, uploads := edgeService(repo, docsbottest.FakeDrive{IsConnected: true})

			saved, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatOther)

			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, saved.Doc.BackupStatus)
			require.Equal(t, tt.wantStatus, repo.Docs[doc.ID].BackupStatus)
			require.Len(t, uploads.Calls, tt.wantQueued)
		})
	}
}

func TestService_ChooseCategory_bigFileWithoutDriveIsPlainSaved(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, FileSize: docsbot.MaxDriveBytes * 2, TgFileUniqueID: "a"})
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})

	saved, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatOther)

	require.NoError(t, err)
	require.Equal(t, docsbot.BackupNone, saved.Doc.BackupStatus)
}

func TestService_ChooseCategoryGroup_tooLargeInAlbum(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	svc, uploads := edgeService(repo, docsbottest.FakeDrive{IsConnected: true})
	small := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, MediaGroupID: "g", FileSize: 10, TgFileUniqueID: "a"})
	big := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, MediaGroupID: "g", FileSize: docsbot.MaxDriveBytes + 1, TgFileUniqueID: "b"})

	_, err := svc.ChooseCategoryGroup(t.Context(), user, small.ID, docsbot.CatOther)

	require.NoError(t, err)
	require.Equal(t, docsbot.BackupTooLarge, repo.Docs[big.ID].BackupStatus)
	require.Equal(t, []docsbot.DocumentID{small.ID}, uploads.Calls)
}

func TestService_NotBackedUpCount_excludesTooLarge(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupNone, TgFileUniqueID: "a"})
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupTooLarge, TgFileUniqueID: "b"})
	svc, _ := edgeService(repo, docsbottest.FakeDrive{IsConnected: true})

	n, err := svc.NotBackedUpCount(t.Context(), user)

	require.NoError(t, err)
	require.Equal(t, 1, n, "a too_large document is not offered for transfer")
}

func TestService_Receive_duplicateAndDeleteSemantics(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})
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

// racingCreate plays a parallel send of the same file: its document lands just
// before our insert, which then hits the unique constraint.
type racingCreate struct {
	*docsbottest.FakeRepo
	winner     docsbot.Document
	rereadErr  error // fails the lookup that follows the lost insert
	lookupSeen *int  // counts DocumentByFileUniqueID calls when rereadErr is set
}

func (r racingCreate) DocumentByFileUniqueID(ctx context.Context, u docsbot.UserID, uid string) (docsbot.Document, error) {
	if r.rereadErr != nil {
		*r.lookupSeen++
		if *r.lookupSeen > 1 {
			return docsbot.Document{}, r.rereadErr
		}
	}
	return r.FakeRepo.DocumentByFileUniqueID(ctx, u, uid)
}

func (r racingCreate) CreateDocument(context.Context, docsbot.IncomingFile) (docsbot.Document, error) {
	r.Seed(r.winner)
	return docsbot.Document{}, docsbot.ErrDuplicateFile
}

func TestService_Receive_lostCreateRaceAnswersByTheWinner(t *testing.T) {
	tests := []struct {
		name      string
		status    docsbot.Status
		want      func(docsbot.Document) docsbot.Reply
		rereadErr error
	}{
		{"winner still pending", docsbot.StatusPending, func(d docsbot.Document) docsbot.Reply { return docsbot.NeedCategory{Doc: d} }, nil},
		{"winner already saved", docsbot.StatusSaved, func(d docsbot.Document) docsbot.Reply { return docsbot.AlreadySaved{Doc: d} }, nil},
		{"winner cannot be reread", docsbot.StatusPending, nil, errors.New("db down")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			in := intakeFile("race")
			winner := docsbot.Document{ID: "winner", UserID: user, Status: tt.status, TgFileUniqueID: in.FileUniqueID, CreatedAt: time.Unix(1, 0)}
			svc := docsbottest.NewService(racingCreate{FakeRepo: repo, winner: winner, rereadErr: tt.rereadErr, lookupSeen: new(int)}, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo,
				docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

			rep, err := svc.Receive(t.Context(), in)

			if tt.rereadErr != nil {
				require.ErrorIs(t, err, tt.rereadErr)
				require.Nil(t, rep)
				return
			}
			require.NoError(t, err)
			stored, err := repo.DocumentByID(t.Context(), user, "winner")
			require.NoError(t, err)
			require.Equal(t, tt.want(stored), rep)
			require.Len(t, repo.Docs, 1)
		})
	}
}

func TestService_Receive_multiPagePDFIsOneDocument(t *testing.T) {
	// Not reproducible: Telegram delivers a multi-page PDF as one document
	// message, and Receive stores exactly one document per message.
	repo := docsbottest.NewFakeRepo()
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})

	rep, err := svc.Receive(t.Context(), docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, FileID: "pdf", FileUniqueID: "pdf-u",
		Title: "scan.pdf", FileSize: 5 << 20, MIME: "application/pdf",
	})

	require.NoError(t, err)
	require.IsType(t, docsbot.NeedCategory{}, rep)
	require.Len(t, repo.Docs, 1)
}

func TestService_Receive_limits(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	tests := []struct {
		name  string
		setup func(*docsbottest.FakeRepo)
		want  docsbot.Reply
	}{
		{"under all limits", func(r *docsbottest.FakeRepo) { seedMany(r, 19, docsbot.StatusPending, old) }, docsbot.NeedCategory{}},
		{"20 pending", func(r *docsbottest.FakeRepo) { seedMany(r, 20, docsbot.StatusPending, old) }, docsbot.TooManyPending{}},
		{"29 in the last minute", func(r *docsbottest.FakeRepo) { seedMany(r, 29, docsbot.StatusSaved, now.Add(-10*time.Second)) }, docsbot.NeedCategory{}},
		{"30 in the last minute", func(r *docsbottest.FakeRepo) { seedMany(r, 30, docsbot.StatusSaved, now.Add(-10*time.Second)) }, docsbot.RateLimited{}},
		{"30 but older than a minute", func(r *docsbottest.FakeRepo) { seedMany(r, 30, docsbot.StatusSaved, now.Add(-2*time.Minute)) }, docsbot.NeedCategory{}},
		{"4999 total", func(r *docsbottest.FakeRepo) { seedMany(r, 4999, docsbot.StatusSaved, old) }, docsbot.NeedCategory{}},
		{"5000 total", func(r *docsbottest.FakeRepo) { seedMany(r, 5000, docsbot.StatusSaved, old) }, docsbot.QuotaExceeded{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			tt.setup(repo)
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo}, docsbot.WithClock(func() time.Time { return now }))
			before := len(repo.Docs)

			rep, err := svc.Receive(t.Context(), intakeFile("new"))

			require.NoError(t, err)
			require.IsType(t, tt.want, rep)
			if _, ok := rep.(docsbot.NeedCategory); !ok {
				require.Len(t, repo.Docs, before, "a rejected file must not be stored")
			}
		})
	}
}

func TestService_Receive_duplicateBypassesLimits(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	seedMany(repo, 20, docsbot.StatusPending, time.Now().Add(-time.Hour))
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, TgFileUniqueID: "known"})
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})

	rep, err := svc.Receive(t.Context(), intakeFile("known"))

	require.NoError(t, err)
	require.IsType(t, docsbot.AlreadySaved{}, rep)
}

// failingPurgeRepo fails the pending-documents purge.
type failingPurgeRepo struct {
	*docsbottest.FakeRepo
	err error
}

func (r failingPurgeRepo) PurgeStalePending(context.Context, time.Time) (int, error) { return 0, r.err }

// failingPurgeStatus fails the closed-status-messages purge and records that it ran.
type failingPurgeStatus struct {
	*docsbottest.FakeStatus
	err    error
	called *bool
}

func (s failingPurgeStatus) PurgeClosed(context.Context, time.Time) (int, error) {
	*s.called = true
	return 0, s.err
}

func TestService_PurgeStalePending_errors(t *testing.T) {
	boom := errors.New("db down")
	tests := []struct {
		name          string
		failRepo      bool
		wantRes       docsbot.PurgeResult
		wantPurgeSeen bool
	}{
		{"documents step fails: status messages are not purged", true, docsbot.PurgeResult{}, false},
		{"status step fails: the result keeps the documents purged", false, docsbot.PurgeResult{Pending: 1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
			repo := docsbottest.NewFakeRepo()
			repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "a", CreatedAt: now.Add(-25 * time.Hour)})
			var purgeSeen bool
			var r docsbot.Repo = repo
			var st docsbot.StatusMessages = failingPurgeStatus{FakeStatus: docsbottest.NewFakeStatus(repo), err: boom, called: &purgeSeen}
			if tt.failRepo {
				r = failingPurgeRepo{FakeRepo: repo, err: boom}
			}
			svc := docsbottest.NewService(r, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, st, &docsbottest.FakeSyncs{Repo: repo}, docsbot.WithClock(func() time.Time { return now }))

			res, err := svc.PurgeStalePending(t.Context())

			require.ErrorIs(t, err, boom)
			require.Equal(t, tt.wantRes, res)
			require.Equal(t, tt.wantPurgeSeen, purgeSeen)
		})
	}
}

func TestService_PurgeStalePending(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	repo := docsbottest.NewFakeRepo()
	stale := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "a", CreatedAt: now.Add(-25 * time.Hour)})
	fresh := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "b", CreatedAt: now.Add(-23 * time.Hour)})
	oldSaved := repo.Seed(docsbot.Document{UserID: stranger, Status: docsbot.StatusSaved, TgFileUniqueID: "c", CreatedAt: now.Add(-48 * time.Hour)})
	status := docsbottest.NewFakeStatus(repo)
	status.Msgs["old-closed"] = docsbot.StatusMessage{ID: "old-closed", UserID: user, Closed: true, CreatedAt: now.Add(-48 * time.Hour)}
	status.Msgs["old-open"] = docsbot.StatusMessage{ID: "old-open", UserID: user, CreatedAt: now.Add(-48 * time.Hour)}
	status.Msgs["new-closed"] = docsbot.StatusMessage{ID: "new-closed", UserID: user, Closed: true, CreatedAt: now.Add(-time.Hour)}
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, status, &docsbottest.FakeSyncs{Repo: repo}, docsbot.WithClock(func() time.Time { return now }))

	res, err := svc.PurgeStalePending(t.Context())

	require.NoError(t, err)
	require.Equal(t, docsbot.PurgeResult{Pending: 1, StatusMessages: 1}, res)
	require.NotContains(t, status.Msgs, docsbot.StatusMessageID("old-closed"))
	require.Contains(t, status.Msgs, docsbot.StatusMessageID("old-open"), "an open message is never purged")
	require.Contains(t, status.Msgs, docsbot.StatusMessageID("new-closed"))
	require.NotContains(t, repo.Docs, stale.ID)
	require.Contains(t, repo.Docs, fresh.ID)
	require.Contains(t, repo.Docs, oldSaved.ID, "saved documents are never purged")
}
