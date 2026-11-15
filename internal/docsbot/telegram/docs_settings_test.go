package telegram_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestDeleteData_revokeFailureStillConfirms(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{RevokeErr: errors.New("google down"), DisconnectErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})

	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))

	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.False(t, has(sent.texts, "Что-то пошло не так"))
	require.Empty(t, repo.Docs)
}

func TestDeleteData_revokeFailureIsWarnedAndConfirmed(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, sent, _ := newBotWith(t, log, docsbottest.FakeDrive{RevokeErr: errors.New("google down"), DisconnectErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))

	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.Contains(t, buf.String(), `"level":"WARN"`)
	require.Contains(t, buf.String(), "drive revoke incomplete")
	require.NotContains(t, buf.String(), "delete all data", "not treated as a failure")
}

func TestStart_driveFolder(t *testing.T) {
	b, sent, _ := newBotOpts(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{}, telegram.WithDriveFolder("Most Docs Test"))

	b.ProcessUpdate(t.Context(), commandUpdate("start"))

	require.True(t, has(sent.texts, "«Most Docs Test»"), "sent %q", sent.texts)
}

func TestSettings_showsCountsAndControls(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, Title: "p", TgFileUniqueID: "u1"})
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "o", TgFileUniqueID: "u2"})

	b.ProcessUpdate(ctx, commandUpdate("settings"))

	require.True(t, has(sent.texts, "Google Диск"), "sent %q", sent.texts)
	require.True(t, has(sent.texts, fmt.Sprintf(telegram.TextSettingsTotal, 2)), "sent %q", sent.texts)
}

func TestDeleteData_confirmation(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})

	// Tapping "Удалить мои данные" asks for the exact word.
	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	require.True(t, has(sent.texts, "УДАЛИТЬ"), "prompt asks for the word: %q", sent.texts)

	// A wrong word cancels and leaves the data in place.
	b.ProcessUpdate(ctx, textUpdate("не надо"))
	require.True(t, has(sent.texts, "отменено"), "sent %q", sent.texts)
	require.Len(t, repo.Docs, 1, "data intact after a wrong word")

	// Re-arm, then the exact word deletes everything.
	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))
	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.Empty(t, repo.Docs, "data deleted after the exact word")
}

func TestDeleteData_promptOffersExportFirst(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dd"))

	require.Len(t, sent.texts, 1)
	require.Contains(t, sent.texts[0], "УДАЛИТЬ")
	require.Contains(t, sent.texts[0], "/export")
	require.Contains(t, sent.markups[0], "d:exp")
	require.Contains(t, sent.markups[0], telegram.BtnExportAll)
}

func startUpdate(payload string) *models.Update {
	u := textUpdate("/start " + payload)
	u.Message.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: len("/start")}}
	return u
}

func TestStart_recordsSource(t *testing.T) {
	yes := true
	tests := []struct {
		name    string
		payload string
		want    docsbot.EventProps
	}{
		{"campaign", "ad_1", docsbot.EventProps{Source: "ad_1", New: &yes}},
		{"no payload", "", docsbot.EventProps{New: &yes}},
		{"malformed payload", "Ad-1", docsbot.EventProps{New: &yes}},
		{"connected redirect is navigation", "connected", docsbot.EventProps{New: &yes}},
		{"inline button is navigation", "inline", docsbot.EventProps{New: &yes}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)

			b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), startUpdate(tt.payload))

			require.Equal(t, []docsbot.Event{{User: docsbottest.TestUserID, Name: docsbot.EventStart, Props: tt.want}}, repo.Events)
			require.NotEmpty(t, sent.texts, "the greeting is sent as before")
		})
	}
}

func TestStart_eventFailureStillGreets(t *testing.T) {
	b, sent, repo := newBot(t)
	repo.EventsErr = errors.New("db down")

	b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), startUpdate("ad_1"))

	require.True(t, has(sent.texts, "Most Docs"), "sent %q", sent.texts)
}

func TestDeleteData_confirmWordIgnoresCaseAndSpaces(t *testing.T) {
	tests := []struct {
		name        string
		reply       string
		wantDeleted bool
	}{
		{"exact", "УДАЛИТЬ", true},
		{"lower case", "удалить", true},
		{"mixed case", "Удалить", true},
		{"surrounding spaces", "  Удалить ", true},
		{"surrounding newline", "удалить\n", true},
		{"another word", "не надо", false},
		{"word inside a sentence", "удалить всё", false},
		{"word with a typo", "удалитъ", false},
		{"latin lookalike", "УДАЛИTЬ", false},
		{"empty", "", false},
		{"only spaces", "   ", false},
		{"command", "/start", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})
			b.ProcessUpdate(ctx, dataCallback("d:dd"))

			b.ProcessUpdate(ctx, textUpdate(tt.reply))

			if tt.wantDeleted {
				require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
				require.Empty(t, repo.Docs)
				return
			}
			require.Len(t, repo.Docs, 1, "data intact")
			require.False(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
		})
	}
}

func TestDeleteData_confirmationIsSingleUse(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})

	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	b.ProcessUpdate(ctx, textUpdate("удалить"))
	b.ProcessUpdate(ctx, textUpdate("удалить"))

	require.Equal(t, 1, strings.Count(strings.Join(sent.snapshot(&sent.texts), "\n"), "Данные удалены"))
	require.True(t, has(sent.texts, "Время на подтверждение вышло"), "the second word is not a confirmation any more: %q", sent.texts)
}

func TestDeleteData_wordWithoutPromptIsNotASearch(t *testing.T) {
	tests := []struct {
		name  string
		reply string
	}{
		{"upper case", "УДАЛИТЬ"},
		{"lower case", "удалить"},
		{"spaces", " Удалить "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})

			b.ProcessUpdate(ctx, textUpdate(tt.reply))

			require.Equal(t, []string{telegram.TextDeleteExpired}, sent.snapshot(&sent.texts))
			require.Len(t, repo.Docs, 1, "nothing is deleted")
			require.Empty(t, repo.Events, "not counted as a search")
		})
	}
}

func TestDeleteData_otherWordWithoutPromptIsASearch(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	seedPassport(repo, "Удалить после оплаты", "u1")

	b.ProcessUpdate(ctx, textUpdate("удалить после"))

	require.Equal(t, []string{fmt.Sprintf(telegram.TextFoundTitle, 1, telegram.PluralDocumentOne)}, sent.snapshot(&sent.texts))
}

func TestDeleteData_cancelButton(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})
	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	require.Contains(t, sent.markups[0], "d:ddn", "the prompt has a cancel button")
	require.Contains(t, sent.markups[0], telegram.BtnCancel)

	b.ProcessUpdate(ctx, dataCallback("d:ddn"))

	require.Equal(t, telegram.TextDeleteWrongWord, sent.edits[len(sent.edits)-1], "the prompt turns into the note")
	require.Contains(t, sent.editMarkups[len(sent.editMarkups)-1], `"inline_keyboard":[]`)
	require.Equal(t, []int{7}, sent.editIDs)

	// The confirmation is disarmed: the word no longer deletes anything.
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))
	require.Len(t, repo.Docs, 1)
	require.False(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.True(t, has(sent.texts, "Время на подтверждение вышло"), "sent %q", sent.texts)
}

func TestDeleteData_cancelButtonWithoutArmedConfirmation(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})

	b.ProcessUpdate(ctx, dataCallback("d:ddn"))
	b.ProcessUpdate(ctx, inaccessible(dataCallback("d:ddn")))

	require.Len(t, repo.Docs, 1)
	require.Empty(t, sent.edits, "nothing is armed: no note is shown")
	require.Len(t, sent.markupEdits, 1, "the buttons are removed once; a message Telegram no longer gives is left alone")
}

// The prompt keeps its buttons after the data is gone; a late tap on "Отмена"
// must not claim that the data is still there.
func TestDeleteData_cancelButtonAfterDeletionDoesNotPromiseTheData(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})
	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))
	require.Empty(t, repo.Docs)

	b.ProcessUpdate(ctx, dataCallback("d:ddn"))

	require.NotContains(t, strings.Join(sent.snapshot(&sent.edits), "\n"), "данные на месте")
}

func TestHelp_optionalLines(t *testing.T) {
	tests := []struct {
		name        string
		opts        []telegram.Option
		wantInline  bool
		wantPrivacy bool
	}{
		{"no options", nil, false, false},
		{"empty username and url", []telegram.Option{telegram.WithBotUsername(""), telegram.WithPrivacyURL("")}, false, false},
		{"username", []telegram.Option{telegram.WithBotUsername("most_files_bot")}, true, false},
		{"privacy url", []telegram.Option{telegram.WithPrivacyURL("https://most.example/privacy")}, false, true},
		{"both", []telegram.Option{telegram.WithBotUsername("most_files_bot"), telegram.WithPrivacyURL("https://most.example/privacy")}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, _ := newBotOpts(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{}, tt.opts...)

			b.ProcessUpdate(t.Context(), commandUpdate("help"))

			require.Len(t, sent.texts, 1)
			text := sent.texts[0]
			require.Equal(t, tt.wantInline, strings.Contains(text, "@most_files_bot"), text)
			require.Equal(t, tt.wantPrivacy, strings.Contains(text, fmt.Sprintf(telegram.TextPrivacy, "https://most.example/privacy")), text)
			require.NotContains(t, text, "%!", "no broken format verbs")
			require.NotContains(t, text, "%s")
			require.NotContains(t, text, "@\n", "no empty username")
			require.LessOrEqual(t, utf8.RuneCountInString(text), 4096, "fits one Telegram message")
			require.True(t, strings.HasSuffix(text, "на твоём Google Диске.") || tt.wantPrivacy, "the closing line stays last without a privacy url")
		})
	}
}

func TestSettings_optionalLinesAndEscaping(t *testing.T) {
	tests := []struct {
		name     string
		opts     []telegram.Option
		contains []string
		absent   []string
	}{
		{
			"no options", nil,
			[]string{"Документов: 0"},
			[]string{"@", "Как я обращаюсь с данными"},
		},
		{
			"username and url",
			[]telegram.Option{telegram.WithBotUsername("most_files_bot"), telegram.WithPrivacyURL("https://most.example/privacy")},
			[]string{"💬 Отправка из любого чата: набери @most_files_bot в любом чате.", "Как я обращаюсь с данными: https://most.example/privacy"},
			nil,
		},
		{
			"url with a query string is escaped for HTML",
			[]telegram.Option{telegram.WithPrivacyURL("https://most.example/privacy?a=1&b=<2>")},
			[]string{"https://most.example/privacy?a=1&amp;b=&lt;2&gt;"},
			[]string{"b=<2>"},
		},
		{
			"folder name is escaped for HTML",
			[]telegram.Option{telegram.WithDriveFolder("Docs <&> «x»")},
			[]string{"«Docs &lt;&amp;&gt; «x»»"},
			[]string{"<&>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, _ := newBotOpts(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{}, tt.opts...)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

			b.ProcessUpdate(ctx, commandUpdate("settings"))

			require.Len(t, sent.texts, 1)
			text := sent.texts[0]
			require.Equal(t, "HTML", sent.sendModes[0])
			for _, want := range tt.contains {
				require.Contains(t, text, want, text)
			}
			for _, no := range tt.absent {
				require.NotContains(t, text, no)
			}
			require.LessOrEqual(t, utf8.RuneCountInString(text), 4096)
		})
	}
}

func TestStart_inlineHintNeedsTheBotUsername(t *testing.T) {
	start := func(text string) *models.Update {
		u := textUpdate(text)
		u.Message.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: len("/start")}}
		return u
	}
	tests := []struct {
		name string
		opts []telegram.Option
		want string
	}{
		{"with username", []telegram.Option{telegram.WithBotUsername("most_files_bot")}, "Набери @most_files_bot и слово из названия"},
		{"without username the plain greeting", nil, "Привет! Я Most Files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, _ := newBotOpts(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{}, tt.opts...)

			b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), start("/start inline"))

			require.Len(t, sent.texts, 1)
			require.Contains(t, sent.texts[0], tt.want)
		})
	}
}
