package telegram_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/go-telegram/bot"
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

// A user who blocked the bot is routine: whatever the handler sends, edits,
// deletes or answers, Telegram's 403 is an Info line (no ids, no content), never
// an Error, and the handler neither panics nor loses its own work.
func TestLogs_blockedUserIsNotAnErrorInHandlers(t *testing.T) {
	tests := []struct {
		name   string
		run    func(b *bot.Bot, ctx context.Context, repo *fakeRepo)
		method string // the call Telegram refused
	}{
		{"help", func(b *bot.Bot, ctx context.Context, _ *fakeRepo) {
			b.ProcessUpdate(ctx, commandUpdate(100, "help"))
		}, "sendMessage"},
		{"start", func(b *bot.Bot, ctx context.Context, _ *fakeRepo) {
			b.ProcessUpdate(ctx, commandUpdate(100, "start"))
		}, "sendMessage"},
		{"document intake keeps the document", func(b *bot.Bot, ctx context.Context, repo *fakeRepo) {
			b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
				From:     &models.User{ID: 100},
				Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
				Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "СекретныйДокумент.pdf"},
			}})
			require.Len(t, repo.docs, 1)
		}, "sendMessage"},
		{"card edit and callback answer", func(b *bot.Bot, ctx context.Context, repo *fakeRepo) {
			doc := seedPassport(repo, "СекретныйДокумент", "u1")
			b.ProcessUpdate(ctx, dataCallback("d:rn:"+string(doc.ID)))
		}, "editMessageText"},
		{"callback answer", func(b *bot.Bot, ctx context.Context, repo *fakeRepo) {
			doc := seedPassport(repo, "СекретныйДокумент", "u1")
			b.ProcessUpdate(ctx, dataCallback("d:dy:"+string(doc.ID)))
		}, "answerCallbackQuery"},
		{"export clears the keyboard", func(b *bot.Bot, ctx context.Context, repo *fakeRepo) {
			seedPassport(repo, "СекретныйДокумент", "u1")
			b.ProcessUpdate(ctx, dataCallback("d:exp"))
		}, "editMessageReplyMarkup"},
		{"converter start", func(b *bot.Bot, ctx context.Context, _ *fakeRepo) {
			b.ProcessUpdate(ctx, commandUpdate(100, "convert"))
		}, "sendMessage"},
		{"converter stale button", func(b *bot.Bot, ctx context.Context, _ *fakeRepo) {
			b.ProcessUpdate(ctx, dataCallback("conv:cancel"))
		}, "editMessageReplyMarkup"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf lockedBuffer
			log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			b, sent, repo := newBotWithLog(t, log)
			sent.forbidden = true
			ctx := tg.ContextWithUserID(t.Context(), testUserID)

			require.NotPanics(t, func() { tt.run(b, ctx, repo) })

			sent.mu.Lock()
			refused := append([]string(nil), sent.refused...)
			sent.mu.Unlock()
			require.Contains(t, refused, tt.method, "the scenario must reach the refused call")

			out := buf.String()
			require.Contains(t, out, `"level":"INFO"`)
			require.Contains(t, out, "bot blocked by user")
			require.NotContains(t, out, `"level":"ERROR"`)
			require.NotContains(t, out, `"level":"WARN"`)
			for _, leak := range []string{"Секретный", "123:ABC", "chat_id", "tg_user_id"} {
				require.NotContains(t, out, leak)
			}
		})
	}
}
