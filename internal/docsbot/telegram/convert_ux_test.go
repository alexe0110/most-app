package telegram_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestConvertPDF_photosShowOneAcceptedMessage(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", h.lastMsg()))

	var live []int // dialogue messages that were not deleted
	for i := 1; i <= 3; i++ {
		h.bot.ProcessUpdate(ctx, photoUpdate(i))
		require.Contains(t, h.lastText(), fmt.Sprintf("страниц: %d", i))
		require.Contains(t, h.lastMarkup(), "conv:done", "Готово is on the same message")
		require.Contains(t, h.lastMarkup(), "conv:cancel")
	}
	for id := 101; id <= h.lastMsg(); id++ {
		if !slices.Contains(h.deleted, id) {
			live = append(live, id)
		}
	}
	require.Equal(t, []int{h.lastMsg()}, live, "exactly one live dialogue message")
}

func TestConvertPDF_fileTipOncePerUser(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", h.lastMsg()))

	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	require.Contains(t, h.lastText(), "как файл", "the first compressed photo gets the tip")
	h.bot.ProcessUpdate(ctx, photoUpdate(2))
	require.NotContains(t, h.lastText(), "как файл", "the tip is shown once")

	// A file (not a compressed photo) never gets it.
	h2 := newConvHarness(t)
	h2.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h2.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", h2.lastMsg()))
	h2.bot.ProcessUpdate(ctx, namedDocumentUpdate("doc", "image/jpeg", "a.jpg"))
	require.NotContains(t, h2.lastText(), "как файл")
	require.Contains(t, h2.lastText(), "страниц: 1")
}

func TestConvertCompress_formatQuestionOnlyForPNG(t *testing.T) {
	tests := []struct {
		name     string
		png      bool
		custom   bool
		wantAsk  bool
		wantFile string
	}{
		{"jpeg, limit button", false, false, false, "фото-сжатый.jpg"},
		{"jpeg, custom limit", false, true, false, "фото-сжатый.jpg"},
		{"png, limit button", true, false, true, "фото-сжатый.png"},
		{"png, custom limit", true, true, true, "фото-сжатый.png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			if tt.png {
				h.servePNG("large")
			}
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", h.lastMsg()))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			if tt.custom {
				h.bot.ProcessUpdate(ctx, callbackOn("conv:limitcustom", h.lastMsg()))
				h.bot.ProcessUpdate(ctx, convText("300"))
			} else {
				h.bot.ProcessUpdate(ctx, callbackOn("conv:limit2", h.lastMsg()))
			}

			require.Equal(t, tt.wantAsk, strings.Contains(h.lastShown(), "формате") || strings.Contains(h.lastMarkup(), "conv:fmtpng"))
			if tt.wantAsk {
				require.Empty(t, h.docs)
				h.bot.ProcessUpdate(ctx, callbackOn("conv:fmtpng", h.lastMsg()))
			}
			require.Len(t, h.docs, 1)
			require.Equal(t, tt.wantFile, h.docs[0].filename)
		})
	}
}

func TestConvertPDFActions_fromCommand(t *testing.T) {
	setup := func(t *testing.T) (*convHarness, int) {
		h := newConvHarness(t)
		h.serve("pdf-in", testPDF(t, 3))
		ctx := t.Context()
		h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
		require.Contains(t, h.lastMarkup(), "conv:pdfops")
		h.bot.ProcessUpdate(ctx, callbackOn("conv:pdfops", h.lastMsg()))
		require.Contains(t, h.lastShown(), "PDF файлом")

		// Anything but a PDF is refused and the step stays.
		h.bot.ProcessUpdate(ctx, photoUpdate(1))
		require.Contains(t, h.lastText(), "Это не PDF")
		h.bot.ProcessUpdate(ctx, namedDocumentUpdate("x", "image/png", "a.png"))
		require.Contains(t, h.lastText(), "Это не PDF")

		h.bot.ProcessUpdate(ctx, namedDocumentUpdate("pdf-in", "application/pdf", "Счёт.pdf"))
		require.Contains(t, h.lastText(), "Что сделать с PDF")
		for _, op := range []string{"rot", "keep", "drop", "merge"} {
			require.Contains(t, h.lastMarkup(), "conv:p:"+op)
		}
		return h, h.lastMsg()
	}

	t.Run("rotate", func(t *testing.T) {
		h, menu := setup(t)
		h.bot.ProcessUpdate(t.Context(), callbackOn("conv:p:rot", menu))

		require.Len(t, h.docs, 1)
		require.Equal(t, 3, pdfPages(t, h.docs[0].data))
		require.Equal(t, "Счёт-изменён.pdf", h.docs[0].filename)
		require.Contains(t, h.deleted, menu, "the menu is gone once the result is sent")
	})

	t.Run("keep pages", func(t *testing.T) {
		h, menu := setup(t)
		ctx := t.Context()
		h.bot.ProcessUpdate(ctx, callbackOn("conv:p:keep", menu))
		require.Contains(t, h.lastShown(), "Какие страницы")
		h.bot.ProcessUpdate(ctx, callbackOn("conv:p:rot", menu))
		require.Equal(t, "Кнопка устарела, открой заново.", h.lastText(), "the menu step is over")

		h.bot.ProcessUpdate(ctx, convText("1-2"))

		require.Len(t, h.docs, 1)
		require.Equal(t, 2, pdfPages(t, h.docs[0].data))
	})

	t.Run("stale press", func(t *testing.T) {
		h, menu := setup(t)
		ctx := t.Context()
		h.bot.ProcessUpdate(ctx, callbackOn("conv:p:rot", menu+50))
		require.Equal(t, "Кнопка устарела, открой заново.", h.lastText())
		require.Empty(t, h.docs)
	})

	t.Run("saved without a source document goes to Other", func(t *testing.T) {
		h, menu := setup(t)
		ctx := tg.ContextWithUserID(t.Context(), testUserID)
		h.bot.ProcessUpdate(ctx, callbackOn("conv:p:rot", menu))
		tok := saveToken.FindStringSubmatch(h.docMarkups[0])
		require.NotNil(t, tok)

		h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok[1]))

		require.Len(t, h.repo.docs, 1)
		for _, d := range h.repo.docs {
			require.Equal(t, docsbot.StatusSaved, d.Status)
			require.Equal(t, docsbot.CatOther, d.Category)
		}
	})
}

func TestConvertCompress_resultNameFollowsTheSentFile(t *testing.T) {
	tests := []struct {
		name string
		send func(h *convHarness)
		want string
	}{
		{"compressed photo has no name", func(h *convHarness) { h.bot.ProcessUpdate(t.Context(), photoUpdate(1)) }, "фото-сжатый.jpg"},
		{"file keeps its name", func(h *convHarness) {
			h.bot.ProcessUpdate(t.Context(), namedDocumentUpdate("doc", "image/jpeg", "Паспорт.jpg"))
		}, "Паспорт-сжатый.jpg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", h.lastMsg()))
			tt.send(h)
			h.bot.ProcessUpdate(ctx, callbackOn("conv:limit1", h.lastMsg()))

			require.Len(t, h.docs, 1)
			require.Equal(t, tt.want, h.docs[0].filename)
		})
	}
}

func TestConvertMenu_progressThenResult(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(src.ID)))
	menu := h.lastMsg()

	h.bot.ProcessUpdate(ctx, callbackOn("conv:q:"+string(src.ID)+":c1", menu))

	require.Len(t, h.docs, 1)
	require.Contains(t, h.editTexts, fmt.Sprintf("%d:⏳ Обрабатываю…", menu), "the menu turns into a progress note")
	require.Contains(t, h.deleted, menu, "and is removed with the result")
}

func TestConvertMenu_errorBringsMenuBack(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	h.serve("src", []byte("not an image"))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(src.ID)))
	menu := h.lastMsg()

	h.bot.ProcessUpdate(ctx, callbackOn("conv:q:"+string(src.ID)+":c1", menu))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastShown(), "Так я пока не умею")
	require.Empty(t, h.deleted, "the menu stays so the user can pick another action")
}
