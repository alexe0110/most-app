package telegram_test

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
)

// A failed sync shows the document card with the button that fits the cause:
// retry, reconnect (with the consent link) or re-upload.
func TestNotifier_SyncOutcome(t *testing.T) {
	tests := []struct {
		name       string
		sync       docsbot.SyncStatus
		reason     string
		wantText   string
		wantButton string
	}{
		{name: "failed", sync: docsbot.SyncFailed, wantText: telegram.TextStorageSyncFailed, wantButton: telegram.BtnRetryBackup},
		{name: "revoked", sync: docsbot.SyncFailed, reason: docsbot.SyncReasonRevoked, wantText: telegram.TextStorageSyncRevoked, wantButton: telegram.BtnReconnect},
		{name: "missing", sync: docsbot.SyncMissing, wantText: telegram.TextStorageMissing, wantButton: telegram.BtnReupload},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifierEnv(t)
			doc := e.repo.Seed(docsbot.Document{
				UserID: docsbottest.TestUserID, Title: "Doc", Category: docsbot.CatPassport,
				BackupStatus: docsbot.BackupBackedUp, DriveFileID: "F1", SyncStatus: tt.sync, SyncReason: tt.reason,
			})

			require.NoError(t, e.n.SyncOutcome(t.Context(), docsbottest.TestUserID, doc.ID))

			sends := e.api.callsOf("sendMessage")
			require.Len(t, sends, 1)
			require.Contains(t, sends[0].text, tt.wantText)
			require.Contains(t, sends[0].markup, tt.wantButton)
		})
	}
}

func TestNotifier_SyncOutcome_unknownDocumentSendsNothing(t *testing.T) {
	e := newNotifierEnv(t)

	require.NoError(t, e.n.SyncOutcome(t.Context(), docsbottest.TestUserID, "missing"))

	require.Empty(t, e.api.callsOf("sendMessage"))
}

func TestNotifier_RemoteKept(t *testing.T) {
	tests := []struct {
		name      string
		blocked   bool
		wantSends int
	}{
		{name: "tells the file stayed in Drive", wantSends: 1},
		{name: "blocked bot is not an error", blocked: true, wantSends: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifierEnv(t)
			e.api.blocked = tt.blocked

			require.NoError(t, e.n.RemoteKept(t.Context(), docsbottest.TestUserID))

			sends := e.api.callsOf("sendMessage")
			require.Len(t, sends, tt.wantSends)
			require.Equal(t, telegram.TextDeletedRemoteKept, sends[0].text)
		})
	}
}

func TestNotifier_RemoteKept_unknownChatIsAnError(t *testing.T) {
	e := newNotifierEnv(t)
	n := telegram.NewNotifier(e.bot, failingLookup{err: errors.New("boom")}, docsbottest.FakeAuth{}, e.svc, slog.New(slog.DiscardHandler))

	require.Error(t, n.RemoteKept(t.Context(), docsbottest.TestUserID))
	require.Empty(t, e.api.callsOf("sendMessage"))
}
