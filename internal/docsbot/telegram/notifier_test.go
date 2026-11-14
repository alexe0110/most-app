package telegram_test

import (
	"cmp"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// botCall is one request that reached the fake Bot API.
type botCall struct {
	method    string // sendMessage, editMessageText, ...
	text      string
	messageID string
	parseMode string
	markup    string // raw reply_markup form value
	hasMarkup bool   // reply_markup field was present at all
}

// fakeBotAPI is a Bot API stand-in whose editMessageText answer is configurable.
type fakeBotAPI struct {
	mu       sync.Mutex
	calls    []botCall
	editErr  string // when set, editMessageText fails with this description
	editCode int    // status of that failure, 400 when zero
	nextID   int    // message_id returned by sendMessage
	blocked  bool   // when set, sendMessage fails with 403 (user blocked the bot)
}

func (f *fakeBotAPI) callsOf(method string) []botCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []botCall
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeBotAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	f.mu.Lock()
	defer f.mu.Unlock()
	call := botCall{
		method: method, text: r.FormValue("text"), messageID: r.FormValue("message_id"), parseMode: r.FormValue("parse_mode"),
	}
	if vals, ok := r.Form["reply_markup"]; ok && len(vals) > 0 {
		call.hasMarkup, call.markup = true, vals[0]
	}
	f.calls = append(f.calls, call)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case call.hasMarkup && call.markup == "null":
		// Mirrors Telegram: a JSON null is not an object.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: object expected as reply markup"}`)
	case method == "editMessageText" && f.editErr != "":
		code := cmp.Or(f.editCode, http.StatusBadRequest)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":`+itoa(code)+`,"description":"`+f.editErr+`"}`)
	case method == "sendMessage" && f.blocked:
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
	case method == "sendMessage":
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":`+itoa(f.nextID)+`}}`)
	default:
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// rewriteTransport sends every request to the fake server. tg.New has no
// server-URL option, so the test redirects the default transport instead.
type rewriteTransport struct {
	target *url.URL
	next   http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = r.target.Scheme, r.target.Host
	return r.next.RoundTrip(req)
}

type stubUsers struct{}

func (stubUsers) Ensure(context.Context, int64, string) (string, error) { return testUserID, nil }

func (stubUsers) Lookup(context.Context, int64) (string, bool, error) { return testUserID, true, nil }

type chatLookup struct{}

func (chatLookup) TgUserID(context.Context, string) (int64, error) { return 100, nil }

type notifierEnv struct {
	api    *fakeBotAPI
	repo   *fakeRepo
	status *fakeStatus
	n      *telegram.Notifier
	bot    *tg.Bot
	svc    *docsbot.Service
}

// newNotifierEnv builds a real tg.Bot against the fake API. It mutates
// http.DefaultTransport for the test's lifetime, so tests using it must not be parallel.
func newNotifierEnv(t *testing.T) *notifierEnv {
	t.Helper()
	api := &fakeBotAPI{nextID: 500}
	ts := httptest.NewServer(api)
	t.Cleanup(ts.Close)
	target, err := url.Parse(ts.URL)
	require.NoError(t, err)

	orig := http.DefaultTransport
	http.DefaultTransport = rewriteTransport{target: target, next: orig}
	t.Cleanup(func() { http.DefaultTransport = orig })

	log := slog.New(slog.DiscardHandler)
	b, err := tg.New(tg.Config{Token: "123:ABC", Mode: tg.ModePolling}, stubUsers{}, log, func(*bot.Bot) {})
	require.NoError(t, err)

	repo := newFakeRepo()
	status := newFakeStatus(repo)
	svc := docsbot.NewService(repo, fakeDrive{connected: true}, nil, fakeUploads{}, repo, status, fakeSyncs{repo: repo})
	return &notifierEnv{api: api, repo: repo, status: status, bot: b, svc: svc, n: telegram.NewNotifier(b, chatLookup{}, fakeAuth{}, svc, log)}
}

// batch seeds a status message (chat 100, message 70) with one document per status.
func (e *notifierEnv) batch(t *testing.T, statuses ...docsbot.BackupStatus) (docsbot.StatusMessage, []docsbot.Document) {
	t.Helper()
	sm, err := e.status.Create(t.Context(), testUserID, 100, 70, docsbot.StatusBatch)
	require.NoError(t, err)
	var docs []docsbot.Document
	for _, st := range statuses {
		docs = append(docs, e.repo.seed(docsbot.Document{
			UserID: testUserID, Title: "Doc", Category: docsbot.CatPassport, BackupStatus: st,
			DriveFileID: "F1", StatusMessageID: sm.ID,
		}))
	}
	return sm, docs
}

func TestNotifier_UploadOutcome_groupedEditsStatusMessage(t *testing.T) {
	e := newNotifierEnv(t)
	sm, docs := e.batch(t, docsbot.BackupBackedUp, docsbot.BackupUploading)

	require.NoError(t, e.n.UploadOutcome(t.Context(), testUserID, docs[0].ID))

	edits := e.api.callsOf("editMessageText")
	require.Len(t, edits, 1)
	require.Equal(t, "70", edits[0].messageID)
	require.Equal(t, "HTML", edits[0].parseMode)
	require.Contains(t, edits[0].text, "⏳ Переношу на Google Диск · 1 из 2")
	require.Empty(t, e.api.callsOf("sendMessage"), "no separate Выгружено notice for a batch")
	require.False(t, e.status.msgs[sm.ID].Closed, "still has a line in progress")
}

func TestNotifier_UploadOutcome_closesWhenAllTerminal(t *testing.T) {
	e := newNotifierEnv(t)
	sm, docs := e.batch(t, docsbot.BackupBackedUp, docsbot.BackupFailed)

	require.NoError(t, e.n.UploadOutcome(t.Context(), testUserID, docs[1].ID))

	require.True(t, e.status.msgs[sm.ID].Closed)
	edits := e.api.callsOf("editMessageText")
	require.Len(t, edits, 1)
	require.Contains(t, edits[0].text, "⚠️ Не перенёс 1")
}

func TestNotifier_UploadOutcome_editErrors(t *testing.T) {
	tests := []struct {
		name       string
		editErr    string
		wantResend bool
		wantErr    bool
	}{
		{name: "not modified is ignored", editErr: "Bad Request: message is not modified: specified new message content and reply markup are exactly the same"},
		{name: "deleted message is resent", editErr: "Bad Request: message to edit not found", wantResend: true},
		{name: "uneditable message is resent", editErr: "Bad Request: message can't be edited", wantResend: true},
		{name: "other error is returned for the worker to log", editErr: "Bad Request: chat not found", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifierEnv(t)
			e.api.editErr = tt.editErr
			sm, docs := e.batch(t, docsbot.BackupBackedUp, docsbot.BackupUploading)

			err := e.n.UploadOutcome(t.Context(), testUserID, docs[0].ID)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			sends := e.api.callsOf("sendMessage")
			if !tt.wantResend {
				require.Empty(t, sends)
				require.Equal(t, 70, e.status.msgs[sm.ID].MessageID)
				return
			}
			require.Len(t, sends, 1)
			require.Equal(t, "HTML", sends[0].parseMode)
			require.Contains(t, sends[0].text, "⏳ Переношу на Google Диск · 1 из 2")
			require.Equal(t, 500, e.status.msgs[sm.ID].MessageID, "new message id is stored for later edits")
		})
	}
}

// A user who left cannot be reached: it is no error for the worker, and the
// remembered message id stays as it was.
func TestNotifier_UploadOutcome_blockedUserIsNotAnError(t *testing.T) {
	tests := []struct {
		name      string
		editErr   string
		editCode  int
		blocked   bool // the resend is refused too
		wantSends int
	}{
		{name: "blocked on edit", editErr: "Forbidden: bot was blocked by the user", editCode: http.StatusForbidden},
		{name: "deactivated on edit", editErr: "Forbidden: user is deactivated", editCode: http.StatusForbidden},
		{name: "blocked on resend", editErr: "Bad Request: message to edit not found", blocked: true, wantSends: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifierEnv(t)
			e.api.editErr, e.api.editCode, e.api.blocked = tt.editErr, tt.editCode, tt.blocked
			sm, docs := e.batch(t, docsbot.BackupBackedUp, docsbot.BackupUploading)
			var buf lockedBuffer
			n := telegram.NewNotifier(e.bot, chatLookup{}, fakeAuth{}, e.svc, slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

			require.NoError(t, n.UploadOutcome(t.Context(), testUserID, docs[0].ID))

			require.Len(t, e.api.callsOf("sendMessage"), tt.wantSends)
			require.Equal(t, 70, e.status.msgs[sm.ID].MessageID)
			require.NotContains(t, buf.String(), `"level":"ERROR"`)
		})
	}
}

func TestNotifier_UploadOutcome_resendStillClosesWhenDone(t *testing.T) {
	e := newNotifierEnv(t)
	e.api.editErr = "Bad Request: message to edit not found"
	sm, docs := e.batch(t, docsbot.BackupBackedUp)

	require.NoError(t, e.n.UploadOutcome(t.Context(), testUserID, docs[0].ID))

	require.Len(t, e.api.callsOf("sendMessage"), 1)
	require.True(t, e.status.msgs[sm.ID].Closed)
}

func TestNotifier_UploadOutcome_notGrouped(t *testing.T) {
	tests := []struct {
		name     string
		status   docsbot.BackupStatus
		wantText string // empty: nothing is sent
	}{
		{name: "backed up", status: docsbot.BackupBackedUp, wantText: "Doc"},
		{name: "failed", status: docsbot.BackupFailed, wantText: "Не удалось"},
		{name: "too large sends nothing", status: docsbot.BackupTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newNotifierEnv(t)
			doc := e.repo.seed(docsbot.Document{UserID: testUserID, Title: "Doc", BackupStatus: tt.status, DriveFileID: "F1"})

			require.NoError(t, e.n.UploadOutcome(t.Context(), testUserID, doc.ID))

			require.Empty(t, e.api.callsOf("editMessageText"))
			sends := e.api.callsOf("sendMessage")
			if tt.wantText == "" {
				require.Empty(t, sends)
				return
			}
			require.Len(t, sends, 1)
			require.Contains(t, sends[0].text, tt.wantText)
		})
	}
}

func TestNotifier_UploadOutcome_unknownDocument(t *testing.T) {
	e := newNotifierEnv(t)
	require.Error(t, e.n.UploadOutcome(t.Context(), testUserID, "missing"))
	require.Empty(t, e.api.callsOf("sendMessage"))
	require.Empty(t, e.api.callsOf("editMessageText"))
}

// The throttle must space edits of one message by about a second, and must not
// hold its lock while sleeping: a different message is not delayed by it.
func TestNotifier_UploadOutcome_throttle(t *testing.T) {
	e := newNotifierEnv(t)
	_, first := e.batch(t, docsbot.BackupUploading, docsbot.BackupUploading)
	_, second := e.batch(t, docsbot.BackupUploading)

	ctx := t.Context()
	require.NoError(t, e.n.UploadOutcome(ctx, testUserID, first[0].ID))

	var wg sync.WaitGroup
	var otherElapsed time.Duration
	wg.Add(1)
	start := time.Now()
	go func() {
		defer wg.Done()
		_ = e.n.UploadOutcome(ctx, testUserID, first[1].ID) // same message: waits ~1s
	}()
	time.Sleep(100 * time.Millisecond) // let it reach the sleep
	otherStart := time.Now()
	require.NoError(t, e.n.UploadOutcome(ctx, testUserID, second[0].ID))
	otherElapsed = time.Since(otherStart)
	wg.Wait()

	require.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond, "second edit of the same message is delayed")
	require.Less(t, otherElapsed, 500*time.Millisecond, "another message is not blocked by the sleeping one")
	require.Len(t, e.api.callsOf("editMessageText"), 3)
}

func TestNotifier_Reconnect_debounced(t *testing.T) {
	e := newNotifierEnv(t)

	require.NoError(t, e.n.Reconnect(t.Context(), testUserID))
	require.NoError(t, e.n.Reconnect(t.Context(), testUserID))
	require.Len(t, e.api.callsOf("sendMessage"), 1, "one reconnect prompt per batch")

	require.NoError(t, e.n.Reconnect(t.Context(), "another-user"))
	require.Len(t, e.api.callsOf("sendMessage"), 2, "a different user is prompted separately")
}

func TestNotifier_UploadOutcome_singleAndTransferKindsSendNoNotice(t *testing.T) {
	for _, kind := range []docsbot.StatusKind{docsbot.StatusSingle, docsbot.StatusTransfer} {
		t.Run(string(kind), func(t *testing.T) {
			e := newNotifierEnv(t)
			sm, err := e.status.Create(t.Context(), testUserID, 100, 70, kind)
			require.NoError(t, err)
			doc := e.repo.seed(docsbot.Document{
				UserID: testUserID, Title: "Doc", Category: docsbot.CatPassport,
				BackupStatus: docsbot.BackupBackedUp, DriveFileID: "F1", StatusMessageID: sm.ID,
			})

			require.NoError(t, e.n.UploadOutcome(t.Context(), testUserID, doc.ID))

			require.Empty(t, e.api.callsOf("sendMessage"), "a grouped doc never gets a per-document notice")
			edits := e.api.callsOf("editMessageText")
			require.Len(t, edits, 1)
			require.Equal(t, "70", edits[0].messageID)
			require.True(t, e.status.msgs[sm.ID].Closed)
		})
	}
}

func TestNotifier_UploadOutcome_transferDoneText(t *testing.T) {
	e := newNotifierEnv(t)
	sm, err := e.status.Create(t.Context(), testUserID, 100, 70, docsbot.StatusTransfer)
	require.NoError(t, err)
	var last docsbot.Document
	for i := 0; i < 3; i++ {
		last = e.repo.seed(docsbot.Document{UserID: testUserID, Title: "Doc", Category: docsbot.CatPassport,
			BackupStatus: docsbot.BackupBackedUp, DriveFileID: "F", StatusMessageID: sm.ID})
	}

	require.NoError(t, e.n.UploadOutcome(t.Context(), testUserID, last.ID))

	edits := e.api.callsOf("editMessageText")
	require.Len(t, edits, 1)
	require.Contains(t, edits[0].text, "✅ Перенёс на Google Диск — 3 документа")
}

type failingLookup struct{ err error }

func (f failingLookup) TgUserID(context.Context, string) (int64, error) { return 0, f.err }

// A failed prompt must not silence the next attempt: a retried job should still
// be able to ask the user to reconnect.
func TestNotifier_Reconnect_failedPromptDoesNotArmDebounce(t *testing.T) {
	e := newNotifierEnv(t)
	n := telegram.NewNotifier(e.bot, failingLookup{err: errors.New("boom")}, fakeAuth{}, e.svc, slog.New(slog.DiscardHandler))

	require.Error(t, n.Reconnect(t.Context(), testUserID))
	require.Error(t, n.Reconnect(t.Context(), testUserID), "the second attempt must try again, not be swallowed by the debounce")
}

// A blocked bot cannot be reached: the prompt is not retried on every upload.
func TestNotifier_Reconnect_blockedArmsDebounce(t *testing.T) {
	e := newNotifierEnv(t)
	e.api.blocked = true

	require.NoError(t, e.n.Reconnect(t.Context(), testUserID))
	require.NoError(t, e.n.Reconnect(t.Context(), testUserID))

	require.Len(t, e.api.callsOf("sendMessage"), 1, "the second call is debounced")
}

// A block is logged by the sender; NotifyConnected must not return it as an error.
func TestNotifyConnected_blockedIsNotAnError(t *testing.T) {
	e := newNotifierEnv(t)
	e.api.blocked = true
	notify := telegram.NotifyConnected(e.bot, chatLookup{}, e.svc, slog.New(slog.DiscardHandler))

	require.NoError(t, notify(t.Context(), testUserID))
}

const emptyKeyboard = `{"inline_keyboard":[]}`

// A finished batch without failures offers only «Мои документы».
func TestNotifier_UploadOutcome_doneWithoutFailuresOffersMyDocs(t *testing.T) {
	e := newNotifierEnv(t)
	sm, docs := e.batch(t, docsbot.BackupBackedUp, docsbot.BackupBackedUp)

	require.NoError(t, e.n.UploadOutcome(t.Context(), testUserID, docs[0].ID))

	edits := e.api.callsOf("editMessageText")
	require.Len(t, edits, 1, "edit must not be rejected and retried")
	require.Contains(t, edits[0].text, "✅")
	require.True(t, edits[0].hasMarkup)
	require.Contains(t, edits[0].markup, "Мои документы")
	require.NotContains(t, edits[0].markup, "Повторить")
	require.True(t, e.status.msgs[sm.ID].Closed)
}

// A batch still in progress has no keyboard: the edit must clear it with an
// explicit empty keyboard, never a JSON null (Telegram answers 400 to that).
func TestNotifier_UploadOutcome_inProgressClearsKeyboard(t *testing.T) {
	e := newNotifierEnv(t)
	_, docs := e.batch(t, docsbot.BackupBackedUp, docsbot.BackupUploading)

	require.NoError(t, e.n.UploadOutcome(t.Context(), testUserID, docs[0].ID))

	edits := e.api.callsOf("editMessageText")
	require.Len(t, edits, 1)
	require.True(t, edits[0].hasMarkup)
	require.Equal(t, emptyKeyboard, edits[0].markup)
}

func TestNotifier_UploadOutcome_retryButtonRemovedAfterRecovery(t *testing.T) {
	e := newNotifierEnv(t)
	_, docs := e.batch(t, docsbot.BackupBackedUp, docsbot.BackupFailed)

	require.NoError(t, e.n.UploadOutcome(t.Context(), testUserID, docs[1].ID))
	edits := e.api.callsOf("editMessageText")
	require.Len(t, edits, 1)
	require.Contains(t, edits[0].markup, "Повторить")
	require.Contains(t, edits[0].markup, "d:rsm:")

	require.NoError(t, e.repo.SetBackupStatus(t.Context(), testUserID, docs[1].ID, docsbot.BackupBackedUp))
	require.NoError(t, e.n.UploadOutcome(t.Context(), testUserID, docs[1].ID))

	edits = e.api.callsOf("editMessageText")
	require.Len(t, edits, 2)
	require.NotContains(t, edits[1].markup, "Повторить", "retry button is removed once nothing failed")
	require.Contains(t, edits[1].markup, "Мои документы")
}

func TestNotifier_UploadOutcome_resendWithoutFailuresHasNoMarkup(t *testing.T) {
	e := newNotifierEnv(t)
	e.api.editErr = "Bad Request: message to edit not found"
	_, docs := e.batch(t, docsbot.BackupBackedUp, docsbot.BackupUploading)

	require.NoError(t, e.n.UploadOutcome(t.Context(), testUserID, docs[0].ID))

	edits := e.api.callsOf("editMessageText")
	require.Len(t, edits, 1)
	require.Equal(t, emptyKeyboard, edits[0].markup, "the failed edit clears the keyboard, never null")

	sends := e.api.callsOf("sendMessage")
	require.Len(t, sends, 1)
	require.False(t, sends[0].hasMarkup, "no reply_markup, not even null")
}
