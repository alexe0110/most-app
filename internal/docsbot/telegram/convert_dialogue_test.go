package telegram_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
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
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	sendsBefore := h.sends()
	h.bot.ProcessUpdate(ctx, callbackOn("conv:done", moved))

	require.Len(t, h.docs, 1)
	require.Equal(t, sendsBefore, h.sends(), "the progress is an edit of the new message")
	require.Contains(t, h.editTexts, fmt.Sprintf("%d:⏳ Обрабатываю…", moved))
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

			h.bot.ProcessUpdate(ctx, callbackOn("conv:done", start))

			require.Len(t, h.docs, 1)
			require.Contains(t, h.editTexts, fmt.Sprintf("%d:⏳ Обрабатываю…", start))
			require.Equal(t, tt.wantDeleted, containsInt(h.deleted, start))
			require.Equal(t, tt.wantDone, containsString(h.editTexts, fmt.Sprintf("%d:Готово ✅", start)))
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
	sends := h.sends()

	h.bot.ProcessUpdate(ctx, callbackOn("conv:done", start))

	require.Empty(t, h.docs)
	require.Equal(t, sends, h.sends(), "the error is an edit of the dialogue message")
	require.Contains(t, h.lastShown(), "Так я пока не умею")
	h.bot.ProcessUpdate(ctx, callbackOn("conv:cancel", start))
	require.Contains(t, h.lastText(), "Режим конвертера выключен", "the session survived for a retry")
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
