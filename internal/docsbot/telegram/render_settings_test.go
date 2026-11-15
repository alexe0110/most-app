package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestRenderSettings_noExportButton(t *testing.T) {
	for _, connected := range []bool{false, true} {
		_, kb := renderSettings(docsbot.SettingsView{DriveConnected: connected, Total: 5}, "https://x", true, screenInfo{})
		for _, row := range kb.InlineKeyboard {
			for _, btn := range row {
				require.NotEqual(t, "d:exp", btn.CallbackData)
				require.NotContains(t, btn.Text, "Прислать все")
			}
		}
	}
}

func TestRenderSettings_screenInfoLines(t *testing.T) {
	tests := []struct {
		name string
		v    docsbot.SettingsView
		info screenInfo
		want []string
		not  []string
	}{
		{
			"not connected names the folder", docsbot.SettingsView{},
			screenInfo{driveFolder: "Most Docs"},
			[]string{"Google Диск: не подключён", "в папку «Most Docs» на твоём Google Диске"},
			[]string{"Отправка из любого чата", "Как я обращаюсь"},
		},
		{
			"connected does not repeat the folder", docsbot.SettingsView{DriveConnected: true, Total: 3, InDrive: 2, OnlyTelegram: 1},
			screenInfo{driveFolder: "Most Docs"},
			[]string{"Google Диск: подключён ✅", "Документов: 3", "☁️ На Google Диске: 2", "📥 Только в Telegram: 1"},
			[]string{"Most Docs"},
		},
		{
			"username and url", docsbot.SettingsView{},
			screenInfo{driveFolder: "Most Docs", botUsername: "most_files_bot", privacyURL: "https://x.example/privacy"},
			[]string{"набери @most_files_bot в любом чате", "Как я обращаюсь с данными: https://x.example/privacy"},
			nil,
		},
		{
			"failed uploads are counted", docsbot.SettingsView{DriveConnected: true, Failed: 4},
			screenInfo{},
			[]string{"⚠️ Не загрузились: 4"},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, _ := renderSettings(tt.v, "https://auth.example", true, tt.info)
			for _, w := range tt.want {
				require.Contains(t, text, w)
			}
			for _, n := range tt.not {
				require.NotContains(t, text, n)
			}
			require.NotContains(t, text, "%!")
		})
	}
}

func TestHelpText_fitsAMessageWithTheLongestOptionalLines(t *testing.T) {
	d := &docs{botUsername: strings.Repeat("a", 32), privacyURL: "https://" + strings.Repeat("a", 2000) + ".example/privacy"}

	require.LessOrEqual(t, utf8.RuneCountInString(d.helpText()), 4096)
}
