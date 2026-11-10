package telegram_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// sends counts the messages the bot sent.
func (h *convHarness) sends() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.texts)
}

func TestConvertDialogue_editFallback(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	start := h.lastMsg()
	sends := h.sends()

	// The edit is refused: the next step goes out as one new message.
	h.failEdits = true
	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", start))
	require.Equal(t, sends+1, h.sends(), "one new message, no duplicates")
	require.Contains(t, h.lastText(), "Пришли фото")
	require.Empty(t, h.editTexts)
	moved := h.lastMsg()
	require.NotEqual(t, start, moved)

	// The old message is dead; the new one drives the session and is edited again.
	h.failEdits = false
	h.bot.ProcessUpdate(ctx, callbackOn("conv:done", start))
	require.Equal(t, "Кнопка устарела, открой заново.", h.lastText())
	h.bot.ProcessUpdate(ctx, photoUpdate(1)) // the dialogue moves down: the prompt is replaced by a new message
	accepted := h.lastMsg()
	require.Contains(t, h.deleted, moved)
	sendsBefore := h.sends()
	h.bot.ProcessUpdate(ctx, callbackOn("conv:done", accepted))

	require.Len(t, h.docs, 1)
	require.Equal(t, sendsBefore, h.sends(), "the progress is an edit of the live message")
	require.Contains(t, h.editTexts, fmt.Sprintf("%d:⏳ Обрабатываю…", accepted))
}

func TestConvertDialogue_notModifiedIsSuccess(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	start := h.lastMsg()
	sends := h.sends()

	h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", start))

	require.Equal(t, sends, h.sends(), "an accepted edit sends nothing")
	require.Len(t, h.editTexts, 1)
}

func TestConvertDialogue_resultReplacesProgress(t *testing.T) {
	tests := []struct {
		name        string
		failDeletes bool
		wantDeleted bool
		wantDone    bool
	}{
		{"progress is deleted", false, true, false},
		{"delete refused: becomes a done note", true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			h.failDeletes = tt.failDeletes
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
			start := h.lastMsg()
			h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", start))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			dialogue := h.lastMsg()

			h.bot.ProcessUpdate(ctx, callbackOn("conv:done", dialogue))

			require.Len(t, h.docs, 1)
			require.Contains(t, h.editTexts, fmt.Sprintf("%d:⏳ Обрабатываю…", dialogue))
			require.Equal(t, tt.wantDeleted, slices.Contains(h.deleted, dialogue))
			require.Equal(t, tt.wantDone, slices.Contains(h.editTexts, fmt.Sprintf("%d:Готово ✅", dialogue)))
		})
	}
}

func TestConvertDialogue_photoMovesDialogueDown(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	start := h.lastMsg()
	h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", start))

	h.bot.ProcessUpdate(ctx, photoUpdate(1))

	require.Contains(t, h.lastText(), "До какого размера")
	require.Equal(t, []int{start}, h.deleted, "the previous dialogue message is removed")
}

func TestConvertDialogue_errorStaysInDialogue(t *testing.T) {
	h := newConvHarness(t)
	h.serve("large", []byte("not an image"))
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	start := h.lastMsg()
	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", start))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	dialogue := h.lastMsg()
	sends := h.sends()

	h.bot.ProcessUpdate(ctx, callbackOn("conv:done", dialogue))

	require.Empty(t, h.docs)
	require.Equal(t, sends, h.sends(), "the error is an edit of the dialogue message")
	require.Contains(t, h.lastShown(), "Так я пока не умею")
	h.bot.ProcessUpdate(ctx, callbackOn("conv:cancel", dialogue))
	require.Contains(t, h.lastText(), "Режим конвертера выключен", "the session survived for a retry")
}

func TestConvertDialogue_editAndSendBothFailKeepsMessage(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	start := h.lastMsg()

	h.failEdits, h.failSends = true, true
	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", start))
	h.failEdits, h.failSends = false, false

	h.bot.ProcessUpdate(ctx, callbackOn("conv:cancel", start))
	require.Contains(t, h.lastText(), "Режим конвертера выключен", "the session still owns its dialogue message")
}

func TestConvert_newCommandStripsPreviousDialogue(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	edits := h.edits

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))

	require.Equal(t, edits+1, h.edits, "the old dialogue loses its buttons")
}

func TestConvertPDFActions_offStepInputAtMenu(t *testing.T) {
	h := newConvHarness(t)
	h.serve("pdf-in", testPDF(t, 2))
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdfops", h.lastMsg()))
	h.bot.ProcessUpdate(ctx, namedDocumentUpdate("pdf-in", "application/pdf", "a.pdf"))

	for name, u := range map[string]*models.Update{
		"second pdf": namedDocumentUpdate("pdf-in", "application/pdf", "b.pdf"),
		"photo":      photoUpdate(2),
		"text":       convText("hi"),
	} {
		h.bot.ProcessUpdate(ctx, u)
		require.Contains(t, h.lastText(), "Нажми кнопку", name)
	}
}

// A result Telegram refused is not "done": the progress note turns into an error
// (the dialogue is not deleted) and the user can retry where the step allows it.
func TestConvertDialogue_resultSendFailureKeepsDialogue(t *testing.T) {
	const failed = "Не удалось отправить файл"
	tests := []struct {
		name        string
		run         func(h *convHarness) // ends with the press or message that fails
		wantSession bool                 // the session is back, so Отмена still works
	}{
		{"photos to pdf", func(h *convHarness) {
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", h.lastMsg()))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			h.failDocs = true
			h.bot.ProcessUpdate(ctx, callbackOn("conv:done", h.lastMsg()))
		}, true},
		{"compress", func(h *convHarness) {
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", h.lastMsg()))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			h.failDocs = true
			h.bot.ProcessUpdate(ctx, callbackOn("conv:limit1", h.lastMsg()))
		}, true},
		{"merge", func(h *convHarness) {
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			src := seedPDF(t, h)
			h.serve("pdf-add", testPDF(t, 1))
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":merge"))
			h.bot.ProcessUpdate(ctx, pdfUpdate("pdf-add", 100))
			h.failDocs = true
			h.bot.ProcessUpdate(ctx, callbackOn("conv:done", h.lastMsg()))
		}, true},
		{"page range", func(h *convHarness) {
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			src := seedPDF(t, h)
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":keep"))
			h.failDocs = true
			h.bot.ProcessUpdate(ctx, convText("1"))
		}, true},
		{"pdf menu of /convert", func(h *convHarness) {
			ctx := t.Context()
			h.serve("pdf-in", testPDF(t, 2))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:pdfops", h.lastMsg()))
			h.bot.ProcessUpdate(ctx, namedDocumentUpdate("pdf-in", "application/pdf", "a.pdf"))
			h.failDocs = true
			h.bot.ProcessUpdate(ctx, callbackOn("conv:p:rot", h.lastMsg()))
		}, true},
		{"card quick compress", func(h *convHarness) {
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			src := seedSource(h, docsbot.StatusSaved)
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(src.ID)))
			h.failDocs = true
			h.bot.ProcessUpdate(ctx, callbackOn("conv:q:"+string(src.ID)+":c1", h.lastMsg()))
		}, false},
		{"card rotate", func(h *convHarness) {
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			src := seedPDF(t, h)
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(src.ID)))
			h.failDocs = true
			h.bot.ProcessUpdate(ctx, callbackOn("conv:q:"+string(src.ID)+":rot", h.lastMsg()))
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
			tt.run(h)
			dialogue := h.lastMsg()

			require.Contains(t, h.lastShown(), failed)
			require.NotContains(t, h.deleted, dialogue, "the dialogue message is not removed")
			require.NotContains(t, h.editTexts, fmt.Sprintf("%d:Готово ✅", dialogue))
			if tt.wantSession {
				h.failDocs = false
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:cancel", dialogue))
				require.Contains(t, h.lastText(), "Режим конвертера выключен", "the session is back for a retry")
			}
		})
	}
}

func TestConvertDialogue_moveDownSendFailureKeepsDialogue(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", h.lastMsg()))
	prompt := h.lastMsg()

	h.failSends = true
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.failSends = false

	require.NotContains(t, h.deleted, prompt, "the previous dialogue message stays")
	h.bot.ProcessUpdate(ctx, callbackOn("conv:done", prompt))
	require.Len(t, h.docs, 1, "its buttons still drive the session")
}

// holdQuick starts a quick operation on menu message msgID and holds it in its
// download until the returned function is called, so the menu counts as busy.
func holdQuick(t *testing.T, h *convHarness, id docsbot.DocumentID, msgID int) (release func()) {
	t.Helper()
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.mu.Lock()
	h.gate = make(chan struct{})
	gate := h.gate
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.bot.ProcessUpdate(ctx, callbackOn("conv:q:"+string(id)+":c1", msgID))
	}()
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.downloads) > 0
	}, 5*time.Second, 5*time.Millisecond)
	return func() {
		close(gate)
		<-done
	}
}

func TestConvertMenu_doubleTap(t *testing.T) {
	t.Run("quick op: refused tap only answers stale and keeps buttons", func(t *testing.T) {
		h := newConvHarness(t)
		src := seedSource(h, docsbot.StatusSaved)
		ctx := tg.ContextWithUserID(t.Context(), testUserID)
		release := holdQuick(t, h, src.ID, 55)
		edits := h.edits

		h.bot.ProcessUpdate(ctx, callbackOn("conv:q:"+string(src.ID)+":c1", 55))

		require.Equal(t, "Кнопка устарела, открой заново.", h.lastText())
		require.Equal(t, edits, h.edits, "the keyboard is not cleared")
		release()
		require.Len(t, h.docs, 1, "the first tap ran once")
	})
	t.Run("card button opens no second menu while busy", func(t *testing.T) {
		h := newConvHarness(t)
		src := seedSource(h, docsbot.StatusSaved)
		ctx := tg.ContextWithUserID(t.Context(), testUserID)
		release := holdQuick(t, h, src.ID, 55)
		sends := h.sends()

		h.bot.ProcessUpdate(ctx, callbackOn("conv:doc:"+string(src.ID), 55))

		require.Equal(t, sends, h.sends())
		release()
		h.bot.ProcessUpdate(ctx, callbackOn("conv:doc:"+string(src.ID), 55))
		require.Equal(t, sends+1, h.sends(), "free again: the menu opens")
	})
	t.Run("menu cancel is ignored while busy", func(t *testing.T) {
		h := newConvHarness(t)
		src := seedSource(h, docsbot.StatusSaved)
		ctx := tg.ContextWithUserID(t.Context(), testUserID)
		release := holdQuick(t, h, src.ID, 56)

		h.bot.ProcessUpdate(ctx, callbackOn("conv:x", 56))

		require.NotContains(t, h.deleted, 56)
		release()
	})
}

func TestConvert_cancelStripsDialogueKeyboard(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	edits := h.edits

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "cancel"))

	require.Equal(t, edits+1, h.edits, "the dialogue loses its buttons")
	require.Contains(t, h.lastText(), "Режим конвертера выключен")
}

const staleReply = "Кнопка устарела, открой заново."

// liveMessages lists the sent messages (ids above the harness start) that were
// not deleted.
func (h *convHarness) liveMessages() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	var live []int
	for id := 101; id <= h.nextMsg; id++ {
		if !slices.Contains(h.deleted, id) {
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
			require.Contains(t, h.editTexts, fmt.Sprint(dialogue)+":⏳ Обрабатываю…")
		})
	}
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
