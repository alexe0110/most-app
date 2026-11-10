package telegram_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const staleReply = "Кнопка устарела, открой заново."

// liveMessages lists the sent messages (ids above the harness start) that were
// not deleted.
func (h *convHarness) liveMessages() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	var live []int
	for id := 101; id <= h.nextMsg; id++ {
		if !containsInt(h.deleted, id) {
			live = append(live, id)
		}
	}
	return live
}

// An album sent in PDF mode arrives as parallel updates: the chat must end with
// exactly one live dialogue message that counts every page, and one PDF.
func TestConvertPDF_parallelAlbumLeavesOneDialogue(t *testing.T) {
	const photos = 8
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", h.lastMsg()))

	var wg sync.WaitGroup
	for i := 1; i <= photos; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.bot.ProcessUpdate(ctx, photoUpdate(i))
		}()
	}
	wg.Wait()

	live := h.liveMessages()
	require.Len(t, live, 1, "exactly one live dialogue message")
	require.Equal(t, h.lastMsg(), live[0])
	require.Contains(t, h.lastText(), "страниц: 8", "the live message shows the total")

	h.bot.ProcessUpdate(ctx, callbackOn("conv:done", live[0]))
	require.Len(t, h.docs, 1)
	require.Equal(t, photos, pdfPages(t, h.docs[0].data))
	require.Empty(t, h.liveMessages(), "the progress note is gone after the result")
}

// Every edit and delete is refused (messages too old): the dialogue still
// finishes, one new message per step, and the stale ones stay dead.
func TestConvertDialogue_everythingRefusedStillFinishes(t *testing.T) {
	tests := []struct {
		name string
		run  func(h *convHarness)
	}{
		{"compress", func(h *convHarness) {
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", h.lastMsg()))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:limit1", h.lastMsg()))
		}},
		{"photos to pdf", func(h *convHarness) {
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", h.lastMsg()))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			h.bot.ProcessUpdate(ctx, photoUpdate(2))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:done", h.lastMsg()))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
			start := h.lastMsg()
			h.failEdits, h.failDeletes = true, true

			require.NotPanics(t, func() { tt.run(h) })

			require.Len(t, h.docs, 1, "the result is delivered")
			h.bot.ProcessUpdate(t.Context(), callbackOn("conv:cancel", start))
			require.Equal(t, staleReply, h.lastText(), "the first message is dead")
		})
	}
}

func TestConvertCompress_badCustomLimitKeepsStep(t *testing.T) {
	for _, text := range []string{"0", "-5", "abc", "", "  ", "20481", "99999999999999999999", "1e3", "1.5", "١٢٣"} {
		t.Run(text, func(t *testing.T) {
			h := newConvHarness(t)
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", h.lastMsg()))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:limitcustom", h.lastMsg()))
			dialogue := h.lastMsg()

			h.bot.ProcessUpdate(ctx, convText(text))
			require.Empty(t, h.docs)

			// The step is intact: a valid size still works, and the dialogue message
			// is the one that asked.
			h.bot.ProcessUpdate(ctx, convText("300"))
			require.Len(t, h.docs, 1)
			require.Contains(t, h.editTexts, strconvID(dialogue)+":⏳ Обрабатываю…")
		})
	}
}

// /convert again replaces the session: buttons of the old dialogue are stale.
func TestConvertDialogue_restartKillsOldButtons(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	old := h.lastMsg()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	fresh := h.lastMsg()
	require.NotEqual(t, old, fresh)

	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", old))
	require.Equal(t, staleReply, h.lastText())
	require.NotContains(t, h.editTexts, strconvID(old)+":Пришли фото")

	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", fresh))
	require.Contains(t, h.lastShown(), "Пришли фото")
}

// A second PDF at the operation menu, a photo at the range prompt and other
// off-step input change nothing: the menu still works afterwards.
func TestConvertPDFActions_offStepInputKeepsMenu(t *testing.T) {
	h := newConvHarness(t)
	h.serve("pdf-in", testPDF(t, 2))
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdfops", h.lastMsg()))
	h.bot.ProcessUpdate(ctx, namedDocumentUpdate("pdf-in", "application/pdf", "a.pdf"))
	menu := h.lastMsg()

	h.bot.ProcessUpdate(ctx, namedDocumentUpdate("pdf-in", "application/pdf", "b.pdf"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, convText("hello"))
	require.NotContains(t, h.deleted, menu, "the menu message stays")

	h.bot.ProcessUpdate(ctx, callbackOn("conv:p:rot", menu))
	require.Len(t, h.docs, 1)
	require.Equal(t, 2, pdfPages(t, h.docs[0].data))
}

// A cancelled context (shutdown mid-dialogue) must not panic or leave the
// session claimed forever.
func TestConvertDialogue_cancelledContextDoesNotPanic(t *testing.T) {
	h := newConvHarness(t)
	h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(t.Context(), callbackOn("conv:compress", h.lastMsg()))
	h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
	limit := h.lastMsg()

	dead, cancel := context.WithCancel(t.Context())
	cancel()
	require.NotPanics(t, func() {
		h.bot.ProcessUpdate(dead, callbackOn("conv:limit1", limit))
		h.bot.ProcessUpdate(dead, photoUpdate(2))
	})
	require.Empty(t, h.docs)
}

func strconvID(id int) string { return fmt.Sprint(id) }
