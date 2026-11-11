package tg

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"
)

func TestMarkupOrNil(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      models.ReplyMarkup
		wantNil bool
	}{
		{"untyped nil", nil, true},
		{"typed nil inline", (*models.InlineKeyboardMarkup)(nil), true},
		{"typed nil reply keyboard", (*models.ReplyKeyboardMarkup)(nil), true},
		{"typed nil keyboard remove", (*models.ReplyKeyboardRemove)(nil), true},
		{"typed nil force reply", (*models.ForceReply)(nil), true},
		{"inline", &models.InlineKeyboardMarkup{}, false},
		{"reply keyboard", &models.ReplyKeyboardMarkup{}, false},
		{"keyboard remove", &models.ReplyKeyboardRemove{}, false},
		{"force reply", &models.ForceReply{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := markupOrNil(tt.in)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("got %#v, want untyped nil", got)
				}
				return
			}
			if got != tt.in {
				t.Fatalf("got %#v, want %#v", got, tt.in)
			}
		})
	}
}

// markupServer records the reply_markup form field of every request, per method.
type markupServer struct {
	mu   sync.Mutex
	seen map[string]*string // nil value: field absent
}

func newMarkupServer(t *testing.T) (*Bot, *markupServer) {
	t.Helper()
	ms := &markupServer{seen: map[string]*string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		_ = r.ParseMultipartForm(1 << 20)
		var markup *string
		if v := r.Form["reply_markup"]; len(v) > 0 {
			markup = &v[0]
		}
		ms.mu.Lock()
		ms.seen[method] = markup
		ms.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if markup != nil && *markup == "null" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: object expected as reply markup"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":9}}`)
	}))
	t.Cleanup(srv.Close)
	api, err := bot.New("123:ABC", bot.WithServerURL(srv.URL), bot.WithSkipGetMe())
	require.NoError(t, err)
	return &Bot{api: api, cfg: Config{Token: "123:ABC"}, http: http.DefaultClient, log: slog.New(slog.DiscardHandler)}, ms
}

func (m *markupServer) get(method string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.seen[method]
	if !ok || v == nil {
		return "", false
	}
	return *v, true
}

func TestMarkupOnTheWire(t *testing.T) {
	t.Parallel()
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Go", CallbackData: "x"}}}}
	const kbJSON = `{"inline_keyboard":[[{"text":"Go","callback_data":"x"}]]}`
	nils := map[string]models.ReplyMarkup{
		"untyped nil": nil,
		"typed nil":   (*models.InlineKeyboardMarkup)(nil),
	}
	for name, m := range nils {
		t.Run("SendMarkup omits "+name, func(t *testing.T) {
			t.Parallel()
			b, ms := newMarkupServer(t)
			require.NoError(t, b.SendMarkup(t.Context(), 42, "hi", m))
			_, present := ms.get("sendMessage")
			require.False(t, present, "reply_markup must not be sent")
		})
		t.Run("SendHTML omits "+name, func(t *testing.T) {
			t.Parallel()
			b, ms := newMarkupServer(t)
			id, err := b.SendHTML(t.Context(), 42, "hi", m)
			require.NoError(t, err)
			require.Equal(t, 9, id)
			_, present := ms.get("sendMessage")
			require.False(t, present, "reply_markup must not be sent")
		})
		t.Run("EditHTML clears keyboard on "+name, func(t *testing.T) {
			t.Parallel()
			b, ms := newMarkupServer(t)
			require.NoError(t, b.EditHTML(t.Context(), 42, 7, "hi", m))
			got, present := ms.get("editMessageText")
			require.True(t, present)
			require.Equal(t, `{"inline_keyboard":[]}`, got)
		})
	}

	t.Run("SendMarkup keeps keyboard", func(t *testing.T) {
		t.Parallel()
		b, ms := newMarkupServer(t)
		require.NoError(t, b.SendMarkup(t.Context(), 42, "hi", kb))
		got, _ := ms.get("sendMessage")
		require.JSONEq(t, kbJSON, got)
	})
	t.Run("SendHTML keeps keyboard", func(t *testing.T) {
		t.Parallel()
		b, ms := newMarkupServer(t)
		_, err := b.SendHTML(t.Context(), 42, "hi", kb)
		require.NoError(t, err)
		got, _ := ms.get("sendMessage")
		require.JSONEq(t, kbJSON, got)
	})
	t.Run("EditHTML keeps keyboard", func(t *testing.T) {
		t.Parallel()
		b, ms := newMarkupServer(t)
		require.NoError(t, b.EditHTML(t.Context(), 42, 7, "hi", kb))
		got, _ := ms.get("editMessageText")
		require.JSONEq(t, kbJSON, got)
	})
}
