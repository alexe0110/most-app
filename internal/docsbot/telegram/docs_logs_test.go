package telegram_test

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestLogs_traceDocumentWithoutLeaks(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, _, repo := newBotWithLog(t, log)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	// Receive a file, choose a category, search, then ask for the file back.
	b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		From:     &models.User{ID: 100},
		Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "Паспорт.pdf"},
	}})
	require.Len(t, repo.docs, 1)
	var id docsbot.DocumentID
	for k := range repo.docs {
		id = k
	}
	b.ProcessUpdate(ctx, dataCallback("d:cat:"+string(id)+":"+string(docsbot.CatOther)))
	b.ProcessUpdate(ctx, textUpdate("СекретныйЗапрос"))
	b.ProcessUpdate(ctx, dataCallback("d:snd:"+string(id)))

	out := buf.String()
	require.Contains(t, out, `"doc_id":"`+string(id)+`"`)
	require.Contains(t, out, "document received")
	require.Contains(t, out, "document saved")
	require.Contains(t, out, "document sent")
	require.Contains(t, out, `"op":"sendMessage"`)
	for _, leak := range []string{"Паспорт", "123:ABC", "СекретныйЗапрос", "chat_id", "tg_user_id", "f1"} {
		require.NotContains(t, out, leak)
	}
}

func TestLogs_editFlowWithoutLeaks(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, _, repo := newBotWithLog(t, log)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	recv := func() {
		b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
			From:     &models.User{ID: 100},
			Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
			Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "Паспорт.pdf"},
		}})
	}
	recv()
	require.Len(t, repo.docs, 1)
	var id docsbot.DocumentID
	for k := range repo.docs {
		id = k
	}
	b.ProcessUpdate(ctx, dataCallback("d:cat:"+string(id)+":"+string(docsbot.CatOther)))
	b.ProcessUpdate(ctx, dataCallback("d:cat:"+string(id)+":"+string(docsbot.CatOther)))
	b.ProcessUpdate(ctx, dataCallback("d:rn:"+string(id)))
	b.ProcessUpdate(ctx, textUpdate("НовоеИмяСекрет"))
	b.ProcessUpdate(ctx, dataCallback("d:dy:"+string(id)))

	out := buf.String()
	for _, ev := range []string{"document received", "document renamed", "document deleted"} {
		require.Contains(t, out, ev)
	}
	require.Contains(t, out, `"doc_id":"`+string(id)+`"`)
	for _, leak := range []string{"Паспорт", "НовоеИмяСекрет", "123:ABC", "chat_id", "tg_user_id", "f1", "u1"} {
		require.NotContains(t, out, leak)
	}
}

func TestSettingsActions_logEventsWithoutLeaks(t *testing.T) {
	var sink lockedBuffer
	log := slog.New(slog.NewJSONHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, _, repo := newBotWithLog(t, log)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "ПаспортSECRET"})

	b.ProcessUpdate(ctx, dataCallback("d:try"))
	b.ProcessUpdate(ctx, dataCallback("d:exp"))
	b.ProcessUpdate(ctx, dataCallback("d:dc"))
	b.ProcessUpdate(ctx, dataCallback("d:dd"))

	out := sink.String()
	for _, ev := range []string{"transfer started", "export started", "drive disconnected", "delete data requested"} {
		require.Contains(t, out, ev)
	}
	for _, leak := range []string{"ПаспортSECRET", "SECRET", "123:ABC", "chat_id", "tg_user_id"} {
		require.NotContains(t, out, leak)
	}
}
