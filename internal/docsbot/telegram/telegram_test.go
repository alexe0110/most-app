package telegram_test

import (
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"
)

func TestRegister_routing(t *testing.T) {
	tests := []struct {
		name     string
		update   *models.Update
		wantText string
	}{
		{"start", commandUpdate(100, "start"), "«Most Docs»"},
		{"help", commandUpdate(100, "help"), "умею"},
		{"unknown command", commandUpdate(100, "foo"), "Не понимаю"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, _ := newBot(t)
			b.ProcessUpdate(t.Context(), tt.update)
			require.True(t, has(sent.texts, tt.wantText), "sent %q", sent.texts)
		})
	}
}
