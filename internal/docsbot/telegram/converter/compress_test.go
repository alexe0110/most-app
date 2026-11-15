package converter_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram/converter"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestConvert_compressPreset(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.serveBig("large")
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limit2"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtjpeg"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "фото-сжатый.jpg", h.docs[0].filename)
	require.LessOrEqual(t, len(h.docs[0].data), 2<<20)
	require.Regexp(t, `^Готово: [\d,]+\x{00A0}(КБ|МБ) \(было [\d,]+\x{00A0}(КБ|МБ)\)\.$`, h.docs[0].caption)
	_, _, err := image.Decode(bytes.NewReader(h.docs[0].data))
	require.NoError(t, err)
	require.Empty(t, h.repo.Docs, "a result is not saved unless asked")
}

func TestConvert_compressCustomLimit(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.servePNG()
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, pngDocumentUpdate())
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limitcustom"))
	h.bot.ProcessUpdate(ctx, convText("300"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtpng"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "фото-сжатый.png", h.docs[0].filename)
}

func TestConvert_customLimitRejectsJunk(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limitcustom"))
	h.bot.ProcessUpdate(ctx, convText("не число"))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastText(), "число")
}

// Two images sent in a row in compress mode: only the first moves the dialogue.
func TestConvert_compressTwoImagesAskLimitOnce(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))

	var wg sync.WaitGroup
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.bot.ProcessUpdate(ctx, photoUpdate(i))
		}()
	}
	wg.Wait()

	require.Empty(t, h.downloads, "the image is downloaded when the compression runs")
	require.Equal(t, 1, strings.Count(h.allTexts(), converter.TextConvertChooseLimit))
}

// An image that already fits the limit is not sent back: it would only grow. The
// user is told, and keeps the buttons to choose another size.
func TestConvert_compressNothingToShrink(t *testing.T) {
	t.Parallel()
	want := fmt.Sprintf(converter.TextCompressNothing, "1,0\u00A0МБ")
	tests := []struct {
		name string
		run  func(t *testing.T, h *convHarness) (retry string) // returns the callback prefix of the restored buttons
	}{
		{"dialogue", func(t *testing.T, h *convHarness) string {
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", h.lastMsg()))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:limit1", h.lastMsg()))
			return "conv:limit1"
		}},
		{"menu of a stored document", func(t *testing.T, h *convHarness) string {
			src := seedSource(h, docsbot.StatusSaved)
			h.serve("src", h.photo) // small
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(src.ID)))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:q:"+string(src.ID)+":c1", h.lastMsg()))
			return "conv:q:" + string(src.ID) + ":c1"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)

			retry := tt.run(t, h)

			require.Empty(t, h.docs, "no file is sent")
			require.Equal(t, want, h.lastShown())
			h.mu.Lock()
			last := h.editMarkups[len(h.editMarkups)-1]
			h.mu.Unlock()
			require.Contains(t, last, retry, "the buttons come back")
			require.NotContains(t, h.allTexts(), "Меньше не вышло")
			require.Empty(t, h.repo.Events, "nothing was converted")
			require.NotContains(t, h.logs.String(), "document converted")
		})
	}
}

// A real shrink is still sent with its caption and no "nothing to compress" note.
func TestConvert_compressShrinkIsSent(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.serveBig("large")
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", h.lastMsg()))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackOn("conv:limit1", h.lastMsg()))

	require.Len(t, h.docs, 1)
	require.Less(t, len(h.docs[0].data), bigPhotoSize)
	require.NotEmpty(t, h.docMarkups[0], "with its buttons")
	require.NotContains(t, h.allTexts(), "сжимать нечего")
	require.Len(t, h.repo.Events, 1)
}

// The session keeps the file id: the image is fetched when the compression runs,
// and a failed fetch leaves the buttons so the user can try again.
func TestConvert_compressDownloadsWhenItRuns(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.serveBig("large")
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", h.lastMsg()))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	limitMsg := h.lastMsg()
	require.Empty(t, h.downloads)

	h.mu.Lock()
	h.failDownloads = true
	h.mu.Unlock()
	h.bot.ProcessUpdate(ctx, callbackOn("conv:limit2", limitMsg))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastShown(), "Не удалось получить файл")

	h.mu.Lock()
	h.failDownloads = false
	h.mu.Unlock()
	h.bot.ProcessUpdate(ctx, callbackOn("conv:limit2", h.lastMsg()))

	require.Len(t, h.docs, 1)
	require.Equal(t, []string{"large", "large"}, h.downloads)
}

// Whatever goes wrong when the file is fetched at execution time (the file is
// gone, it grew past the Bot API limit, Telegram is slow), the user is told once,
// nothing is sent, and the session stays so the same tap can be retried.
func TestConvert_compressDownloadFailureKeepsTheSession(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"file is gone", errors.New("get file: Bad Request: wrong file_id")},
		{"file too large", fmt.Errorf("download: %w", tg.ErrFileTooLarge)},
		{"timeout", fmt.Errorf("get file: %w", context.DeadlineExceeded)},
		{"cancelled", context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			h.serveBig("large")
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", h.lastMsg()))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			limitMsg := h.lastMsg()

			h.mu.Lock()
			h.downloadErr = tt.err
			h.mu.Unlock()
			h.bot.ProcessUpdate(ctx, callbackOn("conv:limit2", limitMsg))

			require.Empty(t, h.docs)
			require.Contains(t, h.lastShown(), "Не удалось получить файл")

			h.mu.Lock()
			h.downloadErr = nil
			h.mu.Unlock()
			h.bot.ProcessUpdate(ctx, callbackOn("conv:limit2", h.lastMsg()))

			require.Len(t, h.docs, 1, "the retry works on the same session")
		})
	}
}

// A stale tap after a failed fetch must not run the compression twice or on a
// session that is not at the format step.
func TestConvert_compressTapAfterFailureOnOldMessageIsStale(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := t.Context()
	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", h.lastMsg()))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	limitMsg := h.lastMsg()
	h.bot.ProcessUpdate(ctx, callbackOn("conv:cancel", limitMsg))

	h.bot.ProcessUpdate(ctx, callbackOn("conv:limit2", limitMsg))

	require.Empty(t, h.docs)
	require.Empty(t, h.downloads, "a cancelled session downloads nothing")
}

// The Bot API would refuse to serve a file over its limit, so a compress image
// declared larger is refused at once and the session keeps waiting for an image.
// An undeclared size (0) is let through: the fetch decides later.
func TestConvert_compressImageSizeAtIntake(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		size      int64
		wantLimit bool
	}{
		{"size unknown", 0, true},
		{"exactly the limit", tg.MaxFileBytes, true},
		{"one byte over", tg.MaxFileBytes + 1, false},
		{"far over", 1 << 40, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
			h.bot.ProcessUpdate(ctx, callbackOn("conv:compress", h.lastMsg()))
			up := documentUpdate("image/png")
			up.Message.Document.FileSize = tt.size

			h.bot.ProcessUpdate(ctx, up)

			require.Empty(t, h.downloads)
			if tt.wantLimit {
				require.Contains(t, h.allTexts(), converter.TextConvertChooseLimit)
				return
			}
			require.NotContains(t, h.allTexts(), converter.TextConvertChooseLimit)
			require.Contains(t, h.lastText(), "Не удалось получить файл")

			h.bot.ProcessUpdate(ctx, photoUpdate(2))
			require.Contains(t, h.allTexts(), converter.TextConvertChooseLimit, "the session still waits for an image")
		})
	}
}
