package telegram_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
)

// capture records what the bot sent to the fake Bot API, split by method.
type capture struct {
	mu          sync.Mutex
	texts       []string     // sendMessage
	edits       []string     // editMessageText
	markups     []string     // sendMessage reply_markup
	editMarkups []string     // editMessageText reply_markup
	files       []string     // sendDocument/sendPhoto caption
	deletes     int          // deleteMessage calls
	deleteIDs   []int        // message_id of each deleteMessage
	editIDs     []int        // message_id of each editMessageText
	answers     []string     // answerCallbackQuery toast texts
	groups      []string     // sendMediaGroup "media" JSON, one per album
	markupEdits []string     // editMessageReplyMarkup reply_markup
	mediaLimit  int          // the next N sendMediaGroup calls answer 429 (retry_after 0)
	mediaDown   bool         // every sendMediaGroup fails with 400
	sendModes   []string     // sendMessage parse_mode
	editModes   []string     // editMessageText parse_mode
	inlines     []url.Values // answerInlineQuery form fields
}

func (c *capture) record(bucket *[]string, s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*bucket = append(*bucket, s)
}

// formInt reads an integer form field of a Bot API request, 0 when absent.
func formInt(r *http.Request, key string) int {
	n, _ := strconv.Atoi(r.FormValue(key))
	return n
}

// has reports whether any captured message contains sub.
func has(msgs []string, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// newBot stands up a fake Bot API and a docsbot client over in-memory fakes.
// Handlers run synchronously, so a test can assert right after ProcessUpdate.
func newBot(t *testing.T) (*bot.Bot, *capture, *fakeRepo) {
	t.Helper()
	return newBotWithLog(t, slog.New(slog.DiscardHandler))
}

// newBotWithLog is newBot with a caller-supplied logger.
func newBotWithLog(t *testing.T, log *slog.Logger) (*bot.Bot, *capture, *fakeRepo) {
	t.Helper()
	return newBotWith(t, log, fakeDrive{})
}

// newBotWith is newBotWithLog with a chosen Drive state.
func newBotWith(t *testing.T, log *slog.Logger, drive fakeDrive) (*bot.Bot, *capture, *fakeRepo) {
	t.Helper()
	return newBotOpts(t, log, drive)
}

// newBotOpts is newBotWith with adapter options.
func newBotOpts(t *testing.T, log *slog.Logger, drive fakeDrive, opts ...telegram.Option) (*bot.Bot, *capture, *fakeRepo) {
	t.Helper()
	sent := &capture{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sent.record(&sent.texts, r.FormValue("text"))
			sent.record(&sent.sendModes, r.FormValue("parse_mode"))
			sent.record(&sent.markups, r.FormValue("reply_markup"))
		case strings.HasSuffix(r.URL.Path, "/sendDocument"), strings.HasSuffix(r.URL.Path, "/sendPhoto"):
			sent.record(&sent.files, r.FormValue("caption"))
		case strings.HasSuffix(r.URL.Path, "/editMessageReplyMarkup"):
			sent.record(&sent.markupEdits, r.FormValue("reply_markup"))
		case strings.HasSuffix(r.URL.Path, "/sendMediaGroup"):
			sent.mu.Lock()
			limited, down := sent.mediaLimit > 0, sent.mediaDown
			if limited {
				sent.mediaLimit--
			}
			sent.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if limited {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 0","parameters":{"retry_after":0}}`)
				return
			}
			if down {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: wrong file identifier"}`)
				return
			}
			sent.record(&sent.groups, r.FormValue("media"))
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
			return
		case strings.HasSuffix(r.URL.Path, "/deleteMessage"):
			sent.mu.Lock()
			sent.deletes++
			sent.deleteIDs = append(sent.deleteIDs, formInt(r, "message_id"))
			sent.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
			return
		case strings.HasSuffix(r.URL.Path, "/answerInlineQuery"):
			_ = r.ParseMultipartForm(1 << 20)
			sent.mu.Lock()
			sent.inlines = append(sent.inlines, r.Form)
			sent.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
			return
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			sent.record(&sent.answers, r.FormValue("text"))
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			sent.record(&sent.edits, r.FormValue("text"))
			sent.record(&sent.editModes, r.FormValue("parse_mode"))
			sent.mu.Lock()
			sent.editIDs = append(sent.editIDs, formInt(r, "message_id"))
			sent.mu.Unlock()
			sent.record(&sent.editMarkups, r.FormValue("reply_markup"))
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":`+strconv.Itoa(sentMessageID)+`}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(ts.Close)

	b, err := bot.New("123:ABC", bot.WithServerURL(ts.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	require.NoError(t, err)

	svc, auth, repo := fakeDepsWith(drive)
	telegram.Register(b, log, svc, auth, opts...)
	return b, sent, repo
}

// sentMessageID is the message id the fake API gives every sent message.
const sentMessageID = 777

const testUserID = "11111111-1111-1111-1111-111111111111"

func commandUpdate(chatID int64, cmd string) *models.Update {
	text := "/" + cmd
	return &models.Update{Message: &models.Message{
		From: &models.User{ID: chatID},
		Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		Text: text,
		Entities: []models.MessageEntity{
			{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: len(text)},
		},
	}}
}

// dataCallback is a button tap by user 100 on message 7.
func dataCallback(data string) *models.Update {
	return dataCallbackMsg(7, data)
}

// dataCallbackMsg is dataCallback from the message with the given id.
func dataCallbackMsg(msgID int, data string) *models.Update {
	const chatID = 100
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   "cb",
		From: models.User{ID: chatID},
		Data: data,
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: msgID, Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}},
		},
	}}
}

// textUpdateID is textUpdate from user 100 with a message id (the bot deletes renamed-title messages).
func textUpdateID(msgID int, text string) *models.Update {
	u := textUpdate(text)
	u.Message.ID = msgID
	return u
}

// textUpdate is a text message from user 100 in their private chat.
func textUpdate(text string) *models.Update {
	const chatID = 100
	return &models.Update{Message: &models.Message{
		From: &models.User{ID: chatID},
		Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		Text: text,
	}}
}

func seedPassport(repo *fakeRepo, title, uid string) docsbot.Document {
	return repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: title, TgFileID: uid, TgFileUniqueID: uid,
	})
}

// lockedBuffer is a log sink safe for concurrent handlers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// fakeDrive reports a configurable connection state; by default "not connected",
// like the production stub.
type fakeDrive struct {
	connected bool
	revokeErr error // returned by Revoke and Disconnect
	gone      bool  // FileExists reports the remote file as deleted
}

func (d fakeDrive) Connected(context.Context, docsbot.UserID) (bool, error) { return d.connected, nil }

func (fakeDrive) Upload(context.Context, docsbot.UserID, docsbot.DriveUpload) (docsbot.DriveFile, error) {
	return docsbot.DriveFile{}, nil
}

func (fakeDrive) Rename(context.Context, docsbot.UserID, string, string) error { return nil }

func (fakeDrive) Move(context.Context, docsbot.UserID, string, docsbot.Category) error { return nil }

func (fakeDrive) Trash(context.Context, docsbot.UserID, string) error { return nil }

func (d fakeDrive) FileExists(context.Context, docsbot.UserID, string) (bool, error) {
	return !d.gone, nil
}

func (fakeDrive) Replace(context.Context, docsbot.UserID, string, string, []byte) error { return nil }

func (d fakeDrive) Revoke(context.Context, docsbot.UserID) error { return d.revokeErr }

func (d fakeDrive) Disconnect(context.Context, docsbot.UserID) error { return d.revokeErr }

// fakeAuth returns a fixed consent URL for the connect button.
type fakeAuth struct{}

func (fakeAuth) AuthURL(context.Context, string, string) (string, error) {
	return "https://auth.example/consent", nil
}

// fakeRepo is an in-memory docsbot.Repo for handler tests.
type fakeRepo struct {
	folders map[docsbot.Category]string // cached Drive folder ids by category
	docs    map[docsbot.DocumentID]docsbot.Document
	status  *fakeStatus // set by fakeDepsWith
	seq     int
	// inlineOff is set by tests to turn inline mode off for a user.
	inlineOff map[docsbot.UserID]bool
	inlineErr error // returned by InlineEnabled
}

var _ docsbot.Repo = (*fakeRepo)(nil)

func newFakeRepo() *fakeRepo {
	return &fakeRepo{docs: map[docsbot.DocumentID]docsbot.Document{}}
}

// seed stores a document, assigning an id and creation time if absent.
func (r *fakeRepo) seed(d docsbot.Document) docsbot.Document {
	if d.ID == "" {
		r.seq++
		d.ID = docsbot.DocumentID(fmt.Sprintf("doc-%d", r.seq))
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
	}
	if d.BackupStatus == "" {
		d.BackupStatus = docsbot.BackupNone // the column default
	}
	r.docs[d.ID] = d
	return d
}

func (r *fakeRepo) CreateDocument(_ context.Context, in docsbot.IncomingFile) (docsbot.Document, error) {
	return r.seed(docsbot.Document{
		UserID: in.UserID, Status: docsbot.StatusPending, Kind: in.Kind, Title: in.Title,
		TgFileID: in.FileID, TgFileUniqueID: in.FileUniqueID, BackupStatus: docsbot.BackupNone,
		MediaGroupID: in.MediaGroupID, FileSize: in.FileSize, PageCount: in.PageCount, Width: in.Width, Height: in.Height, MIME: in.MIME,
	}), nil
}

func (r *fakeRepo) DocumentByFileUniqueID(_ context.Context, user docsbot.UserID, uid string) (docsbot.Document, error) {
	for _, d := range r.docs {
		if d.UserID == user && d.TgFileUniqueID == uid {
			return d, nil
		}
	}
	return docsbot.Document{}, docsbot.ErrNotFound
}

func (r *fakeRepo) DocumentByID(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) (docsbot.Document, error) {
	if d, ok := r.docs[id]; ok && d.UserID == user {
		return d, nil
	}
	return docsbot.Document{}, docsbot.ErrNotFound
}

func (r *fakeRepo) PendingByMediaGroup(_ context.Context, user docsbot.UserID, group string) ([]docsbot.Document, error) {
	var out []docsbot.Document
	for _, d := range r.docs {
		if d.UserID == user && d.MediaGroupID == group && d.Status == docsbot.StatusPending {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *fakeRepo) IntakeStats(_ context.Context, user docsbot.UserID, since time.Time) (docsbot.IntakeStats, error) {
	var st docsbot.IntakeStats
	for _, d := range r.docs {
		if d.UserID != user {
			continue
		}
		st.Total++
		if d.Status == docsbot.StatusPending {
			st.Pending++
		}
		if d.CreatedAt.After(since) {
			st.Recent++
		}
	}
	return st, nil
}

func (r *fakeRepo) PurgeStalePending(_ context.Context, olderThan time.Time) (int, error) {
	n := 0
	for id, d := range r.docs {
		if d.Status == docsbot.StatusPending && d.CreatedAt.Before(olderThan) {
			delete(r.docs, id)
			n++
		}
	}
	return n, nil
}

func (r *fakeRepo) SaveDocument(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) (bool, error) {
	d, ok := r.docs[id]
	if !ok || d.Status != docsbot.StatusPending {
		return false, nil
	}
	d.Category, d.Status = cat, docsbot.StatusSaved
	r.docs[id] = d
	return true, nil
}

func (*fakeRepo) StuckSyncs(context.Context, time.Time, int) ([]docsbot.StuckUpload, error) {
	return nil, nil
}

func (*fakeRepo) StuckUploads(context.Context, time.Time, int) ([]docsbot.StuckUpload, error) {
	return nil, nil
}

func (r *fakeRepo) ResetPendingUploads(_ context.Context, user docsbot.UserID) error {
	for id, d := range r.docs {
		if d.UserID == user && d.Status == docsbot.StatusSaved &&
			(d.BackupStatus == docsbot.BackupPending || d.BackupStatus == docsbot.BackupUploading) {
			d.BackupStatus = docsbot.BackupNone
			r.docs[id] = d
		}
	}
	return nil
}

func (r *fakeRepo) MarkUploading(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	return r.setBackup(user, id, docsbot.BackupUploading)
}

func (r *fakeRepo) SetBackupStatus(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, s docsbot.BackupStatus) error {
	return r.setBackup(user, id, s)
}

func (r *fakeRepo) FailBackup(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, reason docsbot.BackupReason) error {
	if d, ok := r.docs[id]; ok && d.UserID == user {
		d.BackupStatus, d.BackupReason = docsbot.BackupFailed, reason
		r.docs[id] = d
	}
	return nil
}

// setBackup is a user-scoped update: a foreign or missing document is a no-op.
func (r *fakeRepo) setBackup(user docsbot.UserID, id docsbot.DocumentID, s docsbot.BackupStatus) error {
	if d, ok := r.docs[id]; ok && d.UserID == user {
		d.BackupStatus, d.BackupReason = s, ""
		r.docs[id] = d
	}
	return nil
}

// ClaimFailed mirrors ClaimFailedForRetry: failed -> pending, once.
func (r *fakeRepo) ClaimFailed(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) (bool, error) {
	d, ok := r.docs[id]
	if !ok || d.UserID != user || d.BackupStatus != docsbot.BackupFailed {
		return false, nil
	}
	d.BackupStatus = docsbot.BackupPending
	r.docs[id] = d
	return true, nil
}

func (r *fakeRepo) TitleExistsInCategory(_ context.Context, user docsbot.UserID, cat docsbot.Category, title string) (bool, error) {
	for _, d := range r.docs {
		if d.UserID == user && d.Category == cat && d.Status == docsbot.StatusSaved && d.Title == title {
			return true, nil
		}
	}
	return false, nil
}

func (r *fakeRepo) SetTitle(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, title string) error {
	return r.mutate(user, id, func(d *docsbot.Document) { d.Title = title })
}

func (r *fakeRepo) SetCategory(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) error {
	return r.mutate(user, id, func(d *docsbot.Document) { d.Category = cat })
}

// mutate applies fn to the user's document, or ErrNotFound like the :execrows queries.
func (r *fakeRepo) mutate(user docsbot.UserID, id docsbot.DocumentID, fn func(*docsbot.Document)) error {
	d, ok := r.docs[id]
	if !ok || d.UserID != user {
		return docsbot.ErrNotFound
	}
	fn(&d)
	r.docs[id] = d
	return nil
}

func (r *fakeRepo) DeleteDocument(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	if d, ok := r.docs[id]; !ok || d.UserID != user {
		return docsbot.ErrNotFound
	}
	delete(r.docs, id)
	return nil
}

func (r *fakeRepo) savedFor(user docsbot.UserID) []docsbot.Document {
	var out []docsbot.Document
	for _, d := range r.docs {
		if d.UserID == user && d.Status == docsbot.StatusSaved {
			out = append(out, d)
		}
	}
	return out
}

func (r *fakeRepo) CountSaved(_ context.Context, user docsbot.UserID) (int, error) {
	return len(r.savedFor(user)), nil
}

func (r *fakeRepo) CountNotBackedUp(_ context.Context, user docsbot.UserID) (int, error) {
	n := 0
	for _, d := range r.savedFor(user) {
		if !d.InDrive() && d.BackupStatus != docsbot.BackupTooLarge { // mirrors the SQL
			n++
		}
	}
	return n, nil
}

func (r *fakeRepo) CategoryCounts(_ context.Context, user docsbot.UserID) ([]docsbot.CategoryCount, error) {
	byCat := map[docsbot.Category]int{}
	for _, d := range r.savedFor(user) {
		byCat[d.Category]++
	}
	var out []docsbot.CategoryCount
	for c, n := range byCat { // raw and unordered, like the SQL: the service folds and sorts
		out = append(out, docsbot.CategoryCount{Category: c, Count: n})
	}
	return out, nil
}

func (r *fakeRepo) ListByCategory(_ context.Context, user docsbot.UserID, cat docsbot.Category, limit, offset int) ([]docsbot.Document, error) {
	var all []docsbot.Document
	for _, d := range r.savedFor(user) {
		if d.Category == cat || (cat == docsbot.CatOther && !d.Category.Valid()) {
			all = append(all, d)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if offset >= len(all) {
		return nil, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}

func (r *fakeRepo) Search(_ context.Context, user docsbot.UserID, q string, limit int) ([]docsbot.Document, error) {
	q = strings.ToLower(q)
	var out []docsbot.Document
	for _, d := range r.savedFor(user) {
		if strings.Contains(strings.ToLower(d.Title), q) {
			out = append(out, d)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeRepo) recentFor(user docsbot.UserID) []docsbot.Document {
	out := r.savedFor(user)
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

func page(docs []docsbot.Document, limit, offset int) []docsbot.Document {
	if offset >= len(docs) {
		return nil
	}
	docs = docs[offset:]
	if len(docs) > limit {
		docs = docs[:limit]
	}
	return docs
}

func (r *fakeRepo) InlineSearch(_ context.Context, user docsbot.UserID, q string, cats []docsbot.Category, limit, offset int) ([]docsbot.Document, error) {
	q = strings.ToLower(q)
	var match []docsbot.Document
	for _, d := range r.recentFor(user) {
		if strings.Contains(strings.ToLower(d.Title), q) || slices.Contains(cats, d.Category) {
			match = append(match, d)
		}
	}
	return page(match, limit, offset), nil
}

func (r *fakeRepo) RecentSaved(_ context.Context, user docsbot.UserID, limit, offset int) ([]docsbot.Document, error) {
	return page(r.recentFor(user), limit, offset), nil
}

func (r *fakeRepo) InlineEnabled(_ context.Context, user docsbot.UserID) (bool, error) {
	if r.inlineErr != nil {
		return false, r.inlineErr
	}
	return !r.inlineOff[user], nil
}

func (r *fakeRepo) SetInlineEnabled(_ context.Context, user docsbot.UserID, on bool) error {
	if r.inlineOff == nil {
		r.inlineOff = map[docsbot.UserID]bool{}
	}
	r.inlineOff[user] = !on
	return nil
}

// fakeUploads is a no-op Uploads port: handler tests assert on the repo and the
// sent messages, not on the queue.
// With repo set it also marks the document pending, like the real enqueue.
type fakeUploads struct{ repo *fakeRepo }

func (u fakeUploads) EnqueueUpload(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID) error {
	if u.repo != nil {
		d := u.repo.docs[id]
		d.BackupStatus = docsbot.BackupPending
		u.repo.docs[id] = d
	}
	return nil
}

func (fakeUploads) RequeueUpload(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	return nil
}

func (fakeUploads) CancelUploads(context.Context, docsbot.UserID) error { return nil }

func (fakeUploads) CancelUserJobs(context.Context, docsbot.UserID) error { return nil }

// fakeSyncs is a docsbot.Syncs over fakeRepo that mirrors the SQL decisions: a
// backed-up document with a live file goes sync-pending; no job is run.
type fakeSyncs struct{ repo *fakeRepo }

func (s fakeSyncs) change(user docsbot.UserID, id docsbot.DocumentID, fn func(*docsbot.Document)) (bool, error) {
	d, ok := s.repo.docs[id]
	if !ok || d.UserID != user {
		return false, docsbot.ErrNotFound
	}
	fn(&d)
	d.MetaVersion++
	queued := d.BackupStatus == docsbot.BackupBackedUp && d.SyncStatus != docsbot.SyncMissing
	if queued {
		d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	}
	s.repo.docs[id] = d
	return queued, nil
}

func (s fakeSyncs) SetTitle(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, title string) (bool, error) {
	return s.change(user, id, func(d *docsbot.Document) { d.Title = title })
}

func (s fakeSyncs) SetCategory(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) (bool, error) {
	return s.change(user, id, func(d *docsbot.Document) { d.Category = cat })
}

func (fakeSyncs) EnqueueSync(context.Context, docsbot.UserID, docsbot.DocumentID) error { return nil }

func (s fakeSyncs) MarkBackedUp(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, driveFileID string, startVersion, pages int) (bool, error) {
	d, ok := s.repo.docs[id]
	if !ok || d.UserID != user {
		return false, docsbot.ErrNotFound
	}
	d.BackupStatus, d.DriveFileID = docsbot.BackupBackedUp, driveFileID
	if pages > 0 {
		d.PageCount = pages
	}
	queued := d.MetaVersion != startVersion
	if queued {
		d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	}
	s.repo.docs[id] = d
	return queued, nil
}

// ReplaceContent mirrors ReplaceDocumentContent: the guarded update and the
// unique (user, file) constraint.
func (s fakeSyncs) ReplaceContent(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, in docsbot.IncomingFile, cloud bool) (bool, error) {
	d, ok := s.repo.docs[id]
	if !ok || d.UserID != user ||
		d.BackupStatus == docsbot.BackupPending || d.BackupStatus == docsbot.BackupUploading ||
		d.SyncStatus == docsbot.SyncPending || d.SyncStatus == docsbot.SyncMissing {
		return false, nil
	}
	for _, o := range s.repo.docs {
		if o.ID != id && o.UserID == user && o.TgFileUniqueID == in.FileUniqueID {
			return false, docsbot.ErrDuplicateFile
		}
	}
	d.Kind, d.TgFileID, d.TgFileUniqueID, d.FileSize, d.MIME, d.PageCount = in.Kind, in.FileID, in.FileUniqueID, in.FileSize, in.MIME, in.PageCount
	d.Width, d.Height = in.Width, in.Height
	d.MetaVersion++
	if cloud && d.BackupStatus == docsbot.BackupBackedUp {
		d.ContentDirty = true
		d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	}
	s.repo.docs[id] = d
	return true, nil
}

func (s fakeSyncs) RetrySync(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) (bool, error) {
	d, ok := s.repo.docs[id]
	if !ok || d.UserID != user || d.SyncStatus != docsbot.SyncFailed {
		return false, nil
	}
	d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	s.repo.docs[id] = d
	return true, nil
}

func (s fakeSyncs) DeleteDocument(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, trash bool) (bool, error) {
	d, ok := s.repo.docs[id]
	if !ok || d.UserID != user {
		return false, docsbot.ErrNotFound
	}
	delete(s.repo.docs, id)
	live := d.BackupStatus == docsbot.BackupBackedUp && d.DriveFileID != "" && d.SyncStatus != docsbot.SyncMissing
	return live && !trash, nil
}

func (fakeSyncs) RequeueSync(context.Context, docsbot.UserID, docsbot.DocumentID) error { return nil }

// fakeDeps builds a Service over in-memory fakes, plus the fake authorizer.
func fakeDeps() (*docsbot.Service, telegram.Authorizer, *fakeRepo) {
	return fakeDepsWith(fakeDrive{})
}

// fakeDepsWith is fakeDeps with a chosen Drive state. Files are never reached
// and uploads is a no-op.
func fakeDepsWith(drive fakeDrive) (*docsbot.Service, telegram.Authorizer, *fakeRepo) {
	repo := newFakeRepo()
	repo.status = newFakeStatus(repo)
	return docsbot.NewService(repo, drive, nil, fakeUploads{repo: repo}, repo, repo.status, fakeSyncs{repo: repo}), fakeAuth{}, repo
}

func (r *fakeRepo) MarkSynced(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, version int) (bool, error) {
	d, ok := r.docs[id]
	if !ok || d.UserID != user || d.MetaVersion != version || d.SyncStatus != docsbot.SyncPending {
		return false, nil
	}
	d.SyncStatus, d.SyncReason, d.ContentDirty = docsbot.SyncOK, "", false
	r.docs[id] = d
	return true, nil
}

// TransitionSyncStatus mirrors the conditional UPDATE.
func (r *fakeRepo) TransitionSyncStatus(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, remoteID string, from, to docsbot.SyncStatus) (bool, error) {
	d, ok := r.docs[id]
	if !ok || d.UserID != user || d.SyncStatus != from || d.BackupStatus != docsbot.BackupBackedUp || d.DriveFileID != remoteID {
		return false, nil
	}
	d.SyncStatus, d.SyncReason = to, ""
	r.docs[id] = d
	return true, nil
}

func (r *fakeRepo) SetSyncStatus(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, s docsbot.SyncStatus, reason string) error {
	if d, ok := r.docs[id]; ok && d.UserID == user {
		d.SyncStatus, d.SyncReason = s, reason
		r.docs[id] = d
	}
	return nil
}

// ClaimTransferable mirrors ClaimTransferableDocuments: none/failed -> pending.
func (r *fakeRepo) ClaimTransferable(_ context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	return r.claim(user, docsbot.BackupNone, docsbot.BackupFailed), nil
}

// ClaimAllFailed mirrors ClaimFailedDocuments: failed -> pending.
func (r *fakeRepo) ClaimAllFailed(_ context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	return r.claim(user, docsbot.BackupFailed), nil
}

func (r *fakeRepo) claim(user docsbot.UserID, from ...docsbot.BackupStatus) []docsbot.DocumentID {
	var out []docsbot.DocumentID
	for _, d := range r.savedFor(user) {
		if slices.Contains(from, d.BackupStatus) {
			d.BackupStatus = docsbot.BackupPending
			r.docs[d.ID] = d
			out = append(out, d.ID)
		}
	}
	return out
}

func (r *fakeRepo) BackupStatusCounts(_ context.Context, user docsbot.UserID) ([]docsbot.BackupStatusCount, error) {
	byStatus := map[docsbot.BackupStatus]int{}
	for _, d := range r.savedFor(user) {
		byStatus[d.BackupStatus]++
	}
	out := make([]docsbot.BackupStatusCount, 0, len(byStatus))
	for st, n := range byStatus {
		out = append(out, docsbot.BackupStatusCount{Status: st, Count: n})
	}
	return out, nil
}

func (r *fakeRepo) SavedForExport(_ context.Context, user docsbot.UserID, limit int) ([]docsbot.Document, error) {
	docs := r.savedFor(user)
	sort.Slice(docs, func(i, j int) bool { return docs[i].CreatedAt.Before(docs[j].CreatedAt) }) // oldest first, like the SQL
	if len(docs) > limit {
		docs = docs[:limit]
	}
	return docs, nil
}

func (r *fakeRepo) DeleteAllFolders(_ context.Context, _ docsbot.UserID) error {
	clear(r.folders)
	return nil
}

func (r *fakeRepo) DeleteUserData(_ context.Context, user docsbot.UserID) error {
	for id, d := range r.docs {
		if d.UserID == user {
			delete(r.docs, id)
		}
	}
	return nil
}

var _ docsbot.Folders = (*fakeRepo)(nil)

func (r *fakeRepo) CategoryFolderID(_ context.Context, _ docsbot.UserID, cat docsbot.Category) (string, bool, error) {
	id, ok := r.folders[cat]
	return id, ok, nil
}

// fakeStatus is an in-memory docsbot.StatusMessages over the fake repo's documents.
type fakeStatus struct {
	repo *fakeRepo
	msgs map[docsbot.StatusMessageID]docsbot.StatusMessage
	seq  int
}

var _ docsbot.StatusMessages = (*fakeStatus)(nil)

func newFakeStatus(repo *fakeRepo) *fakeStatus {
	return &fakeStatus{repo: repo, msgs: map[docsbot.StatusMessageID]docsbot.StatusMessage{}}
}

func (s *fakeStatus) OpenForUser(_ context.Context, user docsbot.UserID) (docsbot.StatusMessage, bool, error) {
	var best docsbot.StatusMessage
	found := false
	for _, m := range s.msgs {
		if m.UserID == user && !m.Closed && (!found || m.CreatedAt.After(best.CreatedAt)) {
			best, found = m, true
		}
	}
	return best, found, nil
}

func (s *fakeStatus) Create(_ context.Context, user docsbot.UserID, chatID int64, messageID int, kind docsbot.StatusKind) (docsbot.StatusMessage, error) {
	s.seq++
	m := docsbot.StatusMessage{
		ID:        docsbot.StatusMessageID(fmt.Sprintf("sm-%d", s.seq)),
		UserID:    user,
		ChatID:    chatID,
		MessageID: messageID,
		Kind:      kind,
		CreatedAt: time.Now(),
	}
	s.msgs[m.ID] = m
	return m, nil
}

func (s *fakeStatus) Close(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID) error {
	if m, ok := s.msgs[id]; ok && m.UserID == user {
		m.Closed = true
		s.msgs[id] = m
	}
	return nil
}

func (s *fakeStatus) SetMessageID(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID, messageID int) error {
	if m, ok := s.msgs[id]; ok && m.UserID == user {
		m.MessageID = messageID
		s.msgs[id] = m
	}
	return nil
}

func (s *fakeStatus) Attach(_ context.Context, user docsbot.UserID, doc docsbot.DocumentID, id docsbot.StatusMessageID) error {
	if d, ok := s.repo.docs[doc]; ok && d.UserID == user {
		d.StatusMessageID = id
		s.repo.docs[doc] = d
	}
	return nil
}

func (s *fakeStatus) DocumentsIn(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID) ([]docsbot.Document, error) {
	var out []docsbot.Document
	for _, d := range s.repo.docs {
		if d.UserID == user && d.StatusMessageID == id {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeStatus) ByID(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID) (docsbot.StatusMessage, error) {
	m, ok := s.msgs[id]
	if !ok || m.UserID != user {
		return docsbot.StatusMessage{}, docsbot.ErrNotFound
	}
	return m, nil
}
