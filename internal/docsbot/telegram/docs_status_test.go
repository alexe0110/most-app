package telegram_test

import (
	"cmp"
	"log/slog"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestRetry_failedDocumentIsRequeued(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	doc := repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})

	b.ProcessUpdate(ctx, dataCallback("d:rt:"+string(doc.ID)))

	require.Equal(t, docsbot.BackupPending, repo.Docs[doc.ID].BackupStatus)
	require.True(t, has(sent.edits, telegram.TextRetryQueued), "edits %q", sent.edits)
	require.Equal(t, []string{""}, sent.answers, "no toast for a successful retry")
}

func TestRetry_notFailedShowsQuietToast(t *testing.T) {
	for _, st := range []docsbot.BackupStatus{docsbot.BackupBackedUp, docsbot.BackupUploading, docsbot.BackupPending} {
		t.Run(string(st), func(t *testing.T) {
			b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			doc := repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, BackupStatus: st})

			b.ProcessUpdate(ctx, dataCallback("d:rt:"+string(doc.ID)))

			require.Equal(t, []string{telegram.ToastNotRetryable}, sent.answers)
			require.Empty(t, sent.edits, "the card must not be rewritten")
			require.Empty(t, sent.texts, "and no extra message is sent")
			require.Equal(t, st, repo.Docs[doc.ID].BackupStatus)
		})
	}
}

func TestRetry_foreignDocumentIsIgnored(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), "99999999-9999-9999-9999-999999999999")
	doc := repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})

	b.ProcessUpdate(ctx, dataCallback("d:rt:"+string(doc.ID)))

	require.Equal(t, docsbot.BackupFailed, repo.Docs[doc.ID].BackupStatus)
	require.Equal(t, []string{telegram.ToastNotRetryable}, sent.answers)
}

func TestRetry_earlyExitsStillAnswerCallback(t *testing.T) {
	t.Run("no user in context", func(t *testing.T) {
		b, sent, _ := newBot(t)

		b.ProcessUpdate(t.Context(), dataCallback("d:rt:doc-1"))

		require.Equal(t, []string{""}, sent.answers)
	})
	t.Run("no document id", func(t *testing.T) {
		b, sent, _ := newBot(t)
		ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

		b.ProcessUpdate(ctx, dataCallback("d:rt"))

		require.Equal(t, []string{""}, sent.answers)
	})
}

func TestRetrySync(t *testing.T) {
	tests := []struct {
		name       string
		doc        docsbot.Document
		user       string
		data       func(docsbot.DocumentID) string
		press      func(*models.Update) *models.Update
		wantToast  string
		wantSync   docsbot.SyncStatus
		wantRedraw bool
	}{
		{
			name:       "failed sync is requeued and the card shows the update",
			doc:        docsbot.Document{BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncFailed},
			wantSync:   docsbot.SyncPending,
			wantRedraw: true,
		},
		{
			name:      "sync that did not fail shows the quiet toast",
			doc:       docsbot.Document{BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncOK},
			wantToast: telegram.ToastNotRetryable,
			wantSync:  docsbot.SyncOK,
		},
		{
			name:      "document outside Drive shows the quiet toast",
			doc:       docsbot.Document{BackupStatus: docsbot.BackupFailed, SyncStatus: docsbot.SyncFailed},
			wantToast: telegram.ToastNotRetryable,
			wantSync:  docsbot.SyncFailed,
		},
		{
			name:      "foreign document is ignored",
			doc:       docsbot.Document{BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncFailed},
			user:      "99999999-9999-9999-9999-999999999999",
			wantToast: telegram.ToastNotRetryable,
			wantSync:  docsbot.SyncFailed,
		},
		{
			name:     "no document id",
			doc:      docsbot.Document{BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncFailed},
			data:     func(docsbot.DocumentID) string { return "d:rs" },
			wantSync: docsbot.SyncFailed,
		},
		{
			name:     "message too old to edit",
			doc:      docsbot.Document{BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncFailed},
			press:    inaccessible,
			wantSync: docsbot.SyncFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
			user := cmp.Or(tt.user, docsbottest.TestUserID)
			ctx := tg.ContextWithUserID(t.Context(), user)
			tt.doc.UserID, tt.doc.Status = docsbottest.TestUserID, docsbot.StatusSaved
			doc := repo.Seed(tt.doc)
			data := func(id docsbot.DocumentID) string { return "d:rs:" + string(id) }
			if tt.data != nil {
				data = tt.data
			}
			update := dataCallback(data(doc.ID))
			if tt.press != nil {
				update = tt.press(update)
			}

			b.ProcessUpdate(ctx, update)

			require.Equal(t, []string{tt.wantToast}, sent.answers)
			require.Equal(t, tt.wantSync, repo.Docs[doc.ID].SyncStatus)
			require.Empty(t, sent.texts, "no error message is sent")
			if !tt.wantRedraw {
				require.Empty(t, sent.edits, "the card must not be rewritten")
				return
			}
			require.Len(t, sent.edits, 1)
			require.Contains(t, sent.edits[0], telegram.TextStorageSyncing)
		})
	}
}
