package telegram_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func albumPhoto(group, uniqueID string) *models.Update {
	return &models.Update{Message: &models.Message{
		From:         &models.User{ID: 100},
		Chat:         models.Chat{ID: 100, Type: models.ChatTypePrivate},
		MediaGroupID: group,
		Photo:        []models.PhotoSize{{FileID: "f-" + uniqueID, FileUniqueID: uniqueID, FileSize: 1000}},
	}}
}

func albumBot(t *testing.T, drive docsbottest.FakeDrive) (*docsbottest.FakeTimer, func(*models.Update), *capture, *docsbottest.FakeRepo) {
	t.Helper()
	ft := &docsbottest.FakeTimer{}
	b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), drive, telegram.WithAlbumTimer(ft.After))
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	send := func(u *models.Update) { b.ProcessUpdate(ctx, u) }
	return ft, send, sent, repo
}

// pendingCount counts pending documents of the test album "alb".
func pendingCount(repo *docsbottest.FakeRepo) int {
	n := 0
	for _, d := range repo.Docs {
		if d.MediaGroupID == "alb" && d.Status == docsbot.StatusPending {
			n++
		}
	}
	return n
}

func TestAlbum_threePhotosThreeDocsOnePicker(t *testing.T) {
	ft, send, sent, repo := albumBot(t, docsbottest.FakeDrive{})

	for _, uid := range []string{"p1", "p2", "p3"} {
		send(albumPhoto("alb", uid))
	}
	require.Equal(t, 3, pendingCount(repo), "each photo is its own document")
	require.Empty(t, sent.texts, "no picker before the debounce elapses")

	ft.Fire()

	// Intentionally changed: an all-image album first offers one PDF or
	// file-by-file; the category picker follows "Сохранить по одному".
	require.Len(t, sent.texts, 1, "exactly one message for the album: %q", sent.texts)
	require.Contains(t, sent.texts[0], "3 изображения")
	require.Contains(t, sent.markups[0], "conv:alb:")
	require.Contains(t, sent.markups[0], "d:gpk:")
	require.NotContains(t, sent.texts[0], "Уже есть у тебя")
}

func TestAlbum_callbackDataFitsTelegramLimit(t *testing.T) {
	ft, send, sent, _ := albumBot(t, docsbottest.FakeDrive{})
	send(albumPhoto("alb", "p1"))
	ft.Fire()

	for _, cat := range docsbot.AllCategories {
		// The real ids are 36-char UUIDs; the longest slug is the worst case.
		data := "d:ga:" + docsbottest.TestUserID + ":" + string(cat)
		require.LessOrEqual(t, len(data), 64, data)
	}
	require.True(t, strings.Contains(sent.markups[0], "d:ga:"))
}

func TestAlbum_withDuplicateReportsCount(t *testing.T) {
	ft, send, sent, repo := albumBot(t, docsbottest.FakeDrive{})
	repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindPhoto, Category: docsbot.CatOther,
		TgFileID: "f-p2", TgFileUniqueID: "p2", BackupStatus: docsbot.BackupNone,
	})

	for _, uid := range []string{"p1", "p2", "p3"} {
		send(albumPhoto("alb", uid))
	}
	ft.Fire()

	require.Equal(t, 2, pendingCount(repo), "the duplicate creates no document")
	require.Len(t, sent.texts, 1)
	require.Contains(t, sent.texts[0], "2 изображения") // the PDF choice replaces the picker
	require.Contains(t, sent.texts[0], fmt.Sprintf(telegram.TextAlbumDuplicates, 1))
}

func TestAlbum_pickerSavesWholeAlbum(t *testing.T) {
	ft, send, sent, repo := albumBot(t, docsbottest.FakeDrive{})
	var first docsbot.DocumentID
	for _, uid := range []string{"p1", "p2", "p3"} {
		send(albumPhoto("alb", uid))
	}
	ft.Fire()
	for id, d := range repo.Docs {
		if d.TgFileUniqueID == "p1" {
			first = id
		}
	}
	require.NotEmpty(t, first)

	send(dataCallback("d:ga:" + string(first) + ":" + string(docsbot.CatReceipts)))

	require.Zero(t, pendingCount(repo))
	require.Len(t, repo.SavedFor(docsbottest.TestUserID), 3)
	require.True(t, has(sent.edits, "Сохранил 3 документа"), "edits %q", sent.edits)
}

func TestAlbum_twoAlbumsGetTwoPickers(t *testing.T) {
	ft, send, sent, _ := albumBot(t, docsbottest.FakeDrive{})
	send(albumPhoto("a", "a1"))
	send(albumPhoto("b", "b1"))
	ft.Fire()
	require.Len(t, sent.texts, 2)
}

func TestAlbum_limitRefusalIsShownOnce(t *testing.T) {
	ft, send, sent, repo := albumBot(t, docsbottest.FakeDrive{})
	for i := range 20 {
		repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, TgFileUniqueID: "old" + string(rune('a'+i)), CreatedAt: time.Now().Add(-time.Hour)})
	}

	for _, uid := range []string{"p1", "p2", "p3"} {
		send(albumPhoto("alb", uid))
	}
	ft.Fire()

	require.Len(t, sent.texts, 1)
	require.Contains(t, sent.texts[0], "Этот файл я не сохранил: сначала выбери категорию")
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

			b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), &models.Update{Message: &msg})

			require.Equal(t, []string{telegram.TextUnsupported}, sent.texts)
			require.Empty(t, repo.Docs, "nothing is stored")
		})
	}
}

func TestReceive_limitRepliesAreRendered(t *testing.T) {
	tests := []struct {
		name  string
		seed  func(*docsbottest.FakeRepo)
		wants string
	}{
		{"too many pending", func(r *docsbottest.FakeRepo) {
			for i := range 20 {
				r.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, TgFileUniqueID: "o" + string(rune('a'+i)), CreatedAt: time.Now().Add(-time.Hour)})
			}
		}, "Этот файл я не сохранил: сначала выбери категорию"},
		{"rate limited", func(r *docsbottest.FakeRepo) {
			for i := range 30 {
				r.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, TgFileUniqueID: "o" + string(rune('a'+i)), CreatedAt: time.Now()})
			}
		}, "Часть файлов я не сохранил"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			tt.seed(repo)
			before := len(repo.Docs)

			b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), &models.Update{Message: &models.Message{
				From:     &models.User{ID: 100},
				Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
				Document: &models.Document{FileID: "f", FileUniqueID: "new", FileName: "x.pdf"},
			}})

			require.Len(t, sent.texts, 1)
			require.Contains(t, sent.texts[0], tt.wants)
			require.Len(t, repo.Docs, before)
		})
	}
}

func TestChooseCategory_tooLargeStaysInTelegram(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	doc := repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, Kind: docsbot.KindDocument,
		Title: "Видео.mp4", TgFileID: "f", TgFileUniqueID: "u", FileSize: docsbot.MaxDriveBytes + 1,
	})

	b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID),
		dataCallback("d:cat:"+string(doc.ID)+":"+string(docsbot.CatOther)))

	require.Equal(t, docsbot.BackupTooLarge, repo.Docs[doc.ID].BackupStatus)
	require.True(t, has(sent.edits, fmt.Sprintf(telegram.TextStorageTooLarge, 20)), "edits %q", sent.edits)
}

// captioned puts a caption on an album element, as Telegram does for one of them.
func captioned(u *models.Update, caption string) *models.Update {
	u.Message.Caption = caption
	return u
}

// albumTitles lists the titles of the pending documents of album "alb", in arrival order.
func albumTitles(repo *docsbottest.FakeRepo) []string {
	pending, _ := repo.PendingByMediaGroup(context.Background(), docsbottest.TestUserID, "alb")
	var titles []string
	for _, d := range pending {
		titles = append(titles, d.Title)
	}
	return titles
}

func TestAlbum_captionNamesEveryElement(t *testing.T) {
	tests := []struct {
		name string
		send []*models.Update
		want []string
	}{
		{
			"caption on a document, photos and documents mixed",
			[]*models.Update{
				albumPhoto("alb", "p1"),
				captioned(albumDocument("d1", "scan.pdf"), "\n Отпуск 2026\nвторая строка"),
				albumPhoto("alb", "p2"),
			},
			[]string{"Отпуск 2026 (1)", "Отпуск 2026 (2)", "Отпуск 2026 (3)"},
		},
		{
			"caption on the first photo",
			[]*models.Update{captioned(albumPhoto("alb", "p1"), "Чеки"), albumPhoto("alb", "p2")},
			[]string{"Чеки (1)", "Чеки (2)"},
		},
		{
			"no caption keeps the automatic titles",
			[]*models.Update{albumDocument("d1", "one.pdf"), albumDocument("d2", "two.pdf"), albumPhoto("alb", "p1")},
			[]string{"one", "two", ""},
		},
		{
			"whitespace caption is no caption",
			[]*models.Update{captioned(albumDocument("d1", "one.pdf"), " \n "), albumDocument("d2", "two.pdf")},
			[]string{"one", "two"},
		},
		{
			"a single captioned file has the plain caption, no number",
			[]*models.Update{captioned(albumPhoto("alb", "p1"), "Чек")},
			[]string{"Чек"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft, send, sent, repo := albumBot(t, docsbottest.FakeDrive{})
			for _, u := range tt.send {
				send(u)
			}
			ft.Fire()

			require.Equal(t, tt.want, albumTitles(repo))
			require.NotEmpty(t, sent.texts, "the picker is sent")
		})
	}
}

func TestAlbum_captionIsNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ft := &docsbottest.FakeTimer{}
	b, sent, repo := newBotOpts(t, log, docsbottest.FakeDrive{}, telegram.WithAlbumTimer(ft.After))
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, captioned(albumPhoto("alb", "p1"), "СекретнаяПодпись"))
	b.ProcessUpdate(ctx, albumPhoto("alb", "p2"))
	ft.Fire()
	require.Equal(t, []string{"СекретнаяПодпись (1)", "СекретнаяПодпись (2)"}, albumTitles(repo))
	require.NotEmpty(t, sent.texts)

	// A failing numbering is logged without the caption and does not stop the picker.
	repo.NumberErr = errors.New("db down")
	b.ProcessUpdate(ctx, captioned(albumPhoto("alb2", "p3"), "СекретнаяПодпись"))
	b.ProcessUpdate(ctx, albumPhoto("alb2", "p4"))
	before := len(sent.texts)
	ft.Fire()
	require.Greater(t, len(sent.texts), before, "picker still sent")
	require.Contains(t, buf.String(), "title album")
	require.NotContains(t, buf.String(), "СекретнаяПодпись")
}

func albumDocument(uniqueID, name string) *models.Update {
	return &models.Update{Message: &models.Message{
		From:         &models.User{ID: 100},
		Chat:         models.Chat{ID: 100, Type: models.ChatTypePrivate},
		MediaGroupID: "alb",
		Document:     &models.Document{FileID: "f-" + uniqueID, FileUniqueID: uniqueID, FileName: name, FileSize: 1000},
	}}
}

func TestAlbum_allDuplicatesShowNoPicker(t *testing.T) {
	ft, send, sent, repo := albumBot(t, docsbottest.FakeDrive{})
	for _, uid := range []string{"p1", "p2"} {
		repo.Seed(docsbot.Document{
			UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindPhoto, Category: docsbot.CatOther,
			TgFileID: "f-" + uid, TgFileUniqueID: uid, BackupStatus: docsbot.BackupNone,
		})
	}

	send(albumPhoto("alb", "p1"))
	send(albumPhoto("alb", "p2"))
	ft.Fire()

	require.Zero(t, pendingCount(repo))
	require.Len(t, sent.texts, 1, "one summary, no picker: %q", sent.texts)
	require.Contains(t, sent.texts[0], fmt.Sprintf(telegram.TextAlbumDuplicates, 2))
	require.NotContains(t, sent.markups[0], "d:ga:")
}

func TestAlbum_photosAndDocumentsGetOnePicker(t *testing.T) {
	ft, send, sent, repo := albumBot(t, docsbottest.FakeDrive{})

	send(albumPhoto("alb", "p1"))
	send(albumDocument("d1", "scan.pdf"))
	send(albumDocument("d2", "scan2.pdf"))
	ft.Fire()

	require.Equal(t, 3, pendingCount(repo))
	require.Len(t, sent.texts, 1)
	require.Contains(t, sent.markups[0], "d:ga:")
}

func TestAlbum_pickerAfterRestartStillSavesAlbum(t *testing.T) {
	// A fresh bot (no in-memory album state) with the album already in the DB:
	// the picker button alone must save every pending photo.
	_, send, _, repo := albumBot(t, docsbottest.FakeDrive{})
	var anchor docsbot.DocumentID
	for _, uid := range []string{"p1", "p2"} {
		d := repo.Seed(docsbot.Document{
			UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, Kind: docsbot.KindPhoto, MediaGroupID: "alb",
			TgFileID: "f-" + uid, TgFileUniqueID: uid, CreatedAt: time.Now(),
		})
		anchor = d.ID
	}

	send(dataCallback("d:ga:" + string(anchor) + ":" + string(docsbot.CatReceipts)))

	require.Zero(t, pendingCount(repo))
	require.Len(t, repo.SavedFor(docsbottest.TestUserID), 2)
}

func TestAlbum_pickerDoubleTapOpensOneStatus(t *testing.T) {
	_, send, sent, repo := albumBot(t, docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	var anchor docsbot.DocumentID
	for _, uid := range []string{"p1", "p2"} {
		d := repo.Seed(docsbot.Document{
			UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, Kind: docsbot.KindPhoto, MediaGroupID: "alb",
			TgFileID: "f-" + uid, TgFileUniqueID: uid, CreatedAt: time.Now(),
		})
		anchor = d.ID
	}
	tap := dataCallback("d:ga:" + string(anchor) + ":" + string(docsbot.CatReceipts))

	send(tap)
	edits := len(sent.edits)
	send(tap)

	require.Len(t, repo.Status.Msgs, 1, "one status message for the album")
	require.Len(t, sent.edits, edits, "the batch is not redrawn by the second tap")
	require.Len(t, repo.SavedFor(docsbottest.TestUserID), 2)
}

// The pending cap (20 files) keeps an album below the plural boundary of 21.
func TestAlbum_textsAgreeWithTheCount(t *testing.T) {
	tests := []struct {
		n          int
		wantImages string // the "one PDF or file by file" offer of an all-image album
		wantFiles  string // the category picker of an album with a document in it
		wantSaved  string // the summary after saving the album
	}{
		{2, "Альбом: 2 изображения. Что сделать?", "Выбери категорию для 2 файла:", "Сохранил 2 документа в «Чеки и гарантии»."},
		{5, "Альбом: 5 изображений. Что сделать?", "Выбери категорию для 5 файлов:", "Сохранил 5 документов в «Чеки и гарантии»."},
		{11, "Альбом: 11 изображений. Что сделать?", "Выбери категорию для 11 файлов:", "Сохранил 11 документов в «Чеки и гарантии»."},
		{12, "Альбом: 12 изображений. Что сделать?", "Выбери категорию для 12 файлов:", "Сохранил 12 документов в «Чеки и гарантии»."},
		{20, "Альбом: 20 изображений. Что сделать?", "Выбери категорию для 20 файлов:", "Сохранил 20 документов в «Чеки и гарантии»."},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.n), func(t *testing.T) {
			// Images only.
			ft, send, sent, _ := albumBot(t, docsbottest.FakeDrive{})
			for i := range tt.n {
				send(albumPhoto("alb", fmt.Sprintf("p%d", i)))
			}
			ft.Fire()
			require.Len(t, sent.texts, 1)
			require.Equal(t, tt.wantImages, sent.texts[0])

			// One document among the photos: no PDF offer, a plain category picker.
			ft, send, sent, repo := albumBot(t, docsbottest.FakeDrive{})
			for i := range tt.n - 1 {
				send(albumPhoto("alb", fmt.Sprintf("p%d", i)))
			}
			send(albumDocument("d1", "scan.pdf"))
			ft.Fire()
			require.Len(t, sent.texts, 1)
			require.Equal(t, tt.wantFiles, sent.texts[0])

			var anchor docsbot.DocumentID
			for id := range repo.Docs {
				anchor = id
				break
			}
			send(dataCallback("d:ga:" + string(anchor) + ":" + string(docsbot.CatReceipts)))
			require.Contains(t, sent.edits, tt.wantSaved)
		})
	}
}
