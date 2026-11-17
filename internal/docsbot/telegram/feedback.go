package telegram

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

const (
	// feedbackTTL is how long the bot waits for the user to write after the prompt.
	feedbackTTL = 10 * time.Minute
	// feedbackGroupTTL covers the parts of one album that arrive after the first.
	feedbackGroupTTL = time.Minute
	// maxFeedbackParts is the size of a Telegram album; further parts are ignored.
	maxFeedbackParts = 10
)

// Second segment of the feedback button data: where the request starts.
const (
	fbSettings = "s" // d:fb:s
	fbError    = "e" // d:fb:e:<scenario>:<unix time of the error>
)

// scenarioPattern is what a scenario slug may look like. The data comes from our
// own buttons; the check keeps a stale or odd button from putting text into the
// message the team reads.
var scenarioPattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// feedbackEntry is the mode a user is in: their next message is feedback. Only
// where the request came from is kept, never what the user writes.
type feedbackEntry struct {
	source      docsbot.FeedbackSource
	scenario    string    // the failed scenario, for an error source
	errAt       time.Time // when it failed, zero if unknown
	promptMsgID int       // the prompt message, to drop its "Отмена" once the message is sent
}

// feedbackGroupKey identifies an album of one user.
func feedbackGroupKey(tgUser int64, group string) string {
	return strconv.FormatInt(tgUser, 10) + ":" + group
}

// ReportButton is the «Сообщить о проблеме» button under an error of the given
// scenario. It implements converter.ProblemReporter.
func (d *docs) ReportButton(scenario string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{
		Text:         btnReportProblem,
		CallbackData: docCB(actFeedback, "", fbError, scenario, strconv.FormatInt(time.Now().Unix(), 10)),
	}
}

// DropFeedback leaves the feedback mode of the user. It implements
// converter.ProblemReporter: /convert ends it like the docs commands do.
func (d *docs) DropFeedback(tgUser int64) { d.feedbackPending.Delete(tgUser) }

// problemKeyboard is the keyboard of an error message: the report button alone.
func (d *docs) problemKeyboard(scenario string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{d.ReportButton(scenario)}}}
}

// leavingFeedback ends the feedback mode of the user who sends a command.
func (d *docs) leavingFeedback(h bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, u *models.Update) {
		if u.Message != nil && u.Message.From != nil {
			d.DropFeedback(u.Message.From.ID)
		}
		h(ctx, b, u)
	}
}

// feedback is /feedback.
func (d *docs) feedback(ctx context.Context, b *bot.Bot, u *models.Update) {
	if u.Message == nil || u.Message.From == nil {
		return
	}
	d.startFeedback(ctx, b, u.Message.Chat.ID, u.Message.From.ID, feedbackEntry{source: docsbot.FeedbackCommand})
}

// onFeedbackButton starts feedback from the settings button or the button under
// an error: d:fb:s or d:fb:e:<scenario>:<unix>.
func (d *docs) onFeedbackButton(ctx context.Context, b *bot.Bot, p press) {
	entry := feedbackEntry{source: docsbot.FeedbackSettings}
	if args := p.data.args; len(args) > 0 && args[0] == fbError {
		entry = errorEntry(args[1:])
	}
	// The chat of a private conversation has the id of its user.
	d.startFeedback(ctx, b, p.tgUser, p.tgUser, entry)
}

// errorEntry reads the scenario and the time of an error button; what does not
// parse is left out of the entry.
func errorEntry(args []string) feedbackEntry {
	e := feedbackEntry{source: docsbot.FeedbackError}
	if len(args) > 0 && scenarioPattern.MatchString(args[0]) {
		e.scenario = args[0]
	}
	if len(args) > 1 {
		if sec, err := strconv.ParseInt(args[1], 10, 64); err == nil && sec > 0 {
			e.errAt = time.Unix(sec, 0)
		}
	}
	return e
}

// startFeedback asks the user to write and arms the mode.
func (d *docs) startFeedback(ctx context.Context, b *bot.Bot, chatID, tgUser int64, entry feedbackEntry) {
	if d.feedbackChat == 0 {
		d.out(b).Send(ctx, chatID, textFeedbackUnavailable)
		return
	}
	entry.promptMsgID = d.out(b).Send(ctx, chatID, textFeedbackPrompt, tg.WithKeyboard(feedbackCancelKeyboard()))
	d.feedbackPending.Set(tgUser, entry)
}

// onFeedbackCancel leaves the feedback mode.
func (d *docs) onFeedbackCancel(ctx context.Context, b *bot.Bot, p press) {
	d.feedbackPending.Delete(p.tgUser)
	if p.hasMsg {
		d.out(b).Edit(ctx, p.chatID, p.msgID, textFeedbackCanceled, tg.WithKeyboard(nil))
	}
}

func feedbackCancelKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnCancel, CallbackData: cbPrefix + actFeedbackCancel},
	}}}
}

// ownsFeedback matches a message that is feedback: any non-command message of a
// user in the feedback mode, and the later parts of an album that started it.
// It runs before the document handlers, so such a message is neither saved nor
// searched for.
func (d *docs) ownsFeedback(u *models.Update) bool {
	m := u.Message
	if m == nil || m.From == nil || m.Chat.Type != models.ChatTypePrivate || strings.HasPrefix(m.Text, "/") {
		return false
	}
	if _, ok := d.feedbackPending.Get(m.From.ID); ok {
		return true
	}
	if m.MediaGroupID == "" {
		return false
	}
	_, ok := d.feedbackGroups.Get(feedbackGroupKey(m.From.ID, m.MediaGroupID))
	return ok
}

// feedbackAccepts reports a message the team can read: text, a photo or a file.
func feedbackAccepts(m *models.Message) bool {
	return !unsupportedMedia(m) && (m.Text != "" || len(m.Photo) > 0 || m.Document != nil)
}

// onFeedbackMessage takes the user's message as feedback. Media the bot does not
// take gets a refusal and keeps the mode, so the user can write again.
func (d *docs) onFeedbackMessage(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	user, ok := userID(ctx)
	if !ok {
		return
	}
	if m.MediaGroupID != "" {
		d.onFeedbackAlbumPart(ctx, b, m, user)
		return
	}
	if !feedbackAccepts(m) {
		d.out(b).Send(ctx, m.Chat.ID, textFeedbackUnsupported)
		return
	}
	entry, ok := d.feedbackPending.Take(m.From.ID)
	if !ok {
		return // the mode ended while the update was on its way
	}
	d.sendFeedback(ctx, b, feedbackRequest{user: user, from: m.From, chatID: m.Chat.ID, ids: []int{m.ID}, entry: entry})
}

// claimFeedbackGroup finds the mode of an album. The first part moves the mode
// from the user to the album; the group is set before the user's entry goes, so
// a part arriving in between still finds one of them. ok is false when the user
// is in no feedback mode.
func (d *docs) claimFeedbackGroup(tgUser int64, group string) (entry feedbackEntry, ok bool) {
	d.feedbackClaim.Lock()
	defer d.feedbackClaim.Unlock()
	key := feedbackGroupKey(tgUser, group)
	if e, known := d.feedbackGroups.Get(key); known {
		return e, true
	}
	e, armed := d.feedbackPending.Get(tgUser)
	if !armed {
		return feedbackEntry{}, false
	}
	d.feedbackGroups.Set(key, e)
	d.feedbackPending.Delete(tgUser)
	return e, true
}

// onFeedbackAlbumPart collects the parts of an album; the whole album is sent as
// one request once the parts stop coming.
func (d *docs) onFeedbackAlbumPart(ctx context.Context, b *bot.Bot, m *models.Message, user docsbot.UserID) {
	entry, ok := d.claimFeedbackGroup(m.From.ID, m.MediaGroupID)
	if !ok {
		return
	}
	req := feedbackRequest{user: user, from: m.From, chatID: m.Chat.ID, entry: entry}
	accepted := feedbackAccepts(m)
	detached := context.WithoutCancel(ctx) // the flush outlives this update
	d.feedbackAlbums.add(d.albums.after, feedbackGroupKey(m.From.ID, m.MediaGroupID), func(a *feedbackAlbum) {
		a.req = req
		if len(a.ids) < maxFeedbackParts {
			a.ids = append(a.ids, m.ID)
		}
		if !accepted {
			a.unsupported = true
		}
	}, func(a feedbackAlbum) { d.flushFeedbackAlbum(detached, b, a) })
}

// flushFeedbackAlbum sends a collected album, or refuses it whole when a part is
// media the bot does not take; the mode is then given back to the user.
func (d *docs) flushFeedbackAlbum(ctx context.Context, b *bot.Bot, a feedbackAlbum) {
	if a.unsupported {
		d.feedbackPending.Set(a.req.from.ID, a.req.entry)
		d.out(b).Send(ctx, a.req.chatID, textFeedbackUnsupported)
		return
	}
	req := a.req
	req.ids = a.ids
	d.sendFeedback(ctx, b, req)
}

// feedbackAlbum is the parts of one album seen so far: ids only, never content.
type feedbackAlbum struct {
	req         feedbackRequest
	unsupported bool
	ids         []int
	stop        func() bool
}

// feedbackBuffer debounces the parts of an album, like albumBuffer does for
// documents. Entries live for one debounce interval.
type feedbackBuffer struct {
	mu sync.Mutex
	m  map[string]*feedbackAlbum
}

func newFeedbackBuffer() *feedbackBuffer {
	return &feedbackBuffer{m: map[string]*feedbackAlbum{}}
}

// add applies update to the album and re-arms the flush timer; flush runs once,
// albumDebounce after the last part, with a snapshot of the album. after is the
// albums' timer, so one test hook drives both.
func (f *feedbackBuffer) add(after AfterFunc, key string, update func(*feedbackAlbum), flush func(feedbackAlbum)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.m[key]
	if a == nil {
		a = &feedbackAlbum{}
		f.m[key] = a
	}
	update(a)
	if a.stop != nil {
		a.stop()
	}
	a.stop = after(albumDebounce, func() {
		f.mu.Lock()
		if f.m[key] != a {
			f.mu.Unlock()
			return
		}
		delete(f.m, key)
		snapshot := *a
		f.mu.Unlock()
		slices.Sort(snapshot.ids)
		flush(snapshot)
	})
}

// feedbackHeader is the note above a request in the feedback chat: who wrote and
// from where. It holds nothing the user typed except their public username.
func feedbackHeader(req feedbackRequest, now time.Time) string {
	username, lang := textFeedbackNoValue, textFeedbackNoValue
	if req.from.Username != "" {
		username = "@" + req.from.Username
	}
	if req.from.LanguageCode != "" {
		lang = req.from.LanguageCode
	}
	return fmt.Sprintf(textFeedbackHeader, req.from.ID, username, lang, feedbackSourceText(req.entry), now.UTC().Format(time.DateTime))
}

func feedbackSourceText(e feedbackEntry) string {
	switch e.source {
	case docsbot.FeedbackSettings:
		return textFeedbackFromSettings
	case docsbot.FeedbackError:
		scenario := e.scenario
		if scenario == "" {
			scenario = textFeedbackNoValue
		}
		if e.errAt.IsZero() {
			return fmt.Sprintf(textFeedbackFromError, scenario)
		}
		return fmt.Sprintf(textFeedbackFromErrorAt, scenario, e.errAt.UTC().Format(time.DateTime))
	}
	return textFeedbackFromCommand
}
