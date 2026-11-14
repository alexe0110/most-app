package telegram_test

import (
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestCategory_missingDocumentSaysGone(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:cat:missing:"+string(docsbot.CatOther)))

	require.Contains(t, sent.edits[len(sent.edits)-1], "Этого документа уже нет")
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

	require.False(t, has(sent.texts, "Сохранил"), "the picker becomes the card, no extra message")
	require.Len(t, sent.texts, 1, "only the Drive nudge for the first document: %q", sent.texts)
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
