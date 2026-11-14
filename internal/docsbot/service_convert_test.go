package docsbot_test

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
)

func TestDerivedTitle(t *testing.T) {
	long := strings.Repeat("я", 150)
	tests := []struct {
		name, src, suffix, want string
	}{
		{"plain", "Паспорт", " (сжатый)", "Паспорт (сжатый)"},
		{"extension dropped", "scan.jpg", " (PDF)", "scan (PDF)"},
		{"year-like tail kept", "Договор 2026", " (PDF)", "Договор 2026 (PDF)"},
		{"dotted number kept", "Чек 12.05", " (PDF)", "Чек 12.05 (PDF)"},
		{"empty source", "", " (сжатый)", "(сжатый)"},
		{"extension only", ".jpg", " (PDF)", "(PDF)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, docsbot.DerivedTitle(tt.src, tt.suffix))
		})
	}

	t.Run("long base is trimmed, suffix kept", func(t *testing.T) {
		got := docsbot.DerivedTitle(long, " (сжатый)")
		require.True(t, strings.HasSuffix(got, " (сжатый)"))
		require.Equal(t, docsbot.MaxTitleRunes, utf8.RuneCountInString(got))
	})
}

func TestService_SaveDerived(t *testing.T) {
	file := docsbot.IncomingFile{Kind: docsbot.KindDocument, FileID: "f-new", FileUniqueID: "u-new", Title: "photo-compressed.jpg", MIME: "image/jpeg"}

	tests := []struct {
		name      string
		source    *docsbot.Document // nil: no source id
		seedStats func(*docsbottest.FakeRepo)
		wantType  docsbot.Reply
		wantTitle string
		wantCat   docsbot.Category
	}{
		{
			name:      "saved source: copy lands in the same category",
			source:    &docsbot.Document{Status: docsbot.StatusSaved, Category: docsbot.CatPassport, Title: "Паспорт.jpg", TgFileUniqueID: "u-src"},
			wantType:  docsbot.Saved{},
			wantTitle: "Паспорт (сжатый)",
			wantCat:   docsbot.CatPassport,
		},
		{
			name:      "pending source: saved in Other under its own title",
			source:    &docsbot.Document{Status: docsbot.StatusPending, Title: "x", TgFileUniqueID: "u-src"},
			wantType:  docsbot.Saved{},
			wantTitle: "photo-compressed.jpg",
			wantCat:   docsbot.CatOther,
		},
		{
			name:      "no source: saved in Other under its own title",
			wantType:  docsbot.Saved{},
			wantTitle: "photo-compressed.jpg",
			wantCat:   docsbot.CatOther,
		},
		{
			name:     "limit reply passes through",
			source:   &docsbot.Document{Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "A", TgFileUniqueID: "u-src"},
			wantType: docsbot.TooManyPending{},
			seedStats: func(r *docsbottest.FakeRepo) {
				seedMany(r, 20, docsbot.StatusPending, time.Now().Add(-48*time.Hour))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			svc, _ := edgeService(repo, docsbottest.FakeDrive{})
			var srcID docsbot.DocumentID
			if tt.source != nil {
				d := *tt.source
				d.UserID = user
				srcID = repo.Seed(d).ID
			}
			if tt.seedStats != nil {
				tt.seedStats(repo)
			}

			rep, err := svc.SaveDerived(t.Context(), user, srcID, file, " (сжатый)")

			require.NoError(t, err)
			require.IsType(t, tt.wantType, rep)
			switch r := rep.(type) {
			case docsbot.Saved:
				require.Equal(t, tt.wantTitle, r.Doc.Title)
				require.Equal(t, tt.wantCat, r.Doc.Category)
				require.Equal(t, docsbot.StatusSaved, repo.Docs[r.Doc.ID].Status)
				if tt.source != nil {
					require.Equal(t, tt.source.Title, repo.Docs[srcID].Title, "source untouched")
				}
			case docsbot.NeedCategory:
				require.Equal(t, tt.wantTitle, r.Doc.Title)
				require.Equal(t, docsbot.StatusPending, r.Doc.Status)
			}
		})
	}
}

func TestService_SaveDerived_unknownSource(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})
	other := repo.Seed(docsbot.Document{UserID: stranger, Status: docsbot.StatusSaved, Category: docsbot.CatOther, TgFileUniqueID: "x"})

	for _, id := range []docsbot.DocumentID{"missing", other.ID} {
		_, err := svc.SaveDerived(t.Context(), user, id, docsbot.IncomingFile{FileUniqueID: "u"}, " (PDF)")
		require.ErrorIs(t, err, docsbot.ErrNotFound)
	}
	require.Len(t, repo.Docs, 1, "nothing created")
}

func TestService_DiscardAlbum(t *testing.T) {
	tests := []struct {
		name      string
		group     string
		wantN     int
		wantAfter int // documents left in the repo
	}{
		{"removes only pending of the album", "alb", 2, 4},
		{"unknown album", "nope", 0, 6},
		{"empty group", "", 0, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			svc, _ := edgeService(repo, docsbottest.FakeDrive{})
			repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, MediaGroupID: "alb", TgFileUniqueID: "a1"})
			repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, MediaGroupID: "alb", TgFileUniqueID: "a2"})
			repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, MediaGroupID: "alb", TgFileUniqueID: "a3", Category: docsbot.CatOther})
			repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, MediaGroupID: "other", TgFileUniqueID: "b1"})
			repo.Seed(docsbot.Document{UserID: stranger, Status: docsbot.StatusPending, MediaGroupID: "alb", TgFileUniqueID: "c1"})
			repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, TgFileUniqueID: "d1"})

			n, err := svc.DiscardAlbum(t.Context(), user, tt.group)

			require.NoError(t, err)
			require.Equal(t, tt.wantN, n)
			require.Len(t, repo.Docs, tt.wantAfter)
		})
	}
}

func TestService_SaveDerived_failedSaveDropsTheCopy(t *testing.T) {
	boom := errors.New("drive down")
	repo := docsbottest.NewFakeRepo()
	svc, _ := edgeService(repo, docsbottest.FakeDrive{Err: boom})
	src := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "A", TgFileUniqueID: "u-src"})

	_, err := svc.SaveDerived(t.Context(), user, src.ID, docsbot.IncomingFile{FileUniqueID: "u-new", Title: "x"}, " (PDF)")

	require.ErrorIs(t, err, boom)
	require.Len(t, repo.Docs, 1, "only the source remains")
}

func TestService_SaveDerived_alreadySavedIsNotDeleted(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})
	src := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "A", TgFileUniqueID: "u-src"})
	dup := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "B", TgFileUniqueID: "u-new"})

	rep, err := svc.SaveDerived(t.Context(), user, src.ID, docsbot.IncomingFile{FileUniqueID: "u-new"}, " (PDF)")

	require.NoError(t, err)
	require.IsType(t, docsbot.AlreadySaved{}, rep)
	require.Contains(t, repo.Docs, dup.ID)
}

func TestService_SaveDerived_noSourceFailedSaveDropsTheCopy(t *testing.T) {
	boom := errors.New("drive down")
	repo := docsbottest.NewFakeRepo()
	svc, _ := edgeService(repo, docsbottest.FakeDrive{Err: boom})

	_, err := svc.SaveDerived(t.Context(), user, "", docsbot.IncomingFile{FileUniqueID: "u-new", Title: "x"}, " (PDF)")

	require.ErrorIs(t, err, boom)
	require.Empty(t, repo.Docs, "the copy is not left pending")
}
