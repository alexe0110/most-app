package telegram_test

import (
	"log/slog"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
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
			b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: tt.connected})
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			doc := repo.seed(docsbot.Document{
				UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther,
				BackupStatus: docsbot.BackupBackedUp, DriveFileID: "file-1", Title: "a",
			})

			b.ProcessUpdate(ctx, dataCallback("d:sc:"+string(doc.ID)+":auto"))

			require.Equal(t, docsbot.CatAuto, repo.docs[doc.ID].Category)
			require.True(t, has(sent.edits, tt.want), "edits %q", sent.edits)
			require.False(t, has(sent.edits, "прежней папке") && tt.connected, "edits %q", sent.edits)
		})
	}
}

func TestOpenCard_backedUpShowsFileButNoFolder(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	repo.folders = map[docsbot.Category]string{docsbot.CatAuto: "folder-1"}
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatAuto,
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
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			doc := repo.seed(docsbot.Document{
				UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
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
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, dataCallback("d:oc:missing:s"))

	require.True(t, has(sent.edits, "Этого документа уже нет"), "edits %q", sent.edits)
	require.True(t, has(sent.editMarkups, "Мои документы"), "markups %q", sent.editMarkups)
}

func TestSetCategory_editsCardInPlace(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
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

func TestDeleteYes_returnsToOrigin(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	seed := func(title, uid string) docsbot.Document {
		return repo.seed(docsbot.Document{
			UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
			Category: docsbot.CatPassport, Title: title, TgFileID: uid, TgFileUniqueID: uid,
		})
	}
	first, second := seed("Первый", "u1"), seed("Второй", "u2")
	open := func(id docsbot.DocumentID) {
		b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(id)+":l:"+string(docsbot.CatPassport)+":1"))
	}

	open(first.ID)
	b.ProcessUpdate(ctx, dataCallback("d:del:"+string(first.ID)))
	require.True(t, has(sent.edits, "Удалить этот документ"), "edits %q", sent.edits)
	b.ProcessUpdate(ctx, dataCallback("d:dy:"+string(first.ID)))
	require.True(t, has(sent.edits, docsbot.CatPassport.Label()+" — 1"), "category page: %q", sent.edits)

	open(second.ID)
	b.ProcessUpdate(ctx, dataCallback("d:dy:"+string(second.ID)))
	require.True(t, has(sent.edits, "Пока пусто"), "catalog when empty: %q", sent.edits)
	require.Empty(t, sent.texts, "everything is edited in place")
}

func TestDeleteYes_withoutOriginClearsKeyboard(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
	})

	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)))
	b.ProcessUpdate(ctx, dataCallback("d:dy:"+string(doc.ID)))

	require.True(t, has(sent.edits, "Документ удалён"), "edits %q", sent.edits)
	last := sent.editMarkups[len(sent.editMarkups)-1]
	require.NotEmpty(t, last, "reply_markup must be sent to clear the old keyboard")
	require.Contains(t, last, "Мои документы")
	require.Contains(t, last, `"d:lb"`)
	for _, cbData := range []string{"d:snd", "d:rn", "d:sc", "d:del", "d:oc"} {
		require.NotContains(t, last, cbData, "no card buttons remain")
	}
}

func TestDeleteYes_fromSearchReturnsToSearch(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	keep := seedPassport(repo, "Паспорт один", "u1")
	gone := seedPassport(repo, "Паспорт два", "u2")
	b.ProcessUpdate(ctx, textUpdate("паспорт"))

	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(gone.ID)+":s"))
	b.ProcessUpdate(ctx, dataCallback("d:dy:"+string(gone.ID)))

	require.Equal(t, "Нашёл (1):", sent.edits[len(sent.edits)-1])
	require.NotContains(t, repo.docs, gone.ID)
	require.Contains(t, repo.docs, keep.ID)
}

func TestSend_sendsFileWithCaptionAndStaleIsGone(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Паспорт РК", "u1")

	b.ProcessUpdate(ctx, dataCallback("d:snd:"+string(doc.ID)))
	require.Equal(t, []string{"Паспорт РК · " + docsbot.CatPassport.Label()}, sent.files)

	b.ProcessUpdate(ctx, dataCallback("d:snd:missing"))
	require.True(t, has(sent.texts, "Этого документа уже нет"), "texts %q", sent.texts)
}

func TestDeleteNo_restoresCardAndStaleIsGone(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Паспорт РК", "u1")

	b.ProcessUpdate(ctx, dataCallback("d:dn:"+string(doc.ID)))
	require.Contains(t, sent.edits[0], "🗂 ")

	b.ProcessUpdate(ctx, dataCallback("d:dn:missing"))
	require.True(t, has(sent.edits, "Этого документа уже нет"), "edits %q", sent.edits)
}

func TestOpenCard_detectsFileDeletedInDrive(t *testing.T) {
	tests := []struct {
		name      string
		gone      bool
		wantText  string
		wantRetry bool
		wantOpen  bool
	}{
		{"file deleted", true, "Файл удалён в Google Диске, копия осталась в Telegram", true, false},
		{"file there", false, "Google Диск · копия в Telegram", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true, gone: tt.gone})
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			doc := repo.seed(docsbot.Document{
				UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatPassport,
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
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatPassport,
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
