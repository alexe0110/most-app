//go:build integration

package jobs_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/jobs"
)

func TestChooseCategory_savesAndQueuesInOneTransaction(t *testing.T) {
	tests := []struct {
		name       string
		connected  bool
		size       int64
		wantBackup string
		wantJobs   int
	}{
		{"drive connected", true, 1 << 20, "pending", 1},
		{"drive not connected", false, 1 << 20, "none", 0},
		{"too large for drive stays in telegram", true, docsbot.MaxDriveBytes + 1, "too_large", 0},
		{"exactly the limit is uploaded", true, docsbot.MaxDriveBytes, "pending", 1},
		{"size unknown is uploaded", true, 0, "pending", 1},
	}
	shared := newTxEnv(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := shared.fresh(t)
			svc := e.service(e.jq, tt.connected)
			doc := e.receive(t, svc, "a", tt.size)

			saved, err := svc.ChooseCategory(t.Context(), e.user, doc.ID, docsbot.CatPassport)

			require.NoError(t, err)
			require.False(t, saved.Already)
			got, title := e.state(t, doc.ID)
			require.Equal(t, state{"saved", "passport", tt.wantBackup}, got)
			require.NotEmpty(t, title, "the default title is written with the choice")
			require.Equal(t, tt.wantJobs, e.jobCount(t, ""))
			require.Equal(t, tt.wantJobs, e.jobCount(t, jobs.UploadDocumentArgs{}.Kind()))

			again, err := svc.ChooseCategory(t.Context(), e.user, doc.ID, docsbot.CatMedical)
			require.NoError(t, err)
			require.True(t, again.Already, "second tap")
			got, _ = e.state(t, doc.ID)
			require.Equal(t, state{"saved", "passport", tt.wantBackup}, got, "the second tap changes nothing")
			require.Equal(t, tt.wantJobs, e.jobCount(t, ""))
		})
	}
}

func TestChooseCategory_parallelTapsQueueOneUpload(t *testing.T) {
	e := newTxEnv(t)
	svc := e.service(e.jq, true)
	doc := e.receive(t, svc, "a", 10)

	const taps = 8
	var (
		wg    sync.WaitGroup
		wins  atomic.Int32
		start = make(chan struct{})
	)
	for range taps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			saved, err := svc.ChooseCategory(t.Context(), e.user, doc.ID, docsbot.CatPassport)
			assert.NoError(t, err)
			if !saved.Already {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	require.EqualValues(t, 1, wins.Load())
	require.Equal(t, 1, e.jobCount(t, ""))
	got, _ := e.state(t, doc.ID)
	require.Equal(t, state{"saved", "passport", "pending"}, got)
}

// The upload job fails after the category, title and backup status were written:
// the document must be back to pending with no trace, so the next tap works.
func TestChooseCategory_failureBetweenStepsLeavesDocumentPending(t *testing.T) {
	e := newTxEnv(t)
	boom := errors.New("queue down")
	failing := e.service(failAfterInsert{JobInserter: e.jq, err: boom}, true)
	doc := e.receive(t, failing, "a", 10)
	before := e.snapshot(t, doc.ID)

	_, err := failing.ChooseCategory(t.Context(), e.user, doc.ID, docsbot.CatPassport)

	require.ErrorIs(t, err, boom)
	got, title := e.state(t, doc.ID)
	require.Equal(t, state{"pending", "", "none"}, got, "no saved-without-job")
	require.Empty(t, title)
	require.Equal(t, before, e.snapshot(t, doc.ID))
	require.Zero(t, e.jobCount(t, ""))

	healthy := e.service(e.jq, true)
	saved, err := healthy.ChooseCategory(t.Context(), e.user, doc.ID, docsbot.CatPassport)
	require.NoError(t, err)
	require.False(t, saved.Already)
	got, _ = e.state(t, doc.ID)
	require.Equal(t, state{"saved", "passport", "pending"}, got)
	require.Equal(t, 1, e.jobCount(t, ""))
}

func TestSaveChosen_guards(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		foreign   bool // act as another user
		cancelled bool
		backup    docsbot.BackupStatus
		wantSaved bool
		wantErr   bool
		wantState state
		wantJobs  int
	}{
		{"saved without drive queues nothing", "pending", false, false, docsbot.BackupNone, true, false, state{"saved", "passport", "none"}, 0},
		{"too large queues nothing", "pending", false, false, docsbot.BackupTooLarge, true, false, state{"saved", "passport", "too_large"}, 0},
		{"already saved is a no-op", "saved", false, false, docsbot.BackupPending, false, false, state{"saved", "other", "none"}, 0},
		{"another user's document is a no-op", "pending", true, false, docsbot.BackupPending, false, false, state{"pending", "", "none"}, 0},
		{"cancelled context changes nothing", "pending", false, true, docsbot.BackupPending, false, true, state{"pending", "", "none"}, 0},
		{"unsupported backup status rolls back", "pending", false, false, docsbot.BackupFailed, false, true, state{"pending", "", "none"}, 0},
	}
	shared := newTxEnv(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := shared.fresh(t)
			id := e.seed(t, tt.status, "none", "ok")
			who := e.user
			if tt.foreign {
				who = shared.fresh(t).user
			}
			ctx := t.Context()
			if tt.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			saved, err := e.docs(e.jq).SaveChosen(ctx, docsbot.SaveChoice{User: who, ID: id, Category: docsbot.CatPassport, Backup: tt.backup})

			require.Equal(t, tt.wantErr, err != nil)
			require.Equal(t, tt.wantSaved, saved)
			got, _ := e.state(t, id)
			require.Equal(t, tt.wantState, got)
			require.Equal(t, tt.wantJobs, e.jobCount(t, ""))
		})
	}
}
