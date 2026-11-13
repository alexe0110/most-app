package telegram_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestIsExport(t *testing.T) {
	tests := []struct {
		name string
		u    *models.Update
		want bool
	}{
		{"command", convText("/export"), true},
		{"command with bot name", convText("/export@most_bot"), true},
		{"export button", callbackUpdate("d:exp"), true},
		{"other command", convText("/list"), false},
		{"similar command", convText("/exports"), false},
		{"other button", callbackUpdate("d:del"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, telegram.IsExport(tt.u))
		})
	}
}

func TestExport_rateLimitIsRetriedOnceAndSentIsCounted(t *testing.T) {
	tests := []struct {
		name      string
		limited   int
		down      bool
		wantAlbum int
		wantDone  string
	}{
		{"one 429 is waited out and retried", 1, false, 1, "Готово — прислал 3"},
		{"two 429s: the second is not retried", 2, false, 0, "Готово — прислал 0"},
		{"failed album is not counted", 0, true, 0, "Готово — прислал 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithExportPace(0, 0))
			sent.mediaLimit, sent.mediaDown = tt.limited, tt.down
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			seedExport(repo, docsbot.KindDocument, 3, "d", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

			b.ProcessUpdate(ctx, commandUpdate(100, "export"))

			require.Len(t, sent.groups, tt.wantAlbum)
			require.True(t, has(sent.texts, tt.wantDone), "texts %q", sent.texts)
		})
	}
}

func TestExport_buttonIsSpentAndASecondRunIsRefused(t *testing.T) {
	b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithExportPace(time.Hour, time.Hour))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedExport(repo, docsbot.KindPhoto, 1, "p", base)
	seedExport(repo, docsbot.KindDocument, 1, "d", base.Add(time.Hour))

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.ProcessUpdate(runCtx, dataCallbackMsg(9, "d:exp")) // blocks in the pause after the first file
	}()
	require.Eventually(t, func() bool { return len(sent.snapshot(&sent.files)) == 1 }, 5*time.Second, 5*time.Millisecond)

	b.ProcessUpdate(ctx, commandUpdate(100, "export")) // a parallel run
	b.ProcessUpdate(ctx, dataCallbackMsg(9, "d:exp"))
	stop()
	<-done

	require.Len(t, sent.snapshot(&sent.files), 1, "nothing was sent twice")
	refused := 0
	for _, text := range sent.snapshot(&sent.texts) {
		if strings.Contains(text, "подожди, пока закончу") {
			refused++
		}
	}
	require.Equal(t, 2, refused, "both the command and the second tap are told to wait")
	require.NotEmpty(t, sent.snapshot(&sent.markupEdits), "the tapped button's keyboard is removed")
}

// seedExport stores n saved documents of one kind, oldest first.
func seedExport(repo *fakeRepo, kind docsbot.Kind, n int, prefix string, base time.Time) {
	for i := range n {
		id := fmt.Sprintf("%s%d", prefix, i)
		repo.seed(docsbot.Document{
			UserID: testUserID, Status: docsbot.StatusSaved, Kind: kind, Category: docsbot.CatOther,
			Title: "Док " + id, TgFileID: "file-" + id, TgFileUniqueID: id, CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
}

func TestExport_sendsAlbumsByKind(t *testing.T) {
	b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithExportPace(0, 0))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedExport(repo, docsbot.KindPhoto, 12, "p", base)                  // an album of 10, then an album of 2
	seedExport(repo, docsbot.KindDocument, 1, "d", base.Add(time.Hour)) // a lone document
	seedExport(repo, docsbot.KindPhoto, 1, "q", base.Add(2*time.Hour))  // a lone photo

	b.ProcessUpdate(ctx, commandUpdate(100, "export"))

	require.Len(t, sent.groups, 2, "two albums")
	require.Equal(t, 10, strings.Count(sent.groups[0], `"type":"photo"`))
	require.Equal(t, 2, strings.Count(sent.groups[1], `"type":"photo"`))
	require.Contains(t, sent.groups[0], "Док p0 · Другое", "caption: title and category")
	require.Len(t, sent.files, 2, "the lone document and the lone photo are sent as files")
	require.True(t, has(sent.texts, "Присылаю"), "texts %q", sent.texts)
	require.True(t, has(sent.texts, "Готово — прислал 14"), "texts %q", sent.texts)
}

func TestExport_documentAlbum(t *testing.T) {
	b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithExportPace(0, 0))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	seedExport(repo, docsbot.KindDocument, 3, "d", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	b.ProcessUpdate(ctx, dataCallback("d:exp"))

	require.Len(t, sent.groups, 1)
	require.Equal(t, 3, strings.Count(sent.groups[0], `"type":"document"`))
	require.Empty(t, sent.files)
}

func TestExport_empty(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, commandUpdate(100, "export"))

	require.True(t, has(sent.texts, "нечего присылать"), "texts %q", sent.texts)
	require.Empty(t, sent.groups)
}

func TestExport_commandIsNotInTheMenu(t *testing.T) {
	for _, c := range telegram.Commands() {
		require.NotEqual(t, "export", c.Command)
	}
}

func TestExport_cancelStopsBetweenBatches(t *testing.T) {
	b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithExportPace(time.Hour, time.Hour))
	ctx, cancel := context.WithCancel(tg.ContextWithUserID(t.Context(), testUserID))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedExport(repo, docsbot.KindPhoto, 1, "p", base)
	seedExport(repo, docsbot.KindDocument, 1, "d", base.Add(time.Hour))
	time.AfterFunc(100*time.Millisecond, cancel)

	b.ProcessUpdate(ctx, commandUpdate(100, "export"))

	require.Len(t, sent.files, 1, "stops before the second batch")
	require.False(t, has(sent.texts, "Готово"), "texts %q", sent.texts)
}

func TestExport_exactlyTenDocumentsIsOneAlbumAndElevenSplits(t *testing.T) {
	for _, tt := range []struct {
		n          int
		wantGroups int
		wantFiles  int
	}{{10, 1, 0}, {11, 1, 1}} {
		b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithExportPace(0, 0))
		ctx := tg.ContextWithUserID(t.Context(), testUserID)
		seedExport(repo, docsbot.KindDocument, tt.n, "d", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

		b.ProcessUpdate(ctx, commandUpdate(100, "export"))

		require.Len(t, sent.groups, tt.wantGroups, "n=%d", tt.n)
		require.Len(t, sent.files, tt.wantFiles, "n=%d", tt.n)
		require.True(t, has(sent.texts, fmt.Sprintf("прислал %d", tt.n)), "texts %q", sent.texts)
	}
}

func TestExport_spentButtonKeyboardIsClearedWithEmptyArray(t *testing.T) {
	b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithExportPace(0, 0))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "a"})

	b.ProcessUpdate(ctx, dataCallback("d:exp"))

	edits := sent.snapshot(&sent.markupEdits)
	require.NotEmpty(t, edits, "the tapped button's keyboard is removed")
	require.Contains(t, edits[0], `"inline_keyboard":[]`, "an empty array clears the keyboard")
	require.NotContains(t, edits[0], `"inline_keyboard":null`, "Telegram rejects a null inline_keyboard")
}
