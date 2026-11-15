package telegram_test

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestConnect_screenBeforeGoogle(t *testing.T) {
	b, sent, _ := newBotOpts(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{}, telegram.WithDriveFolder("Мои Доки"))
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	// Settings: the button explains first, it is not the consent link itself.
	b.ProcessUpdate(ctx, commandUpdate("settings"))
	require.Contains(t, sent.markups[0], `"callback_data":"d:cna"`)
	require.NotContains(t, sent.markups[0], "auth.example", "the consent link is built after the user asks")

	b.ProcessUpdate(ctx, dataCallback("d:cna"))

	intro := sent.texts[len(sent.texts)-1]
	require.Equal(t, fmt.Sprintf(telegram.TextConnectIntro, "Мои Доки"), intro)
	kb := sent.markups[len(sent.markups)-1]
	require.Contains(t, kb, `"text":"☁️ Продолжить","url":"https://auth.example/consent"`, "continue is a link button")
	require.NotContains(t, kb, `"callback_data":"d:cna"`)
	require.Contains(t, kb, `"callback_data":"d:cnc"`)
	require.Contains(t, kb, telegram.BtnCancel)
	require.Equal(t, 2, strings.Count(kb, `"text"`), "continue and cancel only")
	require.Empty(t, sent.edits, "the settings message stays as it is")

	b.ProcessUpdate(ctx, dataCallbackMsg(8, "d:cnc"))

	require.Equal(t, telegram.TextConnectLater, sent.edits[len(sent.edits)-1])
	require.Equal(t, []int{8}, sent.editIDs, "the intro message itself is edited")
	require.Contains(t, sent.editMarkups[len(sent.editMarkups)-1], `"inline_keyboard":[]`)
}

func TestConnect_screenBeforeGoogleDefaultFolder(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:cna"))

	require.Contains(t, sent.texts[0], "папке «Most Docs»", "the default folder name is used when none is configured")
}

func TestConnect_staleCancelOnMessageTelegramNoLongerGives(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, inaccessible(dataCallback("d:cnc")))

	require.Empty(t, sent.edits)
	require.Empty(t, sent.texts)
}

func TestConnect_offerAfterFirstDocumentOpensTheScreen(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	id := seedPending(repo, 1)[0]

	b.ProcessUpdate(ctx, dataCallbackMsg(11, "d:cat:"+string(id)+":"+string(docsbot.CatOther)))

	last := sent.markups[len(sent.markups)-1]
	require.Contains(t, last, `"callback_data":"d:cna"`)
	require.NotContains(t, last, "auth.example")
}

func TestConnect_reconnectStaysADirectLink(t *testing.T) {
	b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, commandUpdate("settings"))

	require.Contains(t, sent.markups[0], `"url":"https://auth.example/consent"`)
	require.NotContains(t, sent.markups[0], "d:cna")
}

// An old offer keeps its button after Drive is linked: the press confirms the
// connection instead of explaining a consent that is not needed.
func TestConnect_pressWhenAlreadyConnected(t *testing.T) {
	b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:cna"))

	require.Equal(t, []string{telegram.TextDriveConnected}, sent.snapshot(&sent.texts))
	require.NotContains(t, strings.Join(sent.snapshot(&sent.markups), ""), "auth.example", "no consent link")
}
