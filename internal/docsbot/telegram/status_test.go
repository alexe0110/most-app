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
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// statusEdits keeps only edits of the batch status message.
func statusEdits(c *capture) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, e := range c.edits {
		if strings.Contains(e, "Загружаю на Google Диск ·") {
			out = append(out, e)
		}
	}
	return out
}

func seedPending(repo *docsbottest.FakeRepo, n int) []docsbot.DocumentID {
	var ids []docsbot.DocumentID
	for i := 0; i < n; i++ {
		d := repo.Seed(docsbot.Document{
			UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, Kind: docsbot.KindDocument,
			Title: fmt.Sprintf("file%d.pdf", i), TgFileID: fmt.Sprintf("f%d", i), TgFileUniqueID: fmt.Sprintf("u%d", i),
		})
		ids = append(ids, d.ID)
	}
	return ids
}

// pick sends a category choice for the document from picker message msgID.
func pick(ctx context.Context, b *bot.Bot, id docsbot.DocumentID, msgID int) {
	b.ProcessUpdate(ctx, dataCallbackMsg(msgID, "d:cat:"+string(id)+":"+string(docsbot.CatOther)))
}

func TestChooseCategory_driveOn_oneFileBecomesLiveCard(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	id := seedPending(repo, 1)[0]

	pick(ctx, b, id, 11)

	require.Len(t, sent.texts, 1, "the picker is edited, only the first-document hint is new")
	require.Contains(t, sent.texts[0], "Чтобы найти его потом")
	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], "🗂 ")
	require.Contains(t, sent.edits[0], telegram.TextStoragePending)
	require.NotEmpty(t, repo.Docs[id].StatusMessageID)
	sm, err := repo.Status.ByID(t.Context(), docsbottest.TestUserID, repo.Docs[id].StatusMessageID)
	require.NoError(t, err)
	require.Equal(t, docsbot.StatusSingle, sm.Kind)
	require.Equal(t, 11, sm.MessageID)
}

func TestChooseCategory_driveOn_manyFilesShareOneBatch(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	ids := seedPending(repo, 5)

	for i, id := range ids {
		pick(ctx, b, id, 11+i)
	}

	require.Len(t, sent.texts, 1, "pickers are edited or deleted, only the first-document hint is new")
	require.Equal(t, 3, sent.deletes, "pickers of files 3..5 are retired")

	first, err := repo.Status.ByID(t.Context(), docsbottest.TestUserID, repo.Docs[ids[0]].StatusMessageID)
	require.NoError(t, err)
	require.Equal(t, docsbot.StatusSingle, first.Kind, "the first card stays a card")
	require.False(t, has(sent.edits[:1], "Загружаю на Google Диск ·"))

	batches := statusEdits(sent)
	require.Len(t, batches, 4, "file 2 opens the batch, files 3..5 refresh it")
	last := batches[len(batches)-1]
	require.Contains(t, last, fmt.Sprintf(telegram.TextStatusProgress, 0, 4))

	smIDs := map[docsbot.StatusMessageID]bool{}
	for _, d := range repo.Docs {
		require.NotEmpty(t, d.StatusMessageID)
		smIDs[d.StatusMessageID] = true
	}
	require.Len(t, smIDs, 2)
}

func TestChooseCategory_driveOn_closedBatchOpensNewCard(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	ids := seedPending(repo, 2)

	pick(ctx, b, ids[0], 11)
	// The worker finished the only line, which closes the message.
	first := repo.Docs[ids[0]].StatusMessageID
	require.NoError(t, closeStatus(t, repo, first))

	pick(ctx, b, ids[1], 12)
	require.Empty(t, statusEdits(sent), "after completion the next file is a card again")
	require.Len(t, sent.edits, 2)
	require.NotEqual(t, first, repo.Docs[ids[1]].StatusMessageID)
}

func TestChooseCategory_driveOff_pickerBecomesCard(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	for i, id := range seedPending(repo, 3) {
		pick(ctx, b, id, 11+i)
	}

	require.Len(t, sent.edits, 3)
	for _, e := range sent.edits {
		require.Contains(t, e, "🗂 ")
		require.NotContains(t, e, "✅")
	}
	hints := 0
	for _, text := range sent.texts {
		if strings.Contains(text, "Чтобы найти его потом") {
			hints++
		}
	}
	require.Equal(t, 1, hints, "only the first document gets the hint")
	require.Empty(t, statusEdits(sent))
	for _, d := range repo.Docs {
		require.Empty(t, d.StatusMessageID)
	}
}

func newSM(t *testing.T, repo *docsbottest.FakeRepo, owner docsbot.UserID) docsbot.StatusMessageID {
	t.Helper()
	sm, err := repo.Status.Create(t.Context(), owner, 100, 70, docsbot.StatusBatch)
	require.NoError(t, err)
	return sm.ID
}

func closeStatus(t *testing.T, repo *docsbottest.FakeRepo, id docsbot.StatusMessageID) error {
	t.Helper()
	return repo.Status.Close(t.Context(), docsbottest.TestUserID, id)
}

func retryUpdate(smID docsbot.StatusMessageID) *models.Update {
	return dataCallback("d:rsm:" + string(smID))
}

func TestRetryBatch_requeuesFailedAndRedraws(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	sm := newSM(t, repo, docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Title: "bad.pdf", BackupStatus: docsbot.BackupFailed, StatusMessageID: sm})
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Title: "ok.pdf", BackupStatus: docsbot.BackupBackedUp, DriveFileID: "F", StatusMessageID: sm})

	b.ProcessUpdate(ctx, retryUpdate(sm))

	edits := statusEdits(sent)
	require.Len(t, edits, 1)
	require.Contains(t, edits[0], "ok.pdf", "the transferred document stays listed")
	require.NotContains(t, edits[0], "Не перенёс", "the failed document is back in progress")
	require.Contains(t, edits[0], fmt.Sprintf(telegram.TextStatusProgress, 1, 2))
}

func TestRetryBatch_foreignMessageIsIgnored(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), "99999999-9999-9999-9999-999999999999")

	sm := newSM(t, repo, docsbottest.TestUserID)
	bad := repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Title: "bad.pdf", BackupStatus: docsbot.BackupFailed, StatusMessageID: sm})

	b.ProcessUpdate(ctx, retryUpdate(sm))

	require.Empty(t, statusEdits(sent), "someone else's batch is never redrawn")
	require.Equal(t, docsbot.BackupFailed, repo.Docs[bad.ID].BackupStatus, "and never re-queued")
}

func TestTransfer_editsOfferIntoStatusMessage(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	for i := 0; i < 3; i++ {
		repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: fmt.Sprintf("f%d", i)})
	}

	b.ProcessUpdate(ctx, dataCallbackMsg(21, "d:try"))

	require.Empty(t, sent.texts, "no per-document or extra message")
	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], fmt.Sprintf(telegram.TextStatusProgress, 0, 3))
}

func TestTransfer_nothingToTransfer(t *testing.T) {
	b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallbackMsg(21, "d:try"))

	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], telegram.TextNothingToTransfer)
}

func TestRetryAll_editsSettingsIntoStatusMessage(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed, Title: "x"})

	b.ProcessUpdate(ctx, dataCallbackMsg(22, "d:rta"))

	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], fmt.Sprintf(telegram.TextStatusProgress, 0, 1))
}

// nudges counts the connect-Drive offers among sent messages.
func nudges(c *capture) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, m := range c.texts {
		if strings.Contains(m, "Google Диск") {
			n++
		}
	}
	return n
}

func TestChooseCategory_driveOff_nudgeOnlyOnFirstAndFifth(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
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
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	for i, id := range seedPending(repo, 5) {
		pick(ctx, b, id, 11+i)
	}
	require.Zero(t, nudges(sent))
}

func TestTransfer_manyDocsShareOneStatusMessage(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	var seeded []docsbot.DocumentID
	for i := 0; i < 7; i++ {
		d := repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: fmt.Sprintf("f%d", i)})
		seeded = append(seeded, d.ID)
	}

	b.ProcessUpdate(ctx, dataCallbackMsg(21, "d:try"))

	require.Empty(t, sent.texts, "one status message, no per-document notices")
	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], fmt.Sprintf(telegram.TextStatusProgress, 0, 7))
	smIDs := map[docsbot.StatusMessageID]bool{}
	for _, id := range seeded {
		require.NotEmpty(t, repo.Docs[id].StatusMessageID)
		smIDs[repo.Docs[id].StatusMessageID] = true
	}
	require.Len(t, smIDs, 1)
	sm, err := repo.Status.ByID(t.Context(), docsbottest.TestUserID, repo.Docs[seeded[0]].StatusMessageID)
	require.NoError(t, err)
	require.Equal(t, docsbot.StatusTransfer, sm.Kind)
	require.Equal(t, 21, sm.MessageID)
}

func TestRetryAll_nothingFailed(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupBackedUp, Title: "x"})

	b.ProcessUpdate(ctx, dataCallbackMsg(22, "d:rta"))

	require.Empty(t, sent.texts)
	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], telegram.TextNoFailed)
}

func TestTransfer_otherUsersDocsAreNotTouched(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	other := repo.Seed(docsbot.Document{UserID: "other-user", Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "theirs"})

	b.ProcessUpdate(ctx, dataCallbackMsg(21, "d:try"))

	require.Empty(t, repo.Docs[other.ID].StatusMessageID)
	require.Contains(t, sent.edits[0], telegram.TextNothingToTransfer)
}

func TestLogs_retryAllAndUploadStartWithoutLeaks(t *testing.T) {
	var sink lockedBuffer
	log := slog.New(slog.NewJSONHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, _, repo := newBotWith(t, log, docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed, Title: "СекретныйTITLE"})

	b.ProcessUpdate(ctx, dataCallbackMsg(22, "d:rta"))

	out := sink.String()
	require.Contains(t, out, "retry all")
	require.Contains(t, out, `"count":1`)
	for _, leak := range []string{"СекретныйTITLE", "123:ABC", "chat_id", "tg_user_id"} {
		require.NotContains(t, out, leak)
	}
}
