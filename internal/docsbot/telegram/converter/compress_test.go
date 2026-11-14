package converter_test

import (
	"bytes"
	"image"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvert_compressPreset(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limit2"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtjpeg"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "фото-сжатый.jpg", h.docs[0].filename)
	require.LessOrEqual(t, len(h.docs[0].data), 2<<20)
	_, _, err := image.Decode(bytes.NewReader(h.docs[0].data))
	require.NoError(t, err)
	require.Empty(t, h.repo.Docs, "a result is not saved unless asked")
}

func TestConvert_compressCustomLimit(t *testing.T) {
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
	require.Equal(t, 1, strings.Count(h.allTexts(), "До какого размера сжать?"))
}

// The session keeps the file id: the image is fetched when the compression runs,
// and a failed fetch leaves the buttons so the user can try again.
func TestConvert_compressDownloadsWhenItRuns(t *testing.T) {
	h := newConvHarness(t)
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
