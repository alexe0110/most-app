package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

const sendTestToken = "123:ABC"

// apiRequest is one call that reached the fake Bot API.
type apiRequest struct {
	method string
	form   map[string]string // first value of each form field
}

// sendServer is a Bot API stand-in: it records requests and, when fail is set,
// answers every call with that Telegram error.
type sendServer struct {
	mu   sync.Mutex
	reqs []apiRequest
	fail *apiFailure
}

type apiFailure struct {
	code int
	desc string
}

func (s *sendServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseMultipartForm(1 << 20)
	req := apiRequest{method: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], form: map[string]string{}}
	for k, v := range r.Form {
		req.form[k] = v[0]
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	fail := s.fail
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case req.form["reply_markup"] == "null":
		// Mirrors Telegram: a JSON null is not an object.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: object expected as reply markup"}`)
	case fail != nil:
		w.WriteHeader(fail.code)
		_, _ = fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":%q}`, fail.code, fail.desc)
	case req.method == "deleteMessage" || req.method == "answerCallbackQuery":
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	default:
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":9}}`)
	}
}

func (s *sendServer) last(t *testing.T) apiRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.reqs, "no request reached the API")
	return s.reqs[len(s.reqs)-1]
}

func newSendEnv(t *testing.T, level slog.Level) (sender, *sendServer, *bytes.Buffer) {
	t.Helper()
	srv := &sendServer{}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	api, err := bot.New(sendTestToken, bot.WithServerURL(ts.URL), bot.WithSkipGetMe())
	require.NoError(t, err)
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level}))
	return sender{api: api, log: log}, srv, &buf
}

var sendTestKeyboard = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Go", CallbackData: "x"}}}}

const sendTestKeyboardJSON = `{"inline_keyboard":[[{"text":"Go","callback_data":"x"}]]}`

func TestSender_classifiesRealTelegramErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc                       string
		code                       int
		notModified, gone, blocked bool
	}{
		{desc: "Bad Request: message is not modified: specified new message content and reply markup are exactly the same", code: 400, notModified: true},
		{desc: "Bad Request: message to edit not found", code: 400, gone: true},
		{desc: "Bad Request: message can't be edited", code: 400, gone: true},
		{desc: "Bad Request: message to delete not found", code: 400, gone: true},
		{desc: "Forbidden: bot was blocked by the user", code: 403, blocked: true},
		{desc: "Forbidden: user is deactivated", code: 403, blocked: true},
		{desc: "Bad Request: chat not found", code: 400},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			t.Parallel()
			s, srv, _ := newSendEnv(t, slog.LevelDebug)
			srv.fail = &apiFailure{code: tt.code, desc: tt.desc}

			_, err := s.api.EditMessageText(t.Context(), &bot.EditMessageTextParams{ChatID: 1, MessageID: 2, Text: "x"})
			require.Error(t, err)
			err = apiError("editMessageText", err)

			require.Equal(t, tt.notModified, isNotModified(err))
			require.Equal(t, tt.gone, isMessageGone(err))
			require.Equal(t, tt.blocked, errors.Is(err, errBlocked))
			require.False(t, isNotModified(nil))
			require.False(t, isMessageGone(nil))
		})
	}
}

func TestSender_keyboardOnTheWire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		call       func(sender, context.Context)
		method     string
		wantMarkup string // "" means the field must be absent
	}{
		{"send without keyboard", func(s sender, ctx context.Context) { s.send(ctx, 42, "hi") }, "sendMessage", ""},
		{"send with a nil keyboard omits reply_markup", func(s sender, ctx context.Context) {
			s.send(ctx, 42, "hi", withKeyboard(nil))
		}, "sendMessage", ""},
		{"send keeps the keyboard", func(s sender, ctx context.Context) {
			s.send(ctx, 42, "hi", withKeyboard(sendTestKeyboard))
		}, "sendMessage", sendTestKeyboardJSON},
		{"trySend with a nil keyboard omits reply_markup", func(s sender, ctx context.Context) {
			_, _ = s.trySend(ctx, 42, "hi", withKeyboard(nil))
		}, "sendMessage", ""},
		{"edit without withKeyboard keeps the old keyboard", func(s sender, ctx context.Context) {
			s.edit(ctx, 42, 7, "hi")
		}, "editMessageText", ""},
		{"edit with a nil keyboard clears it", func(s sender, ctx context.Context) {
			s.edit(ctx, 42, 7, "hi", withKeyboard(nil))
		}, "editMessageText", `{"inline_keyboard":[]}`},
		{"edit keeps the keyboard", func(s sender, ctx context.Context) {
			s.edit(ctx, 42, 7, "hi", withKeyboard(sendTestKeyboard))
		}, "editMessageText", sendTestKeyboardJSON},
		{"tryEdit with a nil keyboard clears it", func(s sender, ctx context.Context) {
			_ = s.tryEdit(ctx, 42, 7, "hi", withKeyboard(nil))
		}, "editMessageText", `{"inline_keyboard":[]}`},
		{"edit caption with a nil keyboard clears it", func(s sender, ctx context.Context) {
			s.edit(ctx, 42, 7, "cap", onCaption(), withKeyboard(nil))
		}, "editMessageCaption", `{"inline_keyboard":[]}`},
		{"clearKeyboard sends an empty array, not null", func(s sender, ctx context.Context) {
			s.clearKeyboard(ctx, 42, 7)
		}, "editMessageReplyMarkup", `{"inline_keyboard":[]}`},
		{"setKeyboard replaces the keyboard", func(s sender, ctx context.Context) {
			s.setKeyboard(ctx, 42, 7, sendTestKeyboard)
		}, "editMessageReplyMarkup", sendTestKeyboardJSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, srv, buf := newSendEnv(t, slog.LevelDebug)

			tt.call(s, t.Context())

			require.NotContains(t, buf.String(), `"level":"ERROR"`, "the call must succeed")
			got := srv.last(t)
			require.Equal(t, tt.method, got.method)
			markup, present := got.form["reply_markup"]
			if tt.wantMarkup == "" {
				require.False(t, present, "reply_markup must not be sent")
				return
			}
			require.JSONEq(t, tt.wantMarkup, markup)
		})
	}
}

func TestSender_sendsTextOptions(t *testing.T) {
	t.Parallel()
	s, srv, _ := newSendEnv(t, slog.LevelDebug)

	require.Equal(t, 9, s.send(t.Context(), 42, "<b>hi</b>", withHTML()))
	require.Equal(t, models.ParseModeHTML, models.ParseMode(srv.last(t).form["parse_mode"]))

	s.send(t.Context(), 42, "plain")
	require.Empty(t, srv.last(t).form["parse_mode"])

	s.edit(t.Context(), 42, 7, "<b>hi</b>", withHTML())
	require.Equal(t, models.ParseModeHTML, models.ParseMode(srv.last(t).form["parse_mode"]))

	s.answer(t.Context(), "cb1", withToast("Готово"))
	got := srv.last(t)
	require.Equal(t, "answerCallbackQuery", got.method)
	require.Equal(t, "Готово", got.form["text"])

	s.delete(t.Context(), 42, 7)
	require.Equal(t, "deleteMessage", srv.last(t).method)
}

func TestSender_editNotModifiedIsSuccess(t *testing.T) {
	t.Parallel()
	s, srv, buf := newSendEnv(t, slog.LevelDebug)
	srv.fail = &apiFailure{code: 400, desc: "Bad Request: message is not modified: specified new message content and reply markup are exactly the same"}

	require.NoError(t, s.tryEdit(t.Context(), 42, 7, "same"))
	s.edit(t.Context(), 42, 7, "same")
	s.clearKeyboard(t.Context(), 42, 7)
	require.NotContains(t, buf.String(), `"level":"ERROR"`)
	require.NotContains(t, buf.String(), `"level":"WARN"`)
}

// The plain methods log a failure once and return nothing; the try methods log
// nothing and hand the error to the caller.
func TestSender_failureIsLoggedByPlainMethodsOnly(t *testing.T) {
	t.Parallel()
	failure := &apiFailure{code: 400, desc: "Bad Request: chat not found"}

	t.Run("plain methods log once each", func(t *testing.T) {
		t.Parallel()
		s, srv, buf := newSendEnv(t, slog.LevelInfo)
		srv.fail = failure

		require.Zero(t, s.send(t.Context(), 42, "secret text"))
		s.edit(t.Context(), 42, 7, "secret text")
		s.edit(t.Context(), 42, 7, "secret text", onCaption())
		s.delete(t.Context(), 42, 7)
		s.answer(t.Context(), "cb")
		s.clearKeyboard(t.Context(), 42, 7)
		s.setKeyboard(t.Context(), 42, 7, sendTestKeyboard)

		out := buf.String()
		require.Equal(t, 7, strings.Count(out, `"level":"ERROR"`), "one line per failed call")
		require.NotContains(t, out, "secret text")
	})

	t.Run("try methods return the error and log nothing", func(t *testing.T) {
		t.Parallel()
		s, srv, buf := newSendEnv(t, slog.LevelInfo)
		srv.fail = failure

		id, err := s.trySend(t.Context(), 42, "secret text")
		require.Zero(t, id)
		require.ErrorContains(t, err, "sendMessage")
		require.ErrorContains(t, s.tryEdit(t.Context(), 42, 7, "secret text"), "editMessageText")
		require.ErrorContains(t, s.tryDelete(t.Context(), 42, 7), "deleteMessage")
		require.Empty(t, buf.String())
	})
}

// A 403 means the user left: it maps to errBlocked and is an Info line, never an
// Error, with neither content nor ids.
func TestSender_blockedUserIsNotAnError(t *testing.T) {
	t.Parallel()

	t.Run("try methods return errBlocked", func(t *testing.T) {
		t.Parallel()
		s, srv, buf := newSendEnv(t, slog.LevelInfo)
		srv.fail = &apiFailure{code: 403, desc: "Forbidden: bot was blocked by the user"}

		_, err := s.trySend(t.Context(), 42, "secret text", withHTML(), withKeyboard(sendTestKeyboard))
		require.ErrorIs(t, err, errBlocked)
		require.ErrorIs(t, s.tryEdit(t.Context(), 42, 7, "secret text"), errBlocked)
		require.ErrorIs(t, s.tryDelete(t.Context(), 42, 7), errBlocked)
		require.Empty(t, buf.String())
	})

	t.Run("plain methods log Info", func(t *testing.T) {
		t.Parallel()
		s, srv, buf := newSendEnv(t, slog.LevelInfo)
		srv.fail = &apiFailure{code: 403, desc: "Forbidden: bot was blocked by the user"}

		const calls = 6
		s.send(t.Context(), 42, "secret text", withHTML(), withKeyboard(sendTestKeyboard))
		s.edit(t.Context(), 42, 7, "secret text")
		s.clearKeyboard(t.Context(), 42, 7)
		s.setKeyboard(t.Context(), 42, 7, sendTestKeyboard)
		s.delete(t.Context(), 42, 7)
		s.answer(t.Context(), "cb", withToast("secret text"))

		out := buf.String()
		require.Equal(t, calls, strings.Count(out, "\n"), "one line per call")
		require.Equal(t, calls, strings.Count(out, `"level":"INFO"`))
		require.NotContains(t, out, `"level":"ERROR"`)
		require.NotContains(t, out, "secret text")
		require.NotContains(t, out, sendTestToken)
		require.NotContains(t, out, `"chat_id`)
	})
}

func TestSender_ignoreBlocked(t *testing.T) {
	t.Parallel()
	s, srv, buf := newSendEnv(t, slog.LevelInfo)
	other := errors.New("boom")

	require.NoError(t, s.ignoreBlocked(t.Context(), nil))
	require.Equal(t, other, s.ignoreBlocked(t.Context(), other))
	require.Empty(t, buf.String())

	srv.fail = &apiFailure{code: 403, desc: "Forbidden: user is deactivated"}
	_, err := s.trySend(t.Context(), 42, "x")
	require.NoError(t, s.ignoreBlocked(t.Context(), err))
	require.Contains(t, buf.String(), `"level":"INFO"`)
	require.NotContains(t, buf.String(), `"level":"ERROR"`)
}

func TestSender_goneMessageIsReportedToTheCaller(t *testing.T) {
	t.Parallel()
	s, srv, _ := newSendEnv(t, slog.LevelInfo)
	srv.fail = &apiFailure{code: 400, desc: "Bad Request: message to edit not found"}

	err := s.tryEdit(t.Context(), 42, 7, "x")

	require.True(t, isMessageGone(err))
	require.False(t, isNotModified(err))
}

func TestSender_logFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string // substring of the log, "" for no output
	}{
		{"nil", nil, ""},
		{"not modified", errors.New("bad request, Bad Request: message is not modified"), ""},
		{"blocked", fmt.Errorf("sendMessage: %w", errBlocked), `"level":"INFO"`},
		{"other", errors.New("boom"), `"level":"WARN"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _, buf := newSendEnv(t, slog.LevelDebug)

			s.logFailure(t.Context(), slog.LevelWarn, "what", tt.err, "user_id", "u1")

			if tt.want == "" {
				require.Empty(t, buf.String())
				return
			}
			require.Contains(t, buf.String(), tt.want)
		})
	}
}

// The SDK puts the bot token into the request URL, and a transport error carries
// that URL: neither the trace nor the failure line may show the token or the text.
func TestSender_transportErrorDoesNotLeakToken(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	api, err := bot.New(sendTestToken, bot.WithServerURL(url), bot.WithSkipGetMe())
	require.NoError(t, err)
	s := sender{api: api, log: log}

	_, err = s.trySend(t.Context(), 42, "secret message text")
	require.Error(t, err)
	require.NotContains(t, err.Error(), sendTestToken)
	s.send(t.Context(), 42, "secret message text")

	out := buf.String()
	require.Contains(t, out, `"op":"sendMessage"`)
	require.Contains(t, out, `"level":"ERROR"`)
	require.NotContains(t, out, sendTestToken)
	require.NotContains(t, out, "secret message text")
	require.NotContains(t, out, `"chat_id`)
}

func TestSender_keepsCancellation(t *testing.T) {
	t.Parallel()
	s, _, _ := newSendEnv(t, slog.LevelInfo)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := s.trySend(ctx, 42, "x")

	require.ErrorIs(t, err, context.Canceled)
}
