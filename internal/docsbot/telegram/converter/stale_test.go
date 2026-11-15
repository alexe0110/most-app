package converter_test

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// lastMsg is the id of the last message the bot sent.
func (h *convHarness) lastMsg() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nextMsg
}

func TestConvert_staleButtons(t *testing.T) {
	t.Parallel()
	const staleText = "Эта кнопка уже не работает. Открой документ заново."

	tests := []struct {
		name string
		// run drives the dialogue and returns the press that must be stale and
		// the number of documents expected afterwards.
		run      func(h *convHarness) (stale func(), wantDocs int)
		followUp func(h *convHarness, dialogue int) // a valid press that must still work
		wantDocs int                                // after followUp
	}{
		{
			name: "second tap of a limit button",
			run: func(h *convHarness) (func(), int) {
				h.servePNG()
				h.bot.ProcessUpdate(t.Context(), commandUpdate("convert"))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:compress", h.lastMsg()))
				h.bot.ProcessUpdate(t.Context(), pngDocumentUpdate())
				limitMsg := h.lastMsg()
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:limit2", limitMsg))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:limit2", limitMsg)) }, 0
			},
			followUp: func(h *convHarness, dialogue int) {
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:fmtjpeg", dialogue))
			},
			wantDocs: 1,
		},
		{
			name: "second tap of a format button",
			run: func(h *convHarness) (func(), int) {
				h.servePNG()
				h.bot.ProcessUpdate(t.Context(), commandUpdate("convert"))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:compress", h.lastMsg()))
				h.bot.ProcessUpdate(t.Context(), pngDocumentUpdate())
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:limit2", h.lastMsg()))
				fmtMsg := h.lastMsg()
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:fmtjpeg", fmtMsg))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:fmtjpeg", fmtMsg)) }, 1
			},
			wantDocs: 1,
		},
		{
			name: "format button at the limit step",
			run: func(h *convHarness) (func(), int) {
				h.servePNG()
				h.bot.ProcessUpdate(t.Context(), commandUpdate("convert"))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:compress", h.lastMsg()))
				h.bot.ProcessUpdate(t.Context(), pngDocumentUpdate())
				limitMsg := h.lastMsg()
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:fmtjpeg", limitMsg)) }, 0
			},
			followUp: func(h *convHarness, dialogue int) {
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:limit1", dialogue))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:fmtjpeg", dialogue))
			},
			wantDocs: 1,
		},
		{
			name: "second tap of the PDF button",
			run: func(h *convHarness) (func(), int) {
				h.bot.ProcessUpdate(t.Context(), commandUpdate("convert"))
				startMsg := h.lastMsg()
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", startMsg))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", startMsg)) }, 0
			},
			followUp: func(h *convHarness, _ int) {
				h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:done", h.lastMsg()))
			},
			wantDocs: 1,
		},
		{
			name: "compress button after the PDF button",
			run: func(h *convHarness) (func(), int) {
				h.bot.ProcessUpdate(t.Context(), commandUpdate("convert"))
				startMsg := h.lastMsg()
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", startMsg))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:compress", startMsg)) }, 0
			},
			followUp: func(h *convHarness, _ int) {
				h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:done", h.lastMsg()))
			},
			wantDocs: 1,
		},
		{
			name: "second tap of done",
			run: func(h *convHarness) (func(), int) {
				h.bot.ProcessUpdate(t.Context(), commandUpdate("convert"))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", h.lastMsg()))
				h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
				doneMsg := h.lastMsg()
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:done", doneMsg))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:done", doneMsg)) }, 1
			},
			wantDocs: 1,
		},
		{
			name: "tap after cancel",
			run: func(h *convHarness) (func(), int) {
				h.bot.ProcessUpdate(t.Context(), commandUpdate("convert"))
				startMsg := h.lastMsg()
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:cancel", startMsg))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", startMsg)) }, 0
			},
			wantDocs: 0,
		},
		{
			name: "tap on an old message",
			run: func(h *convHarness) (func(), int) {
				h.bot.ProcessUpdate(t.Context(), commandUpdate("convert"))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", 9999)) }, 0
			},
			followUp: func(h *convHarness, dialogue int) {
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", dialogue))
				h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:done", h.lastMsg()))
			},
			wantDocs: 1,
		},
		{
			name: "cancel on an old message",
			run: func(h *convHarness) (func(), int) {
				h.bot.ProcessUpdate(t.Context(), commandUpdate("convert"))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:cancel", 9999)) }, 0
			},
			followUp: func(h *convHarness, dialogue int) {
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", dialogue))
				h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:done", h.lastMsg()))
			},
			wantDocs: 1,
		},
		{
			name: "no session at all",
			run: func(h *convHarness) (func(), int) {
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:fmtpng", 5)) }, 0
			},
			wantDocs: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			press, docsBefore := tt.run(h)
			require.Len(t, h.docs, docsBefore)
			edits, dialogue := h.edits, h.lastMsg()

			press()

			require.Equal(t, staleText, h.lastText())
			require.Len(t, h.docs, docsBefore, "a stale press does no work")
			require.Equal(t, dialogue+1, h.lastMsg(), "only the stale notice is sent")
			require.Equal(t, edits+1, h.edits, "the stale message loses its buttons")

			if tt.followUp != nil {
				tt.followUp(h, dialogue)
			}
			require.Len(t, h.docs, tt.wantDocs, "the session is unchanged by a stale press")
		})
	}
}

// pngDocumentUpdate sends the file served by servePNG as a PNG document.
func pngDocumentUpdate() *models.Update {
	return namedDocumentUpdate("large", "image/png", "фото.png")
}

// servePNG makes file "large" download as a PNG that is small to decode and big
// to send, so that compressing it shrinks it.
func (h *convHarness) servePNG() {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	h.serve("large", padPNG(buf.Bytes(), bigPhotoSize))
}

// /convert again replaces the session: buttons of the old dialogue are stale.
func TestConvertDialogue_restartKillsOldButtons(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	old := h.lastMsg()
	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	fresh := h.lastMsg()
	require.NotEqual(t, old, fresh)

	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", old))
	require.Equal(t, staleReply, h.lastText())
	require.NotContains(t, h.editTexts, fmt.Sprint(old)+":Пришли фото")

	h.bot.ProcessUpdate(ctx, callbackOn("conv:pdf", fresh))
	require.Contains(t, h.lastShown(), "Пришли фото")
}

// inaccessible is a press on a message Telegram no longer gives (too old).
func inaccessible(u *models.Update) *models.Update {
	u.CallbackQuery.Message = models.MaybeInaccessibleMessage{
		Type:                models.MaybeInaccessibleMessageTypeInaccessibleMessage,
		InaccessibleMessage: &models.InaccessibleMessage{Chat: models.Chat{ID: testChat, Type: models.ChatTypePrivate}},
	}
	return u
}

func TestConvert_inaccessibleMessageDoesNothing(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := compressFromMenu(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	before := append([]string(nil), h.events...)
	edits, docs, stored := h.edits, len(h.docs), len(h.repo.Docs)

	for _, data := range []string{
		"conv:doc:" + string(src.ID), "conv:q:" + string(src.ID) + ":c1", "conv:x",
		"conv:sv:" + tok, "conv:save:" + tok, "conv:skip:" + tok, "conv:rp:" + tok, "conv:rpy:" + tok, "conv:rpn:" + tok, "conv:alb:" + tok,
		"conv:pdf", "conv:pdfops", "conv:p:rot", "conv:compress", "conv:done", "conv:cancel",
		"conv:limit1", "conv:limitcustom", "conv:fmtjpeg", "conv:fmtpng", "conv:zzz", "conv:",
	} {
		t.Run(data, func(t *testing.T) {
			h.bot.ProcessUpdate(ctx, inaccessible(callbackUpdate(data)))

			require.Equal(t, before, h.events, "nothing is sent, edited or deleted")
			require.Equal(t, edits, h.edits)
			require.Len(t, h.docs, docs, "no conversion ran")
			require.Len(t, h.repo.Docs, stored, "nothing was saved")
		})
	}

	// None of it burned the result: the token still replaces from a real message.
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rp:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))
	require.Equal(t, "res-uniq-0", h.repo.Docs[src.ID].TgFileUniqueID)
}

func TestConvert_unknownActionsAreStale(t *testing.T) {
	t.Parallel()
	const staleText = "Эта кнопка уже не работает. Открой документ заново."

	for _, data := range []string{"conv:zzz", "conv:", "conv:p:", "conv:p:rot:x", "conv:p:zzz", "conv:fmt", "conv:limit3"} {
		t.Run(data, func(t *testing.T) {
			h := newConvHarness(t)
			h.bot.ProcessUpdate(t.Context(), commandUpdate("convert"))
			dialogue := h.lastMsg()
			before := len(h.events)

			h.bot.ProcessUpdate(t.Context(), callbackOn(data, dialogue))

			require.Equal(t, staleText, h.lastText())
			require.Len(t, h.events, before+1, "only the stale notice")
			require.Empty(t, h.docs)
			// The dialogue is still alive for its real buttons.
			h.bot.ProcessUpdate(t.Context(), callbackOn("conv:compress", dialogue))
			require.Contains(t, h.lastShown(), "Пришли")
		})
	}
}
