package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// errBlocked means Telegram refused delivery with 403: the user blocked the bot
// or deactivated their account. It is permanent, so callers must not retry.
var errBlocked = errors.New("telegram: bot blocked by user")

// sender is the only way the adapter talks to Telegram's message API: it traces
// each call, classifies the error and logs a failure once. The plain methods log
// a failure here (at Error) and return nothing, for handlers that can do no more.
// The try methods log nothing and return the error: the caller owns it.
type sender struct {
	api *bot.Bot
	log *slog.Logger
}

// msgOpt tunes one sender call.
type msgOpt func(*msgOpts)

type msgOpts struct {
	html    bool
	caption bool
	toast   string
	hasKB   bool // withKeyboard was given, even with a nil keyboard
	kb      *models.InlineKeyboardMarkup
}

// withHTML sends the text with HTML parse mode.
func withHTML() msgOpt { return func(o *msgOpts) { o.html = true } }

// withKeyboard sets the inline keyboard. A nil kb sends no keyboard with send,
// and removes the keyboard with edit (an omitted reply_markup keeps the old one).
func withKeyboard(kb *models.InlineKeyboardMarkup) msgOpt {
	return func(o *msgOpts) { o.hasKB, o.kb = true, kb }
}

// withToast shows text as a pop-up when answering a callback query.
func withToast(text string) msgOpt { return func(o *msgOpts) { o.toast = text } }

// onCaption makes edit change the caption of a media message instead of its text.
func onCaption() msgOpt { return func(o *msgOpts) { o.caption = true } }

func collect(opts []msgOpt) msgOpts {
	var o msgOpts
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// editMarkup is the reply_markup of an edit: omitted unless withKeyboard was
// given, and the explicit empty keyboard for a nil one.
func (o msgOpts) editMarkup() models.ReplyMarkup {
	switch {
	case !o.hasKB:
		return nil
	case o.kb == nil:
		return emptyKeyboard()
	}
	return o.kb
}

// send posts a message and returns its id, 0 if it failed (logged here).
func (s sender) send(ctx context.Context, chatID int64, text string, opts ...msgOpt) int {
	id, err := s.trySend(ctx, chatID, text, opts...)
	s.logFailure(ctx, slog.LevelError, "send telegram message", err)
	return id
}

// trySend is send that returns the error instead of logging it.
func (s sender) trySend(ctx context.Context, chatID int64, text string, opts ...msgOpt) (int, error) {
	o := collect(opts)
	params := &bot.SendMessageParams{ChatID: chatID, Text: text}
	if o.html {
		params.ParseMode = models.ParseModeHTML
	}
	if o.kb != nil { // a typed nil would marshal to "reply_markup": null, which Telegram rejects
		params.ReplyMarkup = o.kb
	}
	start := time.Now()
	msg, err := s.api.SendMessage(ctx, params)
	traceAPI(ctx, s.log, "sendMessage", start)
	if err != nil {
		return 0, apiError("sendMessage", err)
	}
	if msg == nil {
		return 0, nil
	}
	return msg.ID, nil
}

// edit rewrites the text (or, with onCaption, the caption) of a message; a
// failure is logged here.
func (s sender) edit(ctx context.Context, chatID int64, msgID int, text string, opts ...msgOpt) {
	what := "edit telegram message"
	if collect(opts).caption {
		what = "edit result caption"
	}
	s.logFailure(ctx, slog.LevelError, what, s.tryEdit(ctx, chatID, msgID, text, opts...))
}

// tryEdit is edit that returns the error instead of logging it. "Message is not
// modified" is success: the message already looks as asked.
func (s sender) tryEdit(ctx context.Context, chatID int64, msgID int, text string, opts ...msgOpt) error {
	o := collect(opts)
	var (
		op  string
		err error
	)
	start := time.Now()
	if o.caption {
		op = "editMessageCaption"
		params := &bot.EditMessageCaptionParams{ChatID: chatID, MessageID: msgID, Caption: text, ReplyMarkup: o.editMarkup()}
		if o.html {
			params.ParseMode = models.ParseModeHTML
		}
		_, err = s.api.EditMessageCaption(ctx, params)
	} else {
		op = "editMessageText"
		params := &bot.EditMessageTextParams{ChatID: chatID, MessageID: msgID, Text: text, ReplyMarkup: o.editMarkup()}
		if o.html {
			params.ParseMode = models.ParseModeHTML
		}
		_, err = s.api.EditMessageText(ctx, params)
	}
	traceAPI(ctx, s.log, op, start)
	return editError(op, err)
}

// setKeyboard replaces the keyboard of a message and keeps its text; a nil kb
// removes the keyboard. A failure is logged here.
func (s sender) setKeyboard(ctx context.Context, chatID int64, msgID int, kb *models.InlineKeyboardMarkup) {
	o := collect([]msgOpt{withKeyboard(kb)})
	start := time.Now()
	_, err := s.api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID: chatID, MessageID: msgID, ReplyMarkup: o.editMarkup(),
	})
	traceAPI(ctx, s.log, "editMessageReplyMarkup", start)
	s.logFailure(ctx, slog.LevelError, "edit telegram markup", editError("editMessageReplyMarkup", err))
}

// clearKeyboard removes the keyboard of a message and keeps its text.
func (s sender) clearKeyboard(ctx context.Context, chatID int64, msgID int) {
	s.setKeyboard(ctx, chatID, msgID, nil)
}

func editError(op string, err error) error {
	if err == nil || isNotModified(err) {
		return nil
	}
	return apiError(op, err)
}

// delete removes a message; a failure is logged here.
func (s sender) delete(ctx context.Context, chatID int64, msgID int) {
	s.logFailure(ctx, slog.LevelError, "delete message", s.tryDelete(ctx, chatID, msgID))
}

// tryDelete is delete that returns the error instead of logging it.
func (s sender) tryDelete(ctx context.Context, chatID int64, msgID int) error {
	start := time.Now()
	_, err := s.api.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msgID})
	traceAPI(ctx, s.log, "deleteMessage", start)
	if err != nil {
		return apiError("deleteMessage", err)
	}
	return nil
}

// answer stops the loading spinner of a pressed button, with a toast if given. A
// failure is logged here.
func (s sender) answer(ctx context.Context, callbackID string, opts ...msgOpt) {
	o := collect(opts)
	start := time.Now()
	_, err := s.api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackID, Text: o.toast})
	traceAPI(ctx, s.log, "answerCallbackQuery", start)
	if err != nil {
		err = apiError("answerCallbackQuery", err)
	}
	s.logFailure(ctx, slog.LevelError, "answer callback", err)
}

// logFailure is the one place a failed Telegram call is logged. An edit that
// changes nothing is no failure, and a blocked bot is routine (Info, no ids, no
// content); everything else is logged at level.
func (s sender) logFailure(ctx context.Context, level slog.Level, what string, err error, attrs ...any) {
	switch {
	case err == nil || isNotModified(err):
	case errors.Is(err, errBlocked):
		s.log.InfoContext(ctx, "telegram refused delivery: bot blocked by user", "op", what)
	default:
		s.log.Log(ctx, level, what, append([]any{"err", err}, attrs...)...)
	}
}

// ignoreBlocked turns errBlocked into nil, after logging it: the user left, which
// is not an error of the caller.
func (s sender) ignoreBlocked(ctx context.Context, err error) error {
	if !errors.Is(err, errBlocked) {
		return err
	}
	s.logFailure(ctx, slog.LevelInfo, "notify user", err)
	return nil
}

// apiError wraps a failed call. A 403 becomes errBlocked. The SDK puts the bot
// token into the request URL and a transport error carries that URL, so the
// transport cause is kept without it.
func apiError(op string, err error) error {
	var ue *url.Error
	switch {
	case errors.Is(err, bot.ErrorForbidden):
		return fmt.Errorf("%s: %w", op, errBlocked)
	case errors.As(err, &ue):
		return fmt.Errorf("%s: %w", op, ue.Err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// isNotModified reports Telegram's answer to an edit that changes nothing. The Bot
// API has no error code for it, only the description text.
func isNotModified(err error) bool {
	return errMentions(err, "message is not modified")
}

// isMessageGone reports that the message to edit or delete no longer exists or
// can't be edited (too old, not the bot's own).
func isMessageGone(err error) bool {
	return errMentions(err, "message to edit not found", "message can't be edited", "message to delete not found")
}

func errMentions(err error, texts ...string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, t := range texts {
		if strings.Contains(msg, t) {
			return true
		}
	}
	return false
}

// emptyKeyboard is the markup that removes an inline keyboard. The slice must be
// non-nil: a nil one marshals to "inline_keyboard": null, which Telegram rejects
// ("must be of type Array"); an empty array clears the keyboard.
func emptyKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
}

// traceAPI logs one Telegram API call at Debug: method and timing only. The
// error, if any, is logged once by the caller.
func traceAPI(ctx context.Context, log *slog.Logger, op string, start time.Time) {
	log.DebugContext(ctx, "telegram api", "op", op, "duration_ms", time.Since(start).Milliseconds())
}
