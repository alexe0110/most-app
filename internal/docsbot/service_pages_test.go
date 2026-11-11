package docsbot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// fakePages counts every payload as pages pages, or fails with err.
type fakePages struct {
	pages int
	err   error
	calls *int
}

func (p fakePages) PageCount(context.Context, []byte) (int, error) {
	if p.calls != nil {
		*p.calls++
	}
	return p.pages, p.err
}

func TestService_Backup_countsPdfPages(t *testing.T) {
	tests := []struct {
		name      string
		docMIME   string
		dlMIME    string
		counter   docsbot.PageCounter
		stored    int
		wantPages int
		wantCalls int
	}{
		{"pdf is counted", "application/pdf", "application/pdf", fakePages{pages: 7}, 0, 7, 1},
		{"download mime decides when the row has none", "", "application/pdf", fakePages{pages: 2}, 0, 2, 1},
		{"image is not counted", "image/jpeg", "image/jpeg", fakePages{pages: 7}, 0, 0, 0},
		{"count error leaves pages unknown, upload still works", "application/pdf", "application/pdf", fakePages{err: errors.New("bad pdf")}, 0, 0, 1},
		{"unknown count keeps the stored value", "application/pdf", "application/pdf", fakePages{err: errors.New("bad pdf")}, 4, 4, 1},
		{"no counter configured", "application/pdf", "application/pdf", nil, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{
				UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, Kind: docsbot.KindDocument,
				MIME: tt.docMIME, TgFileID: "tg-1", BackupStatus: docsbot.BackupPending, PageCount: tt.stored,
			})
			var calls int
			opts := []docsbot.Option{}
			if fp, ok := tt.counter.(fakePages); ok {
				fp.calls = &calls
				opts = append(opts, docsbot.WithPageCounter(fp))
			}
			svc := docsbot.NewService(repo, fakeDrive{uploadFile: docsbot.DriveFile{ID: "drv"}}, fakeFiles{data: []byte("x"), mime: tt.dlMIME},
				&fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo}, opts...)

			res, err := svc.Backup(t.Context(), user, doc.ID)

			require.NoError(t, err)
			require.Equal(t, docsbot.BackupBackedUp, repo.docs[doc.ID].BackupStatus, "page counting never blocks the upload")
			require.Equal(t, tt.wantPages, repo.docs[doc.ID].PageCount)
			require.Equal(t, tt.wantPages, res.Doc.PageCount)
			require.Equal(t, tt.wantCalls, calls)
		})
	}
}
