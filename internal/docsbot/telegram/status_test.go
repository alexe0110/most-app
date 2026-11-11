package telegram_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// statusEdits keeps only edits of the batch status message.
func statusEdits(c *capture) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, e := range c.edits {
		if strings.Contains(e, "Загрузка в Drive") {
			out = append(out, e)
		}
	}
	return out
}

func seedPending(repo *fakeRepo, n int) []docsbot.DocumentID {
	var ids []docsbot.DocumentID
	for i := 0; i < n; i++ {
		d := repo.seed(docsbot.Document{
			UserID: testUserID, Status: docsbot.StatusPending, Kind: docsbot.KindDocument,
			Title: fmt.Sprintf("file%d.pdf", i), TgFileID: fmt.Sprintf("f%d", i), TgFileUniqueID: fmt.Sprintf("u%d", i),
		})
		ids = append(ids, d.ID)
	}
	return ids
}

// pick sends a category choice for the document from picker message msgID.
func pick(ctx context.Context, b *bot.Bot, id docsbot.DocumentID, msgID int) {
	b.ProcessUpdate(ctx, dataCallbackMsg(100, msgID, "d:cat:"+string(id)+":"+string(docsbot.CatOther)))
}

func TestChooseCategory_driveOn_oneFileBecomesLiveCard(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	id := seedPending(repo, 1)[0]

	pick(ctx, b, id, 11)

	require.Empty(t, sent.texts, "the picker is edited, nothing new is sent")
	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], "🗂 ")
	require.Contains(t, sent.edits[0], "⏳ Загружаю в Drive…")
	require.NotEmpty(t, repo.docs[id].StatusMessageID)
	sm, err := repo.status.ByID(t.Context(), testUserID, repo.docs[id].StatusMessageID)
	require.NoError(t, err)
	require.Equal(t, docsbot.StatusSingle, sm.Kind)
	require.Equal(t, 11, sm.MessageID)
}

func TestChooseCategory_driveOn_manyFilesShareOneBatch(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	ids := seedPending(repo, 5)

	for i, id := range ids {
		pick(ctx, b, id, 11+i)
	}

	require.Empty(t, sent.texts, "pickers are edited or deleted, nothing new is sent")
	require.Equal(t, 3, sent.deletes, "pickers of files 3..5 are retired")

	first, err := repo.status.ByID(t.Context(), testUserID, repo.docs[ids[0]].StatusMessageID)
	require.NoError(t, err)
	require.Equal(t, docsbot.StatusSingle, first.Kind, "the first card stays a card")
	require.False(t, has(sent.edits[:1], "Загрузка в Drive"))

	batches := statusEdits(sent)
	require.Len(t, batches, 4, "file 2 opens the batch, files 3..5 refresh it")
	last := batches[len(batches)-1]
	require.Contains(t, last, "Загрузка в Drive (0 из 4)")
	require.Contains(t, last, "/list")
	for i := 1; i < 5; i++ {
		require.Contains(t, last, fmt.Sprintf("file%d.pdf", i))
	}
	require.NotContains(t, last, "file0.pdf", "the first file lives on its own card")

	smIDs := map[docsbot.StatusMessageID]bool{}
	for _, d := range repo.docs {
		require.NotEmpty(t, d.StatusMessageID)
		smIDs[d.StatusMessageID] = true
	}
	require.Len(t, smIDs, 2)
}

func TestChooseCategory_driveOn_closedBatchOpensNewCard(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	ids := seedPending(repo, 2)

	pick(ctx, b, ids[0], 11)
	// The worker finished the only line, which closes the message.
	first := repo.docs[ids[0]].StatusMessageID
	require.NoError(t, closeStatus(t, repo, first))

	pick(ctx, b, ids[1], 12)
	require.Empty(t, statusEdits(sent), "after completion the next file is a card again")
	require.Len(t, sent.edits, 2)
	require.NotEqual(t, first, repo.docs[ids[1]].StatusMessageID)
}

func TestChooseCategory_driveOff_pickerBecomesCard(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	for i, id := range seedPending(repo, 3) {
		pick(ctx, b, id, 11+i)
	}

	require.Len(t, sent.edits, 3)
	for _, e := range sent.edits {
		require.Contains(t, e, "🗂 ")
		require.NotContains(t, e, "✅")
	}
	require.False(t, has(sent.texts, "Сохранил"))
	require.Empty(t, statusEdits(sent))
	for _, d := range repo.docs {
		require.Empty(t, d.StatusMessageID)
	}
}

func newSM(t *testing.T, repo *fakeRepo, owner docsbot.UserID) docsbot.StatusMessageID {
	t.Helper()
	sm, err := repo.status.Create(t.Context(), owner, 100, 70, docsbot.StatusBatch)
	require.NoError(t, err)
	return sm.ID
}

func closeStatus(t *testing.T, repo *fakeRepo, id docsbot.StatusMessageID) error {
	t.Helper()
	return repo.status.Close(t.Context(), testUserID, id)
}

func retryUpdate(smID docsbot.StatusMessageID) *models.Update {
	return dataCallback(100, "d:rsm:"+string(smID))
}

func TestRetryBatch_requeuesFailedAndRedraws(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	sm := newSM(t, repo, testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Title: "bad.pdf", BackupStatus: docsbot.BackupFailed, StatusMessageID: sm})
	repo.seed(docsbot.Document{UserID: testUserID, Title: "ok.pdf", BackupStatus: docsbot.BackupBackedUp, DriveFileID: "F", StatusMessageID: sm})

	b.ProcessUpdate(ctx, retryUpdate(sm))

	edits := statusEdits(sent)
	require.Len(t, edits, 1)
	require.Contains(t, edits[0], "bad.pdf")
	require.NotContains(t, edits[0], "❌", "the failed line is back in progress")
	require.Contains(t, edits[0], "⏳")
}

func TestRetryBatch_foreignMessageIsIgnored(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), "99999999-9999-9999-9999-999999999999")

	sm := newSM(t, repo, testUserID)
	bad := repo.seed(docsbot.Document{UserID: testUserID, Title: "bad.pdf", BackupStatus: docsbot.BackupFailed, StatusMessageID: sm})

	b.ProcessUpdate(ctx, retryUpdate(sm))

	require.Empty(t, statusEdits(sent), "someone else's batch is never redrawn")
	require.Equal(t, docsbot.BackupFailed, repo.docs[bad.ID].BackupStatus, "and never re-queued")
}

func TestTransfer_editsOfferIntoStatusMessage(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	for i := 0; i < 3; i++ {
		repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: fmt.Sprintf("f%d", i)})
	}

	b.ProcessUpdate(ctx, dataCallbackMsg(100, 21, "d:try"))

	require.Empty(t, sent.texts, "no per-document or extra message")
	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], "Перенос в Drive (0 из 3)")
}

func TestTransfer_nothingToTransfer(t *testing.T) {
	b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, dataCallbackMsg(100, 21, "d:try"))

	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], "Всё уже в Google Drive.")
}

func TestRetryAll_editsSettingsIntoStatusMessage(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed, Title: "x"})

	b.ProcessUpdate(ctx, dataCallbackMsg(100, 22, "d:rta"))

	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], "Перенос в Drive (0 из 1)")
}

// nudges counts the connect-Drive offers among sent messages.
func nudges(c *capture) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range c.texts {
		if strings.Contains(m, "Google Drive") {
			n++
		}
	}
	return n
}

func TestChooseCategory_driveOff_nudgeOnlyOnFirstAndFifth(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	ids := seedPending(repo, 6)

	var after []int
	for i, id := range ids {
		pick(ctx, b, id, 11+i)
		after = append(after, nudges(sent))
	}

	require.Equal(t, []int{1, 1, 1, 1, 2, 2}, after, "nudge after the 1st and 5th document only")
	require.Len(t, sent.edits, 6, "every picker became a card in place")
}

func TestChooseCategory_driveOn_neverNudges(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	for i, id := range seedPending(repo, 5) {
		pick(ctx, b, id, 11+i)
	}
	require.Zero(t, nudges(sent))
}

func TestTransfer_manyDocsShareOneStatusMessage(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	var seeded []docsbot.DocumentID
	for i := 0; i < 7; i++ {
		d := repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: fmt.Sprintf("f%d", i)})
		seeded = append(seeded, d.ID)
	}

	b.ProcessUpdate(ctx, dataCallbackMsg(100, 21, "d:try"))

	require.Empty(t, sent.texts, "one status message, no per-document notices")
	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], "Перенос в Drive (0 из 7)")
	smIDs := map[docsbot.StatusMessageID]bool{}
	for _, id := range seeded {
		require.NotEmpty(t, repo.docs[id].StatusMessageID)
		smIDs[repo.docs[id].StatusMessageID] = true
	}
	require.Len(t, smIDs, 1)
	sm, err := repo.status.ByID(t.Context(), testUserID, repo.docs[seeded[0]].StatusMessageID)
	require.NoError(t, err)
	require.Equal(t, docsbot.StatusTransfer, sm.Kind)
	require.Equal(t, 21, sm.MessageID)
}

func TestRetryAll_nothingFailed(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupBackedUp, Title: "x"})

	b.ProcessUpdate(ctx, dataCallbackMsg(100, 22, "d:rta"))

	require.Empty(t, sent.texts)
	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], "Документов с ошибкой выгрузки нет.")
}

func TestTransfer_otherUsersDocsAreNotTouched(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	other := repo.seed(docsbot.Document{UserID: "other-user", Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "theirs"})

	b.ProcessUpdate(ctx, dataCallbackMsg(100, 21, "d:try"))

	require.Empty(t, repo.docs[other.ID].StatusMessageID)
	require.Contains(t, sent.edits[0], "Всё уже в Google Drive.")
}

func TestLogs_retryAllAndUploadStartWithoutLeaks(t *testing.T) {
	var sink lockedBuffer
	log := slog.New(slog.NewJSONHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, _, repo := newBotWith(t, log, fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed, Title: "СекретныйTITLE"})

	b.ProcessUpdate(ctx, dataCallbackMsg(100, 22, "d:rta"))

	out := sink.String()
	require.Contains(t, out, "retry all")
	require.Contains(t, out, `"count":1`)
	for _, leak := range []string{"СекретныйTITLE", "123:ABC", "chat_id", "tg_user_id"} {
		require.NotContains(t, out, leak)
	}
}
