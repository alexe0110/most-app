package converter_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestConvert_unsupportedDocument(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, documentUpdate("application/zip"))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastText(), "С таким файлом не справлюсь")
}

func TestConvert_cancelReturnsToMainScenario(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:cancel"))

	// With the session gone, an unknown command falls through to the catch-all
	// (plain text is now a search, which needs a stored user).
	h.bot.ProcessUpdate(ctx, commandUpdate("foobar"))
	require.Contains(t, h.lastText(), "Такой команды нет")
}

// Every finished conversion is one convert_done event naming only the operation.
func TestConvert_recordsConvertDone(t *testing.T) {
	t.Parallel()
	user := func(t *testing.T) context.Context {
		return tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	}
	tests := []struct {
		name string
		run  func(t *testing.T, h *convHarness, album *docsbottest.FakeTimer)
		want docsbot.ConvertOp
	}{
		{"photos to pdf", func(t *testing.T, h *convHarness, _ *docsbottest.FakeTimer) {
			ctx := user(t)
			h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))
		}, docsbot.ConvertPhotoPDF},
		{"compress in the dialogue", func(t *testing.T, h *convHarness, _ *docsbottest.FakeTimer) {
			ctx := user(t)
			h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limit2"))
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtjpeg"))
		}, docsbot.ConvertCompress},
		{"album to pdf", func(t *testing.T, h *convHarness, ft *docsbottest.FakeTimer) {
			ctx := user(t)
			for n := 1; n <= 2; n++ {
				h.bot.ProcessUpdate(ctx, albumImageUpdate(n, 1000))
			}
			ft.Fire()
			tok := albumToken.FindStringSubmatch(h.albumMarkup())
			require.NotNil(t, tok)
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:alb:"+tok[1]))
		}, docsbot.ConvertAlbumPDF},
		{"stored image to pdf", func(t *testing.T, h *convHarness, _ *docsbottest.FakeTimer) {
			src := seedSource(h, docsbot.StatusSaved)
			h.bot.ProcessUpdate(user(t), callbackUpdate("conv:q:"+string(src.ID)+":pdf"))
		}, docsbot.ConvertPhotoPDF},
		{"stored image compressed", func(t *testing.T, h *convHarness, _ *docsbottest.FakeTimer) {
			src := seedSource(h, docsbot.StatusSaved)
			h.bot.ProcessUpdate(user(t), callbackUpdate("conv:q:"+string(src.ID)+":c1"))
		}, docsbot.ConvertCompress},
		{"webp to jpg", func(t *testing.T, h *convHarness, _ *docsbottest.FakeTimer) {
			h.serve("webp-src", tinyWebP(t))
			src := h.repo.Seed(docsbot.Document{
				UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "image/webp",
				Category: docsbot.CatOther, Title: "Скан.webp", TgFileID: "webp-src", TgFileUniqueID: "webp-uniq",
			})
			h.bot.ProcessUpdate(user(t), callbackUpdate("conv:q:"+string(src.ID)+":jpg"))
		}, docsbot.ConvertWebPJPG},
		{"pdf rotate", func(t *testing.T, h *convHarness, _ *docsbottest.FakeTimer) {
			src := seedPDF(t, h)
			h.bot.ProcessUpdate(user(t), callbackUpdate("conv:q:"+string(src.ID)+":rot"))
		}, docsbot.ConvertPDFRotate},
		{"pdf keep pages", func(t *testing.T, h *convHarness, _ *docsbottest.FakeTimer) {
			src := seedPDF(t, h)
			h.bot.ProcessUpdate(user(t), callbackUpdate("conv:q:"+string(src.ID)+":keep"))
			h.bot.ProcessUpdate(user(t), convText("1-2"))
		}, docsbot.ConvertPDFKeep},
		{"pdf remove pages", func(t *testing.T, h *convHarness, _ *docsbottest.FakeTimer) {
			src := seedPDF(t, h)
			h.bot.ProcessUpdate(user(t), callbackUpdate("conv:q:"+string(src.ID)+":drop"))
			h.bot.ProcessUpdate(user(t), convText("1"))
		}, docsbot.ConvertPDFRemove},
		{"pdf merge", func(t *testing.T, h *convHarness, _ *docsbottest.FakeTimer) {
			src := seedPDF(t, h)
			h.serve("pdf-add", testPDF(t, 2))
			h.bot.ProcessUpdate(user(t), callbackUpdate("conv:q:"+string(src.ID)+":merge"))
			h.bot.ProcessUpdate(user(t), pdfUpdate("pdf-add", 1000))
			h.bot.ProcessUpdate(user(t), callbackUpdate("conv:done"))
		}, docsbot.ConvertPDFMerge},
		{"pdf rotate in the dialogue", func(t *testing.T, h *convHarness, _ *docsbottest.FakeTimer) {
			ctx := user(t)
			h.serve("pdf-in", testPDF(t, 2))
			h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:pdfops", h.lastMsg()))
			h.bot.ProcessUpdate(ctx, namedDocumentUpdate("pdf-in", "application/pdf", "a.pdf"))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:p:rot", h.lastMsg()))
		}, docsbot.ConvertPDFRotate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ft := albumHarness(t)

			tt.run(t, h, ft)

			require.Len(t, h.docs, 1)
			require.Equal(t, []docsbot.Event{{
				User: docsbottest.TestUserID, Name: docsbot.EventConvertDone, Props: docsbot.EventProps{Operation: tt.want},
			}}, h.repo.Events)
		})
	}
}

func TestConvert_noEventWhenTheResultIsNotSent(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	src := seedSource(h, docsbot.StatusSaved)
	h.failDocs = true

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":c1"))

	require.Contains(t, h.lastShown(), "Не удалось отправить файл")
	require.Empty(t, h.repo.Events, "a failed send is retried, not counted")
}

func TestConvert_eventFailureDoesNotBreakTheResult(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.repo.EventsErr = errors.New("db down")
	src := seedSource(h, docsbot.StatusSaved)

	h.bot.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), callbackUpdate("conv:q:"+string(src.ID)+":c1"))

	require.Len(t, h.docs, 1)
	require.Empty(t, h.repo.Events)
}
