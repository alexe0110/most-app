package converter_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram/converter"
	"github.com/alexe0110/most-app/internal/platform/convert"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestInfoFlow_cleanFileOffersNothingToClean(t *testing.T) {
	t.Parallel()
	clean, err := convert.Strip(t.Context(), fixtureFile(t, "gps.jpg"))
	require.NoError(t, err)
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "IMG_0001", clean)

	openInfo(t, h)

	require.Contains(t, h.lastText(), converter.TextInfoClean)
	require.NotContains(t, h.lastText(), "Очистить")
	require.NotContains(t, h.lastMarkup(), "conv:cl:")
	require.Contains(t, h.lastMarkup(), "conv:x")
}

func TestInfoFlow_cleanSendsACopyWithKeepButtons(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := fixtureFile(t, "gps.jpg")
	seedFile(h, "image/jpeg", "Квартира", src)

	cleanFile(t, h)

	sent := h.docs[0]
	require.Equal(t, "Квартира-очищен.jpg", sent.filename)
	require.Contains(t, sent.caption, "Очищенная копия")
	require.Contains(t, sent.caption, "Убрал")
	require.Contains(t, sent.caption, "включая геолокация")
	after, err := convert.Inspect(t.Context(), sent.data)
	require.NoError(t, err)
	require.True(t, after.Clean())
	require.Contains(t, h.docMarkups[0], "Сохранить очищенный")
	require.Contains(t, h.docMarkups[0], converter.BtnKeepOriginal)
	require.Contains(t, h.editTexts[0], "Обрабатываю", "the info screen becomes the progress message")
	require.NotContains(t, h.editMarkups[0], "conv:", "with no buttons while it works")
	require.Contains(t, h.deleted, infoMsg, "and is removed at the end")
	require.Equal(t, []docsbot.Event{{
		User: docsbottest.TestUserID, Name: docsbot.EventFileCleaned, Props: docsbot.EventProps{Format: docsbot.FormatJPEG},
	}}, h.repo.Events)
	require.Equal(t, "file-Квартира", h.repo.Docs[infoDocID].TgFileID, "nothing is replaced before the choice")
}

func TestInfoFlow_everyButtonFitsTelegramLimit(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	openInfo(t, h)
	cleanFile(t, h)

	h.mu.Lock()
	defer h.mu.Unlock()
	checked := 0
	for _, markup := range append(append([]string{}, h.markupBodies...), h.docMarkups...) {
		if markup == "" {
			continue
		}
		var kb struct {
			Rows [][]struct {
				Data string `json:"callback_data"`
			} `json:"inline_keyboard"`
		}
		require.NoError(t, json.Unmarshal([]byte(markup), &kb))
		for _, row := range kb.Rows {
			for _, b := range row {
				require.LessOrEqual(t, len(b.Data), 64, b.Data)
				checked++
			}
		}
	}
	require.GreaterOrEqual(t, checked, 4)
}

func TestInfoFlow_keepCleanedReplacesTheFileThenCategorySavesIt(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "Квартира", fixtureFile(t, "gps.jpg"))
	tok := cleanFile(t, h)
	ctx := infoCtx(t)
	version := h.repo.Docs[infoDocID].MetaVersion

	h.bot.ProcessUpdate(ctx, callbackOn("conv:kc:"+tok, 60))

	got := h.repo.Docs[infoDocID]
	require.Equal(t, "res-0", got.TgFileID, "the cleaned copy that Telegram stored")
	require.Equal(t, "res-uniq-0", got.TgFileUniqueID)
	require.Equal(t, docsbot.StatusPending, got.Status)
	require.Equal(t, version+1, got.MetaVersion)
	require.Equal(t, "В какую категорию сохранить?", h.lastText())
	require.Contains(t, h.lastMarkup(), "d:cat:"+string(infoDocID))

	h.bot.ProcessUpdate(ctx, callbackOn("conv:kc:"+tok, 60)) // double tap

	require.Equal(t, version+1, h.repo.Docs[infoDocID].MetaVersion, "replaced once")
	require.Equal(t, converter.TextConvertResultOld, h.lastText())

	h.bot.ProcessUpdate(ctx, callbackOn("d:cat:"+string(infoDocID)+":"+string(docsbot.CatPassport), 61))

	saved := h.repo.Docs[infoDocID]
	require.Equal(t, docsbot.StatusSaved, saved.Status)
	require.Equal(t, docsbot.CatPassport, saved.Category)
	require.Equal(t, "res-0", saved.TgFileID, "the cleaned copy is what is saved")
}

func TestInfoFlow_keepOriginalLeavesTheFile(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "Квартира", fixtureFile(t, "gps.jpg"))
	tok := cleanFile(t, h)
	m := keepOrigRe.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, m)
	require.Equal(t, tok, m[1])
	ctx := infoCtx(t)

	h.bot.ProcessUpdate(ctx, callbackOn("conv:ko:"+tok, 60))

	require.Equal(t, "file-Квартира", h.repo.Docs[infoDocID].TgFileID)
	require.Equal(t, "В какую категорию сохранить?", h.lastShown())

	h.bot.ProcessUpdate(ctx, callbackOn("conv:kc:"+tok, 60))

	require.Equal(t, "file-Квартира", h.repo.Docs[infoDocID].TgFileID, "the choice is made once")
	require.Equal(t, converter.TextConvertResultOld, h.lastText())
}

func TestInfoFlow_keepCleanedOfADeletedDocument(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	tok := cleanFile(t, h)
	delete(h.repo.Docs, infoDocID)

	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:kc:"+tok, 60))

	require.Equal(t, converter.TextGone, h.lastText())
	require.Empty(t, h.repo.Docs, "nothing is resurrected")
}

func TestInfoFlow_someoneElsesDocumentAndTokens(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	tok := cleanFile(t, h)
	downloads := len(h.downloads)
	stranger := tg.ContextWithUserID(t.Context(), "22222222-2222-2222-2222-222222222222")
	press := func(data string, msgID int) {
		u := callbackOn(data, msgID)
		u.CallbackQuery.From.ID = 99
		h.bot.ProcessUpdate(stranger, u)
	}

	press("conv:i:"+string(infoDocID), 70)
	require.Equal(t, converter.TextGone, h.lastText())
	press("conv:cl:"+string(infoDocID), 71)
	require.Equal(t, converter.TextGone, h.lastText())
	press("conv:kc:"+tok, 72)
	require.Equal(t, converter.TextConvertResultOld, h.lastText(), "a token belongs to the user it was made for")
	press("conv:ko:"+tok, 73)

	require.Equal(t, downloads, len(h.downloads), "a stranger can't make the bot read the file")
	require.Len(t, h.docs, 1, "and gets no copy")
	require.Equal(t, "file-a", h.repo.Docs[infoDocID].TgFileID)

	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:kc:"+tok, 60))
	require.Equal(t, "res-0", h.repo.Docs[infoDocID].TgFileID, "the owner's token still works")
}

func TestInfoFlow_sendFailureKeepsTheCleanButton(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	h.failDocs = true

	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:cl:"+string(infoDocID), infoMsg))

	require.Contains(t, h.lastShown(), "Не удалось отправить файл")
	require.Contains(t, h.editMarkups[len(h.editMarkups)-1], "conv:cl:", "retry is one tap away")
	require.Empty(t, h.repo.Events, "a copy that was not delivered is not counted")
}

func TestInfoFlow_doubleTapOnCleanSendsOneCopy(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	h.mu.Lock()
	h.gate = make(chan struct{})
	gate := h.gate
	h.mu.Unlock()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:cl:"+string(infoDocID), infoMsg))
	}()
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.downloads) > 0
	}, 5*time.Second, 5*time.Millisecond)

	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:cl:"+string(infoDocID), infoMsg)) // second tap while the first is busy
	close(gate)
	wg.Wait()

	require.Len(t, h.docs, 1, "one copy however many taps")
	require.Len(t, h.repo.Events, 1)
}

func TestInfoFlow_concurrentTapsOnTheKeepButtons(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	tok := cleanFile(t, h)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := "conv:kc:" + tok
			if i%2 == 1 {
				data = "conv:ko:" + tok
			}
			h.bot.ProcessUpdate(infoCtx(t), callbackOn(data, 60+i))
		}()
	}
	wg.Wait()

	got := h.repo.Docs[infoDocID].TgFileID
	require.Contains(t, []string{"file-a", "res-0"}, got, "one choice won, whichever it was")
	h.mu.Lock()
	defer h.mu.Unlock()
	pickers := 0
	for _, text := range h.texts {
		if text == "В какую категорию сохранить?" {
			pickers++
		}
	}
	require.Equal(t, 1, pickers, "the choice is made once")
}
