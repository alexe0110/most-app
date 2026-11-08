package telegram_test

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

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

func TestChooseCategory_driveOn_fiveFilesOneStatusMessage(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	for _, id := range seedPending(repo, 5) {
		b.ProcessUpdate(ctx, dataCallback(100, "d:cat:"+string(id)+":"+string(docsbot.CatOther)))
	}

	require.Len(t, sent.texts, 1, "one status message for the whole batch: %q", sent.texts)
	require.Contains(t, sent.texts[0], "Загрузка в Drive (0 из 1)")
	require.False(t, has(sent.texts, "Сохранил"), "no per-file card when Drive is on")

	edits := statusEdits(sent)
	require.Len(t, edits, 4, "each further file edits the same message")
	last := edits[len(edits)-1]
	require.Contains(t, last, "Загрузка в Drive (0 из 5)")
	for i := 0; i < 5; i++ {
		require.Contains(t, last, fmt.Sprintf("file%d.pdf", i))
	}

	smIDs := map[docsbot.StatusMessageID]bool{}
	for _, d := range repo.docs {
		require.NotEmpty(t, d.StatusMessageID)
		smIDs[d.StatusMessageID] = true
	}
	require.Len(t, smIDs, 1)
}

func TestChooseCategory_driveOn_closedBatchOpensNewMessage(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	ids := seedPending(repo, 2)

	b.ProcessUpdate(ctx, dataCallback(100, "d:cat:"+string(ids[0])+":"+string(docsbot.CatOther)))
	require.Len(t, sent.texts, 1)
	// The worker finished the only line, which closes the message.
	first := repo.docs[ids[0]].StatusMessageID
	require.NoError(t, closeStatus(t, repo, first))

	b.ProcessUpdate(ctx, dataCallback(100, "d:cat:"+string(ids[1])+":"+string(docsbot.CatOther)))
	require.Len(t, sent.texts, 2, "next document after completion opens a new message")
	require.NotEqual(t, first, repo.docs[ids[1]].StatusMessageID)
}

func TestChooseCategory_driveOff_cardPerFileNoStatusMessage(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	for _, id := range seedPending(repo, 3) {
		b.ProcessUpdate(ctx, dataCallback(100, "d:cat:"+string(id)+":"+string(docsbot.CatOther)))
	}

	cards := 0
	for _, m := range sent.texts {
		require.NotContains(t, m, "Загрузка в Drive")
		if strings.Contains(m, "Сохранил") {
			cards++
		}
	}
	require.Equal(t, 3, cards)
	require.Empty(t, statusEdits(sent))
	for _, d := range repo.docs {
		require.Empty(t, d.StatusMessageID)
	}
}

func newSM(t *testing.T, repo *fakeRepo, owner docsbot.UserID) docsbot.StatusMessageID {
	t.Helper()
	sm, err := repo.status.Create(t.Context(), owner, 100, 70)
	require.NoError(t, err)
	return sm.ID
}

func closeStatus(t *testing.T, repo *fakeRepo, id docsbot.StatusMessageID) error {
	t.Helper()
	return repo.status.Close(t.Context(), id)
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
