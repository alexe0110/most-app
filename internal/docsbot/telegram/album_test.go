package telegram_test

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// fakeTimer is a manual telegram.AfterFunc: nothing fires until the test says so.
type fakeTimer struct {
	mu     sync.Mutex
	timers []*fakeTimerEntry
}

type fakeTimerEntry struct {
	f       func()
	stopped bool
}

func (ft *fakeTimer) after(_ time.Duration, f func()) func() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	e := &fakeTimerEntry{f: f}
	ft.timers = append(ft.timers, e)
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		was := !e.stopped
		e.stopped = true
		return was
	}
}

// fire runs every timer that was not stopped, once.
func (ft *fakeTimer) fire() {
	ft.mu.Lock()
	var run []func()
	for _, e := range ft.timers {
		if !e.stopped {
			e.stopped = true
			run = append(run, e.f)
		}
	}
	ft.mu.Unlock()
	for _, f := range run {
		f()
	}
}

func albumPhoto(group, uniqueID string) *models.Update {
	return &models.Update{Message: &models.Message{
		From:         &models.User{ID: 100},
		Chat:         models.Chat{ID: 100, Type: models.ChatTypePrivate},
		MediaGroupID: group,
		Photo:        []models.PhotoSize{{FileID: "f-" + uniqueID, FileUniqueID: uniqueID, FileSize: 1000}},
	}}
}

func albumBot(t *testing.T, drive fakeDrive) (*fakeTimer, func(*models.Update), *capture, *fakeRepo) {
	t.Helper()
	ft := &fakeTimer{}
	b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), drive, telegram.WithAlbumTimer(ft.after))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	send := func(u *models.Update) { b.ProcessUpdate(ctx, u) }
	return ft, send, sent, repo
}

func pendingCount(repo *fakeRepo, group string) int {
	n := 0
	for _, d := range repo.docs {
		if d.MediaGroupID == group && d.Status == docsbot.StatusPending {
			n++
		}
	}
	return n
}

func TestAlbum_threePhotosThreeDocsOnePicker(t *testing.T) {
	ft, send, sent, repo := albumBot(t, fakeDrive{})

	for _, uid := range []string{"p1", "p2", "p3"} {
		send(albumPhoto("alb", uid))
	}
	require.Equal(t, 3, pendingCount(repo, "alb"), "each photo is its own document")
	require.Empty(t, sent.texts, "no picker before the debounce elapses")

	ft.fire()

	// Intentionally changed by #80: an all-image album first offers one PDF or
	// file-by-file; the category picker follows "Сохранить по одному".
	require.Len(t, sent.texts, 1, "exactly one message for the album: %q", sent.texts)
	require.Contains(t, sent.texts[0], "3 изображения")
	require.Contains(t, sent.markups[0], "conv:alb:")
	require.Contains(t, sent.markups[0], "d:gpk:")
	require.NotContains(t, sent.texts[0], "Уже были сохранены")
}

func TestAlbum_callbackDataFitsTelegramLimit(t *testing.T) {
	ft, send, sent, _ := albumBot(t, fakeDrive{})
	send(albumPhoto("alb", "p1"))
	ft.fire()

	for _, cat := range docsbot.AllCategories {
		// The real ids are 36-char UUIDs; the longest slug is the worst case.
		data := "d:ga:" + testUserID + ":" + string(cat)
		require.LessOrEqual(t, len(data), 64, data)
	}
	require.True(t, strings.Contains(sent.markups[0], "d:ga:"))
}

func TestAlbum_withDuplicateReportsCount(t *testing.T) {
	ft, send, sent, repo := albumBot(t, fakeDrive{})
	repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindPhoto, Category: docsbot.CatOther,
		TgFileID: "f-p2", TgFileUniqueID: "p2", BackupStatus: docsbot.BackupNone,
	})

	for _, uid := range []string{"p1", "p2", "p3"} {
		send(albumPhoto("alb", uid))
	}
	ft.fire()

	require.Equal(t, 2, pendingCount(repo, "alb"), "the duplicate creates no document")
	require.Len(t, sent.texts, 1)
	require.Contains(t, sent.texts[0], "2 изображения") // #80: the PDF choice replaces the picker
	require.Contains(t, sent.texts[0], "Уже были сохранены: 1")
}

func TestAlbum_pickerSavesWholeAlbum(t *testing.T) {
	ft, send, sent, repo := albumBot(t, fakeDrive{})
	var first docsbot.DocumentID
	for _, uid := range []string{"p1", "p2", "p3"} {
		send(albumPhoto("alb", uid))
	}
	ft.fire()
	for id, d := range repo.docs {
		if d.TgFileUniqueID == "p1" {
			first = id
		}
	}
	require.NotEmpty(t, first)

	send(dataCallback(100, "d:ga:"+string(first)+":"+string(docsbot.CatReceipts)))

	require.Zero(t, pendingCount(repo, "alb"))
	require.Len(t, repo.savedFor(testUserID), 3)
	require.True(t, has(sent.edits, "Сохранил 3 файла"), "edits %q", sent.edits)
}

func TestAlbum_twoAlbumsGetTwoPickers(t *testing.T) {
	ft, send, sent, _ := albumBot(t, fakeDrive{})
	send(albumPhoto("a", "a1"))
	send(albumPhoto("b", "b1"))
	ft.fire()
	require.Len(t, sent.texts, 2)
}

func TestAlbum_limitRefusalIsShownOnce(t *testing.T) {
	ft, send, sent, repo := albumBot(t, fakeDrive{})
	for i := range 20 {
		repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusPending, TgFileUniqueID: "old" + string(rune('a'+i)), CreatedAt: time.Now().Add(-time.Hour)})
	}

	for _, uid := range []string{"p1", "p2", "p3"} {
		send(albumPhoto("alb", uid))
	}
	ft.fire()

	require.Len(t, sent.texts, 1)
	require.Contains(t, sent.texts[0], "Сначала выбери категорию")
}

func TestUnsupportedMedia_repliesForEveryType(t *testing.T) {
	tests := []struct {
		name string
		msg  models.Message
	}{
		{"voice", models.Message{Voice: &models.Voice{FileID: "v"}}},
		{"video", models.Message{Video: &models.Video{FileID: "v"}}},
		{"video note", models.Message{VideoNote: &models.VideoNote{FileID: "v"}}},
		{"audio", models.Message{Audio: &models.Audio{FileID: "a"}}},
		{"sticker", models.Message{Sticker: &models.Sticker{FileID: "s"}}},
		{"animation", models.Message{Animation: &models.Animation{FileID: "g"}, Document: &models.Document{FileID: "g"}}},
		{"contact", models.Message{Contact: &models.Contact{PhoneNumber: "1"}}},
		{"location", models.Message{Location: &models.Location{Latitude: 1}}},
		{"poll", models.Message{Poll: &models.Poll{ID: "p"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			msg := tt.msg
			msg.From = &models.User{ID: 100}
			msg.Chat = models.Chat{ID: 100, Type: models.ChatTypePrivate}

			b.ProcessUpdate(tg.ContextWithUserID(t.Context(), testUserID), &models.Update{Message: &msg})

			require.Equal(t, []string{"Пока принимаю фото и файлы. Видео/голосовые — отправь как файл"}, sent.texts)
			require.Empty(t, repo.docs, "nothing is stored")
		})
	}
}

func TestReceive_limitRepliesAreRendered(t *testing.T) {
	tests := []struct {
		name  string
		seed  func(*fakeRepo)
		wants string
	}{
		{"too many pending", func(r *fakeRepo) {
			for i := range 20 {
				r.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusPending, TgFileUniqueID: "o" + string(rune('a'+i)), CreatedAt: time.Now().Add(-time.Hour)})
			}
		}, "Сначала выбери категорию для предыдущих файлов"},
		{"rate limited", func(r *fakeRepo) {
			for i := range 30 {
				r.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, TgFileUniqueID: "o" + string(rune('a'+i)), CreatedAt: time.Now()})
			}
		}, "Слишком много файлов"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			tt.seed(repo)
			before := len(repo.docs)

			b.ProcessUpdate(tg.ContextWithUserID(t.Context(), testUserID), &models.Update{Message: &models.Message{
				From:     &models.User{ID: 100},
				Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
				Document: &models.Document{FileID: "f", FileUniqueID: "new", FileName: "x.pdf"},
			}})

			require.Len(t, sent.texts, 1)
			require.Contains(t, sent.texts[0], tt.wants)
			require.Len(t, repo.docs, before)
		})
	}
}

func TestChooseCategory_tooLargeStaysInTelegram(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusPending, Kind: docsbot.KindDocument,
		Title: "Видео.mp4", TgFileID: "f", TgFileUniqueID: "u", FileSize: docsbot.MaxDriveBytes + 1,
	})

	b.ProcessUpdate(tg.ContextWithUserID(t.Context(), testUserID),
		dataCallback(100, "d:cat:"+string(doc.ID)+":"+string(docsbot.CatOther)))

	require.Equal(t, docsbot.BackupTooLarge, repo.docs[doc.ID].BackupStatus)
	require.True(t, has(sent.edits, "Слишком большой для Drive"), "edits %q", sent.edits)
}

func albumDocument(group, uniqueID, name string) *models.Update {
	return &models.Update{Message: &models.Message{
		From:         &models.User{ID: 100},
		Chat:         models.Chat{ID: 100, Type: models.ChatTypePrivate},
		MediaGroupID: group,
		Document:     &models.Document{FileID: "f-" + uniqueID, FileUniqueID: uniqueID, FileName: name, FileSize: 1000},
	}}
}

func TestAlbum_allDuplicatesShowNoPicker(t *testing.T) {
	ft, send, sent, repo := albumBot(t, fakeDrive{})
	for _, uid := range []string{"p1", "p2"} {
		repo.seed(docsbot.Document{
			UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindPhoto, Category: docsbot.CatOther,
			TgFileID: "f-" + uid, TgFileUniqueID: uid, BackupStatus: docsbot.BackupNone,
		})
	}

	send(albumPhoto("alb", "p1"))
	send(albumPhoto("alb", "p2"))
	ft.fire()

	require.Zero(t, pendingCount(repo, "alb"))
	require.Len(t, sent.texts, 1, "one summary, no picker: %q", sent.texts)
	require.Contains(t, sent.texts[0], "Уже были сохранены: 2")
	require.NotContains(t, sent.markups[0], "d:ga:")
}

func TestAlbum_photosAndDocumentsGetOnePicker(t *testing.T) {
	ft, send, sent, repo := albumBot(t, fakeDrive{})

	send(albumPhoto("alb", "p1"))
	send(albumDocument("alb", "d1", "scan.pdf"))
	send(albumDocument("alb", "d2", "scan2.pdf"))
	ft.fire()

	require.Equal(t, 3, pendingCount(repo, "alb"))
	require.Len(t, sent.texts, 1)
	require.Contains(t, sent.markups[0], "d:ga:")
}

func TestAlbum_pickerAfterRestartStillSavesAlbum(t *testing.T) {
	// A fresh bot (no in-memory album state) with the album already in the DB:
	// the picker button alone must save every pending photo.
	_, send, _, repo := albumBot(t, fakeDrive{})
	var anchor docsbot.DocumentID
	for _, uid := range []string{"p1", "p2"} {
		d := repo.seed(docsbot.Document{
			UserID: testUserID, Status: docsbot.StatusPending, Kind: docsbot.KindPhoto, MediaGroupID: "alb",
			TgFileID: "f-" + uid, TgFileUniqueID: uid, CreatedAt: time.Now(),
		})
		anchor = d.ID
	}

	send(dataCallback(100, "d:ga:"+string(anchor)+":"+string(docsbot.CatReceipts)))

	require.Zero(t, pendingCount(repo, "alb"))
	require.Len(t, repo.savedFor(testUserID), 2)
}

func TestAlbum_pickerDoubleTapOpensOneStatus(t *testing.T) {
	_, send, sent, repo := albumBot(t, fakeDrive{connected: true})
	var anchor docsbot.DocumentID
	for _, uid := range []string{"p1", "p2"} {
		d := repo.seed(docsbot.Document{
			UserID: testUserID, Status: docsbot.StatusPending, Kind: docsbot.KindPhoto, MediaGroupID: "alb",
			TgFileID: "f-" + uid, TgFileUniqueID: uid, CreatedAt: time.Now(),
		})
		anchor = d.ID
	}
	tap := dataCallback(100, "d:ga:"+string(anchor)+":"+string(docsbot.CatReceipts))

	send(tap)
	edits := len(sent.edits)
	send(tap)

	require.Len(t, repo.status.msgs, 1, "one status message for the album")
	require.Len(t, sent.edits, edits, "the batch is not redrawn by the second tap")
	require.Len(t, repo.savedFor(testUserID), 2)
}
