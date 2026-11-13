package telegram

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestSettingsStore_isolationBoundaryAndConcurrency(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	s := newSettingsStore(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })

	s.set(1, 10)
	s.set(2, 20)
	m1, _ := s.get(1)
	m2, _ := s.get(2)
	require.Equal(t, []int{10, 20}, []int{m1, m2}, "chats do not see each other")

	mu.Lock()
	now = now.Add(settingsTTL) // exactly the TTL: still valid
	mu.Unlock()
	_, ok := s.get(1)
	require.True(t, ok)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.set(int64(i%5), i)
			s.get(int64(i % 5))
		}()
	}
	wg.Wait()
}
