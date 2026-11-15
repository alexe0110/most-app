package telegram_test

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestCategory_missingDocumentSaysGone(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:cat:missing:"+string(docsbot.CatOther)))

	require.Contains(t, sent.edits[len(sent.edits)-1], telegram.TextGone)
}

func TestReceive_asksCategory(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	update := &models.Update{Message: &models.Message{
		From:     &models.User{ID: 100},
		Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "Паспорт.pdf"},
	}}
	b.ProcessUpdate(ctx, update)

	require.True(t, has(sent.texts, "категори"), "sent %q", sent.texts)
}

func TestReceive_duplicateShowsFolderButton(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	repo.Folders = map[docsbot.Category]string{docsbot.CatOther: "fld-1"}
	repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatOther,
		Title: "Паспорт", TgFileID: "f1", TgFileUniqueID: "u1",
	})
	b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		From:     &models.User{ID: 100},
		Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "Паспорт.pdf"},
	}})

	require.True(t, has(sent.texts, "Паспорт"), "sent %q", sent.texts)
	require.True(t, has(sent.markups, "https://drive.google.com/drive/folders/fld-1"), "markups %q", sent.markups)
}

func TestReceive_duplicateBackedUpLinksFileOnly(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	repo.Folders = map[docsbot.Category]string{docsbot.CatOther: "fld-1"}
	repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatOther,
		Title: "Паспорт", TgFileID: "f1", TgFileUniqueID: "u1",
		BackupStatus: docsbot.BackupBackedUp, DriveFileID: "drive-1",
	})
	b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		From:     &models.User{ID: 100},
		Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "Паспорт.pdf"},
	}})

	require.True(t, has(sent.markups, "https://drive.google.com/file/d/drive-1/view"), "markups %q", sent.markups)
	require.False(t, has(sent.markups, "/drive/folders/"), "markups %q", sent.markups)
}

func TestChooseCategory_savesAndShowsCard(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	doc := repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, Kind: docsbot.KindDocument,
		Title: "Паспорт", TgFileID: "f1", TgFileUniqueID: "u1", BackupStatus: docsbot.BackupNone,
	})
	update := &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   "cb1",
		From: models.User{ID: 100},
		Data: "d:cat:" + string(doc.ID) + ":" + string(docsbot.CatOther),
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: 7, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate}},
		},
	}}
	b.ProcessUpdate(ctx, update)

	require.Len(t, sent.texts, 2, "the first-document hint and the Drive nudge: %q", sent.texts)
	require.Contains(t, sent.texts[0], "Чтобы найти его потом", "the hint comes before the nudge")
	require.True(t, has(sent.edits, "🗂 "), "edits %q", sent.edits)
	require.False(t, has(sent.edits, "✅"), "edits %q", sent.edits)
	require.Equal(t, docsbot.StatusSaved, repo.Docs[doc.ID].Status)
	require.Equal(t, docsbot.CatOther, repo.Docs[doc.ID].Category)
}

func TestReceive_photoPickerOffersConvert(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		ID:    1,
		From:  &models.User{ID: 100},
		Chat:  models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Photo: []models.PhotoSize{{FileID: "large", FileUniqueID: "u1", Width: 1280, Height: 960}},
	}})

	require.Len(t, repo.Docs, 1)
	for id := range repo.Docs {
		require.Contains(t, sent.markups[0], "conv:doc:"+string(id))
		require.Contains(t, sent.markups[0], "d:cat:"+string(id)+":passport", "category buttons stay")
	}
}

func TestReceive_pdfPickerOffersActions(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		ID:       9,
		From:     &models.User{ID: 100},
		Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "pdf-x", FileUniqueID: "u-pdf-x", MimeType: "application/pdf", FileSize: 100},
	}})

	require.Len(t, repo.Docs, 1)
	require.Contains(t, sent.markups[0], "conv:doc:")
	require.Contains(t, sent.markups[0], "Действия с PDF")
}

// firstDocHints counts the "how to find it" hints among sent messages.
func firstDocHints(c *capture) int {
	n := 0
	for _, text := range c.snapshot(&c.texts) {
		if strings.Contains(text, "Чтобы найти его потом") {
			n++
		}
	}
	return n
}

func TestChooseCategory_firstDocHint(t *testing.T) {
	tests := []struct {
		name      string
		connected bool
		saved     int // documents saved before the pick
		taps      int
		want      int
	}{
		{"first document, drive off", false, 0, 1, 1},
		{"first document, drive on", true, 0, 1, 1},
		{"second document, drive off", false, 1, 1, 0},
		{"second document, drive on", true, 1, 1, 0},
		{"double tap on the first document", false, 0, 2, 1},
		{"double tap on the first document, drive on", true, 0, 2, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: tt.connected, ExistsRes: true})
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			for i := range tt.saved {
				seedPassport(repo, fmt.Sprintf("old%d", i), fmt.Sprintf("old%d", i))
			}
			id := seedPending(repo, 1)[0]

			for range tt.taps {
				b.ProcessUpdate(ctx, dataCallbackMsg(11, "d:cat:"+string(id)+":"+string(docsbot.CatPassport)))
			}

			require.Equal(t, tt.want, firstDocHints(sent), "texts %q", sent.texts)
			if tt.want > 0 {
				want := "Сохранил. Чтобы найти его потом, напиши слово из названия — или открой /list."
				require.Contains(t, sent.texts, want)
			}
		})
	}
}

func TestChooseCategory_firstDocHint_secondDocumentAfterTheFirstOne(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	for i, id := range seedPending(repo, 2) {
		b.ProcessUpdate(ctx, dataCallbackMsg(11+i, "d:cat:"+string(id)+":"+string(docsbot.CatPassport)))
		require.Equal(t, 1, firstDocHints(sent), "after document %d", i+1)
	}
}

func TestAlbum_firstDocHint(t *testing.T) {
	tests := []struct {
		name      string
		connected bool
		saved     int
		want      int
	}{
		{"album is the first documents", false, 0, 1},
		{"album is the first documents, drive on", true, 0, 1},
		{"album after an existing document", false, 1, 0},
		{"album after an existing document, drive on", true, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft, send, sent, repo := albumBot(t, docsbottest.FakeDrive{IsConnected: tt.connected, ExistsRes: true})
			for i := range tt.saved {
				seedPassport(repo, fmt.Sprintf("old%d", i), fmt.Sprintf("old%d", i))
			}
			for _, uid := range []string{"p1", "p2", "p3"} {
				send(albumPhoto("alb", uid))
			}
			ft.Fire()
			var anchor docsbot.DocumentID
			for id, d := range repo.Docs {
				if d.TgFileUniqueID == "p1" {
					anchor = id
				}
			}
			tap := dataCallback("d:ga:" + string(anchor) + ":" + string(docsbot.CatReceipts))

			send(tap)
			send(tap)

			require.Equal(t, tt.want, firstDocHints(sent), "one hint per album, none for a repeated tap: %q", sent.texts)
		})
	}
}
