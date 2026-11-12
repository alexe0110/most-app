package docsbot_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// trueDrive reports the file as present and runs onCheck during the lookup.
type trueDrive struct {
	fakeDrive
	onCheck func()
}

func (d *trueDrive) FileExists(context.Context, docsbot.UserID, string) (bool, error) {
	d.onCheck()
	return true, nil
}

func TestService_OpenCard_missingToOkDoesNotOverwritePendingSync(t *testing.T) {
	// A rename queued a sync while the existence check was in flight: the
	// conditional missing -> ok update must lose to the newer pending state.
	repo := newFakeRepo()
	doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing })
	drive := &trueDrive{fakeDrive: fakeDrive{connected: true}, onCheck: func() {
		_ = repo.SetSyncStatus(context.Background(), user, doc.ID, docsbot.SyncPending, "")
	}}
	svc := docsbot.NewService(repo, drive, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	card, err := svc.OpenCard(t.Context(), user, doc.ID)

	require.NoError(t, err)
	require.Equal(t, docsbot.SyncMissing, card.Doc.SyncStatus, "card shows what was read")
	require.Equal(t, docsbot.SyncPending, repo.docs[doc.ID].SyncStatus, "row keeps the newer state")
}

func TestService_OpenCard_cancelledContextLeavesCardUnchanged(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, nil)
	ctx, cancel := context.WithCancel(t.Context())
	drive := &racingDrive{fakeDrive: fakeDrive{connected: true}, onCheck: cancel} // reports "deleted" after cancel
	svc := docsbot.NewService(repo, drive, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	// The drive says "gone" but the caller is already cancelled: whatever the
	// service decides, it must not panic and must return the card.
	card, err := svc.OpenCard(ctx, user, doc.ID)

	require.NoError(t, err)
	require.Equal(t, doc.ID, card.Doc.ID)
}

func TestTrimExt_ambiguousTails(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2026.11", "2026.11"},     // digits only: not an extension
		{"Акт 12.05", "Акт 12.05"}, // date-like tail
		{"scan.final", "scan"},     // 5 letters look like an extension (documented heuristic)
		{"a.b.c.pdf", "a.b.c"},     // only the last one goes
		{"Акт.2026а", "Акт.2026а"}, // non-ASCII tail
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			require.Equal(t, tt.want, docsbot.TrimExt(tt.in))
		})
	}
}
