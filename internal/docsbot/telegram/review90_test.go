package telegram_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// A status message card has «Имя». The rename ends in the status rendering, so the
// retry row stays, both after «Отмена» and after a successful rename.
func TestRename_onStatusCardKeepsStatusRendering(t *testing.T) {
	for _, finish := range []string{"cancel", "rename"} {
		t.Run(finish, func(t *testing.T) {
			b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			id := seedPending(repo, 1)[0]
			pick(ctx, b, id, 11) // the picker message 11 becomes the live status card
			d := repo.docs[id]
			d.BackupStatus = docsbot.BackupFailed
			repo.docs[id] = d

			b.ProcessUpdate(ctx, dataCallbackMsg(11, "d:rn:"+string(id)))
			require.Equal(t, "Пришли новое название", sent.edits[len(sent.edits)-1])

			if finish == "cancel" {
				b.ProcessUpdate(ctx, dataCallbackMsg(11, "d:rnc:"+string(id)))
			} else {
				b.ProcessUpdate(ctx, textUpdateID(50, "Новое имя"))
				require.Equal(t, "Новое имя", repo.docs[id].Title)
			}

			require.Equal(t, 11, sent.editIDs[len(sent.editIDs)-1])
			require.Contains(t, sent.edits[len(sent.edits)-1], "❌", "the status storage line")
			require.Contains(t, sent.editMarkups[len(sent.editMarkups)-1], "d:rsm:", "the retry-batch row is kept")
		})
	}
}

// The worker redraws the status message while a rename prompt sits on it: the
// armed rename must go, or the next text would silently rename the document.
func TestRename_statusRedrawEndsThePrompt(t *testing.T) {
	rn := telegram.NewRenames()
	b, _, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithRenames(rn))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")

	// newNotifierEnv reroutes the default HTTP transport to its own fake Bot API, so
	// from here on that fake sees the calls of both the bot handlers and the notifier.
	e := newNotifierEnv(t)
	n := telegram.NewNotifier(e.bot, chatLookup{}, fakeAuth{}, e.svc, slog.New(slog.DiscardHandler), telegram.WithNotifierRenames(rn))
	_, docs := e.batch(t, docsbot.BackupBackedUp)

	// The prompt is armed on the status message (chat 100, message 70).
	b.ProcessUpdate(ctx, dataCallbackMsg(70, "d:rn:"+string(doc.ID)))
	require.NoError(t, n.UploadOutcome(t.Context(), testUserID, docs[0].ID))

	b.ProcessUpdate(ctx, textUpdateID(51, "Случайный текст"))

	require.Equal(t, "Старое", repo.docs[doc.ID].Title, "the text is a search, not a rename")
	require.Empty(t, e.api.callsOf("deleteMessage"), "the message is not eaten")
	var texts []string
	for _, c := range e.api.callsOf("sendMessage") {
		texts = append(texts, c.text)
	}
	require.True(t, has(texts, "Ничего не нашёл"), "answered as a search: %q", texts)
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

// snapshot copies a capture bucket under the lock (handlers may run concurrently).
func (c *capture) snapshot(bucket *[]string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), *bucket...)
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
