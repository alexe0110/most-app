package docsbot_test

import (
	"strings"
	"testing"
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
