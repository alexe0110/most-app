package telegram_test

import (
	"bytes"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConvert_compressPreset(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limit2"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtjpeg"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "фото-сжатый.jpg", h.docs[0].filename)
	require.LessOrEqual(t, len(h.docs[0].data), 2<<20)
	_, _, err := image.Decode(bytes.NewReader(h.docs[0].data))
	require.NoError(t, err)
	require.Empty(t, h.repo.docs, "a result is not saved unless asked")
}

func TestConvert_compressCustomLimit(t *testing.T) {
	h := newConvHarness(t)
	h.servePNG()
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limitcustom"))
	h.bot.ProcessUpdate(ctx, convText("300"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtpng"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "фото-сжатый.png", h.docs[0].filename)
}

func TestConvert_customLimitRejectsJunk(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
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
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))

	h.mu.Lock()
	h.gate = make(chan struct{})
	gate := h.gate
	h.mu.Unlock()
	var wg sync.WaitGroup
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.bot.ProcessUpdate(ctx, photoUpdate(i))
		}()
	}
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.downloads) == 2
	}, 5*time.Second, 5*time.Millisecond)
	close(gate)
	wg.Wait()

	require.Equal(t, 1, strings.Count(h.allTexts(), "До какого размера сжать?"))
}
