package telegram_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestConvertQA_saveAfterSourceDeleted(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := compressFromMenu(t, h, src.ID)
	delete(h.repo.docs, src.ID)

	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok))

	require.Contains(t, h.lastText(), "уже нет")
	require.Empty(t, h.repo.docs, "nothing was created")
}

func TestConvertQA_unknownOrGarbageTokens(t *testing.T) {
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	for _, data := range []string{"conv:save:", "conv:save:deadbeef", "conv:skip:nope", "conv:alb:zzz"} {
		h.bot.ProcessUpdate(ctx, callbackUpdate(data))
	}

	require.Empty(t, h.repo.docs)
	require.Empty(t, h.docs)
}

func TestConvertQA_saveHitsPendingLimit(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := compressFromMenu(t, h, src.ID)
	for i := 0; i < 20; i++ {
		h.repo.seed(docsbot.Document{
			UserID: testUserID, Status: docsbot.StatusPending, Kind: docsbot.KindPhoto,
			Title: "p", TgFileID: fmt.Sprintf("p%d", i), TgFileUniqueID: fmt.Sprintf("pu%d", i),
		})
	}
	before := len(h.repo.docs)

	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok))

	require.Len(t, h.repo.docs, before, "nothing added past the limit")
	require.Contains(t, h.lastText(), "категорию")
}

func TestConvertQA_quickOnMalformedData(t *testing.T) {
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	for _, data := range []string{"conv:q:", "conv:q:abc", "conv:q::c1", "conv:doc:", "conv:q:abc:zzz"} {
		h.bot.ProcessUpdate(ctx, callbackUpdate(data))
	}
	require.Empty(t, h.docs)
	require.Empty(t, h.repo.docs)
}
