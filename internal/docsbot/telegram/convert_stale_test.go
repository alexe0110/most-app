package telegram_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// lastMsg is the id of the last message the bot sent.
func (h *convHarness) lastMsg() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nextMsg
}

func TestConvert_staleButtons(t *testing.T) {
	const staleText = "Кнопка устарела, открой заново."

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
				h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:compress", h.lastMsg()))
				h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
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
				h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:compress", h.lastMsg()))
				h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
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
				h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:compress", h.lastMsg()))
				h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
				limitMsg := h.lastMsg()
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:fmtjpeg", limitMsg)) }, 0
			},
			followUp: func(h *convHarness, dialogue int) {
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:limit1", dialogue))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:fmtjpeg", h.lastMsg()))
			},
			wantDocs: 1,
		},
		{
			name: "second tap of the PDF button",
			run: func(h *convHarness) (func(), int) {
				h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
				startMsg := h.lastMsg()
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", startMsg))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", startMsg)) }, 0
			},
			followUp: func(h *convHarness, dialogue int) {
				h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:done", dialogue))
			},
			wantDocs: 1,
		},
		{
			name: "compress button after the PDF button",
			run: func(h *convHarness) (func(), int) {
				h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
				startMsg := h.lastMsg()
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", startMsg))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:compress", startMsg)) }, 0
			},
			followUp: func(h *convHarness, dialogue int) {
				h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:done", dialogue))
			},
			wantDocs: 1,
		},
		{
			name: "second tap of done",
			run: func(h *convHarness) (func(), int) {
				h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", h.lastMsg()))
				doneMsg := h.lastMsg()
				h.bot.ProcessUpdate(t.Context(), photoUpdate(1))
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:done", doneMsg))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:done", doneMsg)) }, 1
			},
			wantDocs: 1,
		},
		{
			name: "tap after cancel",
			run: func(h *convHarness) (func(), int) {
				h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
				startMsg := h.lastMsg()
				h.bot.ProcessUpdate(t.Context(), callbackOn("conv:cancel", startMsg))
				return func() { h.bot.ProcessUpdate(t.Context(), callbackOn("conv:pdf", startMsg)) }, 0
			},
			wantDocs: 0,
		},
		{
			name: "tap on an old message",
			run: func(h *convHarness) (func(), int) {
				h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
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
				h.bot.ProcessUpdate(t.Context(), commandUpdate(testChat, "convert"))
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
