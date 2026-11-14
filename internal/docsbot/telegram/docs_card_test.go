package telegram_test

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestSetCategory_backedUpCard(t *testing.T) {
	tests := []struct {
		name      string
		connected bool
		want      string
	}{
		{"connected: file follows via sync", true, "Обновляю на Google Диске"},
		{"not connected: file stays in the old folder", false, "На Google Диске файл остался в прежней папке"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: tt.connected, ExistsRes: true})
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			doc := repo.Seed(docsbot.Document{
				UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther,
				BackupStatus: docsbot.BackupBackedUp, DriveFileID: "file-1", Title: "a",
			})

			b.ProcessUpdate(ctx, dataCallback("d:sc:"+string(doc.ID)+":auto"))

			require.Equal(t, docsbot.CatAuto, repo.Docs[doc.ID].Category)
			require.True(t, has(sent.edits, tt.want), "edits %q", sent.edits)
			require.False(t, has(sent.edits, "прежней папке") && tt.connected, "edits %q", sent.edits)
		})
	}
}

func TestOpenCard_backedUpShowsFileButNoFolder(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	repo.Folders = map[docsbot.Category]string{docsbot.CatAuto: "folder-1"}
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	doc := repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Category: docsbot.CatAuto,
		BackupStatus: docsbot.BackupBackedUp, DriveFileID: "file-1", Title: "a",
	})

	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)+":s"))

	require.NotEmpty(t, sent.edits)
	require.False(t, has(sent.edits, "прежней папке"), "the note is one-shot")
}

func TestOpenCard_editsMessageWithBack(t *testing.T) {
	tests := []struct {
		name     string
		suffix   string
		wantBack string
	}{
		{"from list", ":l:" + string(docsbot.CatPassport) + ":1", "d:lc:" + string(docsbot.CatPassport) + ":1"},
		{"from search", ":s", "d:sb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			doc := repo.Seed(docsbot.Document{
				UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
				Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
			})

			b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)+tt.suffix))

			require.True(t, has(sent.edits, "🗂 "), "edits %q", sent.edits)
			require.True(t, has(sent.edits, "📄"), "edits %q", sent.edits)
			require.True(t, has(sent.editMarkups, "⬅️ Назад"), "markups %q", sent.editMarkups)
			require.True(t, has(sent.editMarkups, tt.wantBack), "markups %q", sent.editMarkups)
			require.Empty(t, sent.texts, "no file or message is sent")
		})
	}
}

func TestOpenCard_unknownDocumentIsGone(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:oc:missing:s"))

	require.True(t, has(sent.edits, "Этого документа уже нет"), "edits %q", sent.edits)
	require.True(t, has(sent.editMarkups, "Мои документы"), "markups %q", sent.editMarkups)
}

func TestSetCategory_editsCardInPlace(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	doc := repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
	})
	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)+":l:"+string(docsbot.CatPassport)+":1"))

	b.ProcessUpdate(ctx, dataCallback("d:sc:"+string(doc.ID)+":"+string(docsbot.CatOther)))

	require.Len(t, sent.edits, 2)
	last := sent.edits[len(sent.edits)-1]
	require.Contains(t, last, "🗂 ")
	require.Contains(t, last, docsbot.CatOther.Label())
	require.True(t, has(sent.editMarkups, "d:lc:"+string(docsbot.CatPassport)+":1"), "origin kept: %q", sent.editMarkups)
	require.Empty(t, sent.texts, "no new card is sent")
}

func TestSend_sendsFileWithCaptionAndStaleIsGone(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	doc := seedPassport(repo, "Паспорт РК", "u1")

	b.ProcessUpdate(ctx, dataCallback("d:snd:"+string(doc.ID)))
	require.Equal(t, []string{"Паспорт РК · " + docsbot.CatPassport.Label()}, sent.files)

	b.ProcessUpdate(ctx, dataCallback("d:snd:missing"))
	require.True(t, has(sent.texts, "Этого документа уже нет"), "texts %q", sent.texts)
}

func TestOpenCard_detectsFileDeletedInDrive(t *testing.T) {
	tests := []struct {
		name      string
		gone      bool
		wantText  string
		wantRetry bool
		wantOpen  bool
	}{
		{"file deleted", true, "На Google Диске файла нет, копия осталась в Telegram", true, false},
		{"file there", false, "Google Диск · копия в Telegram", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: !tt.gone})
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			doc := repo.Seed(docsbot.Document{
				UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatPassport,
				Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
				BackupStatus: docsbot.BackupBackedUp, DriveFileID: "remote", SyncStatus: docsbot.SyncOK,
			})

			b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)+":s"))

			require.True(t, has(sent.edits, tt.wantText), "edits %q", sent.edits)
			require.Equal(t, tt.wantRetry, has(sent.editMarkups, "d:rt:"+string(doc.ID)), "markups %q", sent.editMarkups)
			require.Equal(t, tt.wantOpen, has(sent.editMarkups, "drive.google.com/file"), "markups %q", sent.editMarkups)
		})
	}
}

// Every card view is HTML (the title is escaped in cardText): sent, opened from a
// list, recategorized, and the rename prompt and hint around it.
func TestCard_isAlwaysSentAsHTML(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	doc := repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatPassport,
		Title: "A & <b>B</b>", TgFileID: "f", TgFileUniqueID: "u",
	})

	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)+":s"))
	require.Contains(t, sent.edits[len(sent.edits)-1], "<b>A &amp; &lt;b&gt;B&lt;/b&gt;</b>")
	b.ProcessUpdate(ctx, dataCallback("d:rn:"+string(doc.ID)))
	b.ProcessUpdate(ctx, textUpdateID(5, " "))
	b.ProcessUpdate(ctx, dataCallback("d:rnc:"+string(doc.ID)))
	b.ProcessUpdate(ctx, dataCallback("d:sc:"+string(doc.ID)+":"+string(docsbot.CatAuto)))
	b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		From: &models.User{ID: 100}, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "f", FileUniqueID: "u", FileName: "x.pdf"},
	}}) // the same file again: AlreadySaved card

	require.Len(t, sent.editModes, 5)
	for _, mode := range sent.editModes {
		require.Equal(t, "HTML", mode)
	}
	require.Equal(t, []string{"HTML"}, sent.sendModes, "the already-saved card")
}

func TestCard_convertRowOnlyForConvertible(t *testing.T) {
	tests := []struct {
		name string
		doc  docsbot.Document
		want bool
	}{
		{"photo", docsbot.Document{Kind: docsbot.KindPhoto}, true},
		{"png file", docsbot.Document{Kind: docsbot.KindDocument, MIME: "image/png"}, true},
		{"pdf file", docsbot.Document{Kind: docsbot.KindDocument, MIME: "application/pdf"}, true},
		{"word file", docsbot.Document{Kind: docsbot.KindDocument, MIME: "application/msword"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			tt.doc.UserID, tt.doc.Status, tt.doc.Category = docsbottest.TestUserID, docsbot.StatusSaved, docsbot.CatOther
			tt.doc.Title, tt.doc.TgFileID, tt.doc.TgFileUniqueID = "Файл", "f", "u"
			doc := repo.Seed(tt.doc)

			b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)+":s"))

			require.Len(t, sent.editMarkups, 1)
			require.Equal(t, tt.want, strings.Contains(sent.editMarkups[0], "conv:doc:"+string(doc.ID)), "%q", sent.editMarkups[0])
		})
	}
}

func TestCard_noConvertRowForHEIC(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	doc := repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "image/heic",
		Category: docsbot.CatOther, Title: "IMG.heic", TgFileID: "x", TgFileUniqueID: "heic-uniq",
	})

	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)+":s"))

	require.Len(t, sent.editMarkups, 1)
	require.NotContains(t, sent.editMarkups[0], "conv:doc:")
}

func TestChangeCategory_pickerAndBack(t *testing.T) {
	tests := []struct {
		name     string
		suffix   string // origin token the card was opened with
		wantBack string // callback of the card's own "Назад", "" for none
	}{
		{"opened from a list", ":l:" + string(docsbot.CatPassport) + ":2", "d:lc:" + string(docsbot.CatPassport) + ":2"},
		{"opened from a search", ":s", "d:sb"},
		{"no origin", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			doc := seedPassport(repo, "Паспорт <РК> & Ко", "u1")
			id := string(doc.ID)

			b.ProcessUpdate(ctx, dataCallback("d:oc:"+id+tt.suffix))
			card := sent.edits[len(sent.edits)-1]
			b.ProcessUpdate(ctx, dataCallback("d:chc:"+id))

			picker := sent.edits[len(sent.edits)-1]
			require.Equal(t, "Новая категория для «Паспорт <РК> & Ко»:", picker, "the picker names the document")
			require.Empty(t, sent.editModes[len(sent.editModes)-1], "plain text: the title needs no escaping")
			pickerKB := sent.editMarkups[len(sent.editMarkups)-1]
			for _, cat := range docsbot.AllCategories {
				require.Contains(t, pickerKB, "d:sc:"+id+":"+string(cat))
			}
			require.Contains(t, pickerKB, "d:cb:"+id, "a way back")
			require.Contains(t, pickerKB, "⬅️ Назад")

			b.ProcessUpdate(ctx, dataCallback("d:cb:"+id))

			require.Equal(t, card, sent.edits[len(sent.edits)-1], "the card comes back as it was")
			require.Equal(t, "HTML", sent.editModes[len(sent.editModes)-1])
			cardKB := sent.editMarkups[len(sent.editMarkups)-1]
			require.Contains(t, cardKB, "d:chc:"+id)
			require.NotContains(t, cardKB, "d:sc:", "the category buttons are gone")
			if tt.wantBack != "" {
				require.Contains(t, cardKB, tt.wantBack, "the card keeps where it was opened from")
			}
			require.Equal(t, docsbot.CatPassport, repo.Docs[doc.ID].Category, "going back changes nothing")
			require.Empty(t, sent.texts, "everything is edited in place")
		})
	}
}

func TestChangeCategory_staleButtons(t *testing.T) {
	tests := []struct {
		name string
		data string
		msg  func(*models.Update) *models.Update
		want string // text of the last edit, "" for none
	}{
		{"picker for a deleted document", "d:chc:missing", nil, "Этого документа уже нет."},
		{"back to a deleted document", "d:cb:missing", nil, "Этого документа уже нет."},
		{"back without a document id", "d:cb", nil, ""},
		{"picker without a document id", "d:chc", nil, ""},
		{"back on a message Telegram no longer gives", "d:cb:missing", inaccessible, ""},
		{"picker on a message Telegram no longer gives", "d:chc:missing", inaccessible, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, _ := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			u := dataCallback(tt.data)
			if tt.msg != nil {
				u = tt.msg(u)
			}

			b.ProcessUpdate(ctx, u)

			if tt.want == "" {
				require.Empty(t, sent.edits)
				require.Empty(t, sent.texts)
				return
			}
			require.Equal(t, tt.want, sent.edits[len(sent.edits)-1])
			require.Contains(t, sent.editMarkups[len(sent.editMarkups)-1], "Мои документы")
		})
	}
}

func TestChangeCategory_backOnLiveStatusMessageKeepsStatus(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, ExistsRes: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	id := seedPending(repo, 1)[0]
	pick(ctx, b, id, 11)
	b.ProcessUpdate(ctx, dataCallbackMsg(11, "d:chc:"+string(id)))

	b.ProcessUpdate(ctx, dataCallbackMsg(11, "d:cb:"+string(id)))

	require.Contains(t, sent.edits[len(sent.edits)-1], "Загружаю на Google Диск", "the live upload card is restored, not a plain card")
}

func TestChangeCategory_anotherUsersDocumentIsGone(t *testing.T) {
	for _, data := range []string{"d:chc:", "d:cb:"} {
		t.Run(data, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			foreign := repo.Seed(docsbot.Document{
				UserID: "22222222-2222-2222-2222-222222222222", Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
				Category: docsbot.CatPassport, Title: "Чужой паспорт", TgFileID: "f", TgFileUniqueID: "u",
			})

			b.ProcessUpdate(ctx, dataCallback(data+string(foreign.ID)))

			require.Equal(t, "Этого документа уже нет.", sent.edits[len(sent.edits)-1])
			require.NotContains(t, strings.Join(sent.snapshot(&sent.edits), "\n"), "Чужой паспорт")
			require.Equal(t, docsbot.CatPassport, repo.Docs[foreign.ID].Category)
		})
	}
}
