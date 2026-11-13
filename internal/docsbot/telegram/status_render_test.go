package telegram

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestRenderStatusMessage_tooLargeOnly(t *testing.T) {
	text, kb := renderStatusMessage(batchMsg, []docsbot.Document{
		batchDoc("a", docsbot.CatWork, docsbot.BackupTooLarge, ""),
		batchDoc("b", docsbot.CatWork, docsbot.BackupTooLarge, ""),
	}, nil)
	require.Equal(t, "⚠️ Не перенёс 2: больше 20 МБ", text)
	require.NotNil(t, kb)
	require.Equal(t, [][]string{{btnMyDocs}}, buttonTexts(kb.InlineKeyboard), "nothing to retry")
}

func TestRenderStatusMessage_folderIDIsEscapedInLink(t *testing.T) {
	text, _ := renderStatusMessage(batchMsg, []docsbot.Document{
		batchDoc("a", docsbot.CatWork, docsbot.BackupBackedUp, ""),
	}, map[docsbot.Category]string{docsbot.CatWork: `x"><b>`})
	require.NotContains(t, text, `"><b>`)
	require.Contains(t, text, "<a href=")
}

func TestStatusDonePlural(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{1, "1 документ"}, {2, "2 документа"}, {4, "4 документа"}, {5, "5 документов"},
		{11, "11 документов"}, {12, "12 документов"}, {21, "21 документ"}, {22, "22 документа"},
		{101, "101 документ"}, {111, "111 документов"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			lines := make([]docsbot.Document, tt.n)
			for i := range lines {
				lines[i] = batchDoc("d", docsbot.CatWork, docsbot.BackupBackedUp, "")
			}
			text, _ := renderStatusMessage(batchMsg, lines, nil)
			require.True(t, strings.HasPrefix(text, "✅ Перенёс на Google Диск — "+tt.want+"\n"), text)
		})
	}
}

// Worst case: every category full of long escaped titles, every error reason,
// a long folder id, still running. Header, error block and buttons must survive.
func TestRenderStatusMessage_worstCaseKeepsEssentials(t *testing.T) {
	folders := map[docsbot.Category]string{}
	var lines []docsbot.Document
	for _, cat := range docsbot.AllCategories {
		folders[cat] = strings.Repeat("f", 100)
		for i := 0; i < 300; i++ {
			lines = append(lines, batchDoc(strings.Repeat("&<>\"😀", 20), cat, docsbot.BackupBackedUp, ""))
		}
	}
	for _, r := range []docsbot.BackupReason{docsbot.BackupReasonQuota, docsbot.BackupReasonRevoked, docsbot.BackupReasonUnavailable} {
		lines = append(lines, batchDoc("x", docsbot.CatWork, docsbot.BackupFailed, r))
	}
	lines = append(lines, batchDoc("big", docsbot.CatWork, docsbot.BackupTooLarge, ""), batchDoc("p", docsbot.CatWork, docsbot.BackupPending, ""))

	text, kb := renderStatusMessage(batchMsg, lines, folders)

	require.LessOrEqual(t, utf16Len(text), statusTextLimit)
	require.True(t, strings.HasPrefix(text, fmt.Sprintf("⏳ Переношу на Google Диск · %d из %d\n", len(lines)-1, len(lines))))
	require.Contains(t, text, "⚠️ Не перенёс 4:")
	require.NotNil(t, kb)
	require.Equal(t, [][]string{{"🔁 Повторить (3)"}}, buttonTexts(kb.InlineKeyboard))
}

func batchDoc(title string, cat docsbot.Category, st docsbot.BackupStatus, reason docsbot.BackupReason) docsbot.Document {
	return docsbot.Document{ID: "d", Title: title, Category: cat, BackupStatus: st, BackupReason: reason}
}

var batchMsg = docsbot.StatusMessage{ID: "sm", Kind: docsbot.StatusBatch}

func TestRenderStatusMessage_progressBar(t *testing.T) {
	tests := []struct {
		name      string
		done, all int
		wantBar   string
	}{
		{"none of seven", 0, 7, "▱▱▱▱▱▱▱"},
		{"three of seven", 3, 7, "▰▰▰▱▱▱▱"},
		{"six of seven stays below full", 6, 7, "▰▰▰▰▰▰▱"},
		{"one of three floors", 1, 3, "▰▰▱▱▱▱▱"},
		{"two of three floors", 2, 3, "▰▰▰▰▱▱▱"},
		{"one of many is still empty", 1, 100, "▱▱▱▱▱▱▱"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lines []docsbot.Document
			for i := 0; i < tt.all; i++ {
				st := docsbot.BackupPending
				if i < tt.done {
					st = docsbot.BackupBackedUp
				}
				lines = append(lines, batchDoc("d", docsbot.CatPassport, st, ""))
			}
			text, kb := renderStatusMessage(batchMsg, lines, nil)
			require.True(t, strings.HasPrefix(text, fmt.Sprintf("⏳ Переношу на Google Диск · %d из %d\n%s", tt.done, tt.all, tt.wantBar)), text)
			require.Nil(t, kb, "nothing to press while uploads run")
		})
	}
}

func TestRenderStatusMessage_done(t *testing.T) {
	folders := map[docsbot.Category]string{docsbot.CatPassport: "FOLD"}
	tests := []struct {
		name    string
		kind    docsbot.StatusKind
		lines   []docsbot.Document
		folders map[docsbot.Category]string
		want    string
	}{
		{
			name:    "one category with folder link",
			kind:    docsbot.StatusBatch,
			lines:   []docsbot.Document{batchDoc("ВНЖ", docsbot.CatPassport, docsbot.BackupBackedUp, ""), batchDoc("Паспорт", docsbot.CatPassport, docsbot.BackupBackedUp, "")},
			folders: folders,
			want: "✅ Перенёс на Google Диск — 2 документа\n\n" +
				`📁 <a href="https://drive.google.com/drive/folders/FOLD">Паспорт и ID</a> · 2` + "\nВНЖ, Паспорт",
		},
		{
			name:  "transfer renders the same, folder unknown means no link",
			kind:  docsbot.StatusTransfer,
			lines: []docsbot.Document{batchDoc("Полис", docsbot.CatInsurance, docsbot.BackupBackedUp, "")},
			want:  "✅ Перенёс на Google Диск — 1 документ\n\n📁 Страховки · 1\nПолис",
		},
		{
			name: "categories follow the menu order, not the upload order",
			kind: docsbot.StatusBatch,
			lines: []docsbot.Document{
				batchDoc("c", docsbot.CatOther, docsbot.BackupBackedUp, ""),
				batchDoc("b", docsbot.CatAuto, docsbot.BackupBackedUp, ""),
				batchDoc("a", docsbot.CatPassport, docsbot.BackupBackedUp, ""),
			},
			folders: map[docsbot.Category]string{docsbot.CatAuto: "AUTO"},
			want: "✅ Перенёс на Google Диск — 3 документа\n\n" +
				"📁 Паспорт и ID · 1\na\n\n" +
				`📁 <a href="https://drive.google.com/drive/folders/AUTO">Авто</a> · 1` + "\nb\n\n" +
				"📁 Другое · 1\nc",
		},
		{
			name:  "a retired category folds into Другое",
			kind:  docsbot.StatusBatch,
			lines: []docsbot.Document{batchDoc("x", "gone", docsbot.BackupBackedUp, "")},
			want:  "✅ Перенёс на Google Диск — 1 документ\n\n📁 Другое · 1\nx",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, kb := renderStatusMessage(docsbot.StatusMessage{ID: "sm", Kind: tt.kind}, tt.lines, tt.folders)
			require.Equal(t, tt.want, text)
			require.NotNil(t, kb)
			require.Equal(t, [][]string{{btnMyDocs}}, buttonTexts(kb.InlineKeyboard))
			require.Equal(t, cbPrefix+actListBack, kb.InlineKeyboard[0][0].CallbackData)
		})
	}
}

func TestRenderStatusMessage_failures(t *testing.T) {
	ok := batchDoc("ok", docsbot.CatWork, docsbot.BackupBackedUp, "")
	tests := []struct {
		name      string
		lines     []docsbot.Document
		want      string
		wantRows  [][]string
		wantRetry bool
	}{
		{
			name:     "partial failure: error block on top, failed one excluded from blocks",
			lines:    []docsbot.Document{ok, batchDoc("bad", docsbot.CatWork, docsbot.BackupFailed, docsbot.BackupReasonQuota)},
			want:     "✅ Перенёс на Google Диск — 1 документ\n\n⚠️ Не перенёс 1: нет места на Диске\n\n📁 Работа · 1\nok",
			wantRows: [][]string{{"🔁 Повторить (1)", btnMyDocs}},
		},
		{
			name: "all failed: no success header, retry and my documents",
			lines: []docsbot.Document{
				batchDoc("a", docsbot.CatWork, docsbot.BackupFailed, docsbot.BackupReasonRevoked),
				batchDoc("b", docsbot.CatWork, docsbot.BackupFailed, docsbot.BackupReasonRevoked),
			},
			want:     "⚠️ Не перенёс 2: доступ отозван",
			wantRows: [][]string{{"🔁 Повторить (2)", btnMyDocs}},
		},
		{
			name: "empty or unknown reason reads as unavailable",
			lines: []docsbot.Document{
				batchDoc("a", docsbot.CatWork, docsbot.BackupFailed, ""),
				batchDoc("b", docsbot.CatWork, docsbot.BackupFailed, "weird"),
			},
			want:     "⚠️ Не перенёс 2: сервис недоступен",
			wantRows: [][]string{{"🔁 Повторить (2)", btnMyDocs}},
		},
		{
			name: "mixed reasons are listed with counts, in a fixed order",
			lines: []docsbot.Document{
				batchDoc("a", docsbot.CatWork, docsbot.BackupFailed, docsbot.BackupReasonUnavailable),
				batchDoc("b", docsbot.CatWork, docsbot.BackupFailed, docsbot.BackupReasonQuota),
				batchDoc("c", docsbot.CatWork, docsbot.BackupFailed, docsbot.BackupReasonQuota),
				batchDoc("d", docsbot.CatWork, docsbot.BackupTooLarge, ""),
			},
			want:     "⚠️ Не перенёс 4:\n• нет места на Диске — 2\n• сервис недоступен — 1\n• больше 20 МБ — 1",
			wantRows: [][]string{{"🔁 Повторить (3)", btnMyDocs}},
		},
		{
			name:     "too large only: counted as not transferred, no retry",
			lines:    []docsbot.Document{ok, batchDoc("big", docsbot.CatWork, docsbot.BackupTooLarge, "")},
			want:     "✅ Перенёс на Google Диск — 1 документ\n\n⚠️ Не перенёс 1: больше 20 МБ\n\n📁 Работа · 1\nok",
			wantRows: [][]string{{btnMyDocs}},
		},
		{
			name:     "failures while others still run: retry only, no my documents yet",
			lines:    []docsbot.Document{batchDoc("bad", docsbot.CatWork, docsbot.BackupFailed, docsbot.BackupReasonQuota), batchDoc("x", docsbot.CatWork, docsbot.BackupPending, "")},
			want:     "⏳ Переношу на Google Диск · 1 из 2\n▰▰▰▱▱▱▱\n\n⚠️ Не перенёс 1: нет места на Диске",
			wantRows: [][]string{{"🔁 Повторить (1)"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, kb := renderStatusMessage(batchMsg, tt.lines, nil)
			require.Equal(t, tt.want, text)
			require.NotNil(t, kb)
			require.Equal(t, tt.wantRows, buttonTexts(kb.InlineKeyboard))
			if len(tt.wantRows[0]) > 0 && strings.HasPrefix(tt.wantRows[0][0], "🔁") {
				require.Equal(t, "d:rsm:sm", kb.InlineKeyboard[0][0].CallbackData)
			}
		})
	}
}

// A redraw after the retry must not keep the stale retry button: the
// requeued documents are pending again, so the keyboard comes back nil, which
// the send paths turn into an explicit "clear keyboard".
func TestRenderStatusMessage_afterRetryNoStaleButton(t *testing.T) {
	_, kb := renderStatusMessage(batchMsg, []docsbot.Document{
		batchDoc("a", docsbot.CatWork, docsbot.BackupPending, ""),
		batchDoc("b", docsbot.CatWork, docsbot.BackupBackedUp, ""),
	}, nil)
	require.Nil(t, kb)
}

func TestRenderStatusMessage_manyTitlesAreCut(t *testing.T) {
	var lines []docsbot.Document
	for i := 1; i <= 13; i++ {
		lines = append(lines, batchDoc(fmt.Sprintf("n%d", i), docsbot.CatWork, docsbot.BackupBackedUp, ""))
	}
	text, _ := renderStatusMessage(batchMsg, lines, nil)
	require.Contains(t, text, "📁 Работа · 13\nn1, n2, n3, n4, n5, n6, n7, n8, n9, n10, …и ещё 3")
	require.NotContains(t, text, "n11")
}

func TestRenderStatusMessage_escapesAndTruncatesTitles(t *testing.T) {
	long := strings.Repeat("я", 100)
	text, _ := renderStatusMessage(batchMsg, []docsbot.Document{
		batchDoc(`<b>x</b> & "q"`, docsbot.CatWork, docsbot.BackupBackedUp, ""),
		batchDoc(long, docsbot.CatWork, docsbot.BackupBackedUp, ""),
	}, nil)
	require.Contains(t, text, "&lt;b&gt;x&lt;/b&gt; &amp; &#34;q&#34;")
	require.NotContains(t, text, "<b>")
	require.NotContains(t, text, long)
	require.Contains(t, text, truncateMiddle(long, listTitleRunes))
}

// A huge batch shrinks the title lists until it fits Telegram's limit, and
// keeps the header, the error block and the buttons.
func TestRenderStatusMessage_fitsTelegramLimit(t *testing.T) {
	folders := map[docsbot.Category]string{}
	var lines []docsbot.Document
	for _, cat := range docsbot.AllCategories {
		folders[cat] = strings.Repeat("a", 33)
		for i := 0; i < 40; i++ {
			lines = append(lines, batchDoc(strings.Repeat("<&\"я", 30), cat, docsbot.BackupBackedUp, ""))
		}
	}
	lines = append(lines, batchDoc("bad", docsbot.CatWork, docsbot.BackupFailed, docsbot.BackupReasonQuota))

	text, kb := renderStatusMessage(batchMsg, lines, folders)

	require.LessOrEqual(t, utf16Len(text), statusTextLimit)
	require.True(t, strings.HasPrefix(text, "✅ Перенёс на Google Диск — 440 документов"))
	require.Contains(t, text, "⚠️ Не перенёс 1: нет места на Диске")
	require.Contains(t, text, "…и ещё")
	require.NotNil(t, kb)
	require.Equal(t, [][]string{{"🔁 Повторить (1)", btnMyDocs}}, buttonTexts(kb.InlineKeyboard))
}

func TestRenderStatusMessage_emptyDoesNotPanic(t *testing.T) {
	text, kb := renderStatusMessage(batchMsg, nil, nil)
	require.Equal(t, "⏳ Переношу на Google Диск · 0 из 0\n▱▱▱▱▱▱▱", text)
	require.Nil(t, kb)
}

func TestRenderStatusMessage_singleKeepsCard(t *testing.T) {
	sm := docsbot.StatusMessage{ID: "sm", Kind: docsbot.StatusSingle}
	text, kb := renderStatusMessage(sm, []docsbot.Document{statusDoc("a.pdf", docsbot.BackupBackedUp)}, nil)
	require.Contains(t, text, "☁️ Google Диск · копия в Telegram")
	require.NotNil(t, kb)
	require.NotContains(t, text, "Переношу")
}

func buttonTexts(rows [][]models.InlineKeyboardButton) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		for _, b := range r {
			out[i] = append(out[i], b.Text)
		}
	}
	return out
}
