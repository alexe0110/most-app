package telegram

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// albumDebounce is how long the adapter waits after the last photo of an album
// before it asks for one category for the whole album.
const albumDebounce = 1500 * time.Millisecond

// AfterFunc schedules f to run after d and returns a function that cancels it
// (reporting whether it was cancelled in time), like time.AfterFunc. Injected so
// tests can fire the album debounce deterministically.
type AfterFunc func(d time.Duration, f func()) (stop func() bool)

// Option tunes the docs adapter at Register.
type Option func(*docs)

// WithAlbumTimer replaces the album debounce timer; used by tests.
func WithAlbumTimer(after AfterFunc) Option {
	return func(d *docs) { d.albums.after = after }
}

// WithRenames shares rename state with a Notifier so a status-message redraw can
// end a rename prompt shown on that message.
func WithRenames(r *Renames) Option {
	return func(d *docs) { d.renames = r.store }
}

// WithExportPace replaces the pauses of an export: file follows a single file,
// perAlbumFile is multiplied by the size of the album before it; used by tests.
func WithExportPace(file, perAlbumFile time.Duration) Option {
	return func(d *docs) { d.pauseFile, d.pauseAlbum = file, perAlbumFile }
}

// WithDriveFolder sets the Drive root folder name shown in /start; an empty
// name keeps the default.
func WithDriveFolder(name string) Option {
	return func(d *docs) {
		if name != "" {
			d.driveFolder = name
		}
	}
}

func realAfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// albumState is what the adapter has learned about one album so far. Only the
// in-flight picker is held in memory; the documents themselves are in the DB.
type albumState struct {
	chatID  int64
	user    docsbot.UserID
	pickDoc docsbot.DocumentID // document the picker's buttons carry
	// pickDocGroup is that document's own album, to prefer one of this album.
	pickDocGroup string
	fresh        int           // documents awaiting a category
	dups         int           // files that were already saved
	reject       docsbot.Reply // first limit rejection, shown once
	group        string
	images       []albumImage // fresh image files, for the PDF offer
	nonImage     bool         // the album holds a file the PDF can't take
	stop         func() bool
}

// albumImage is one fresh image of an album. msgID keeps the page order: album
// photos arrive as separate updates in any order.
type albumImage struct {
	msgID  int
	fileID string
	size   int64
	docID  docsbot.DocumentID
}

// albumBuffer debounces albums per media_group_id.
type albumBuffer struct {
	mu    sync.Mutex
	after AfterFunc
	m     map[string]*albumState
}

func newAlbumBuffer() *albumBuffer {
	return &albumBuffer{after: realAfterFunc, m: map[string]*albumState{}}
}

// add applies update to the album's state and (re)arms the flush timer; flush
// runs once, albumDebounce after the last add, with a snapshot of the state.
func (a *albumBuffer) add(key string, update func(*albumState), flush func(albumState)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.m[key]
	if st == nil {
		st = &albumState{}
		a.m[key] = st
	}
	update(st)
	if st.stop != nil {
		st.stop()
	}
	st.stop = a.after(albumDebounce, func() {
		a.mu.Lock()
		if a.m[key] != st {
			a.mu.Unlock()
			return
		}
		delete(a.m, key)
		snapshot := *st
		a.mu.Unlock()
		flush(snapshot)
	})
}

// collectAlbum records one received album file and schedules the single picker.
func (d *docs) collectAlbum(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, group string, rep docsbot.Reply, msgID int, in docsbot.IncomingFile) {
	detached := context.WithoutCancel(ctx) // the flush outlives this update's request
	isImage := in.Kind == docsbot.KindPhoto || supportedImageMIME(in.MIME)
	d.albums.add(fmt.Sprintf("%s:%s", user, group), func(st *albumState) {
		st.chatID, st.user, st.group = chatID, user, group
		if !isImage {
			st.nonImage = true
		}
		switch r := rep.(type) {
		case docsbot.NeedCategory:
			st.fresh++
			if isImage {
				st.images = append(st.images, albumImage{msgID: msgID, fileID: in.FileID, size: in.FileSize, docID: r.Doc.ID})
			}
			// Prefer a document of this album: the picker saves the album of its anchor.
			if st.pickDoc == "" || (r.Doc.MediaGroupID == group && st.pickDocGroup != group) {
				st.pickDoc, st.pickDocGroup = r.Doc.ID, r.Doc.MediaGroupID
			}
		case docsbot.AlreadySaved:
			st.dups++
		default:
			if st.reject == nil {
				st.reject = rep
			}
		}
	}, func(st albumState) { d.flushAlbum(detached, b, st) })
}

// flushAlbum sends the one category picker for an album, with a count of the
// files that were already saved, and the limit notice if any file was refused.
func (d *docs) flushAlbum(ctx context.Context, b *bot.Bot, st albumState) {
	switch {
	case d.offersPDF(st):
		text := fmt.Sprintf(textAlbumChoice, st.fresh, plural(st.fresh, "изображение", "изображения", "изображений"))
		if st.dups > 0 {
			text += "\n\n" + fmt.Sprintf(textAlbumDuplicates, st.dups)
		}
		token := d.registerAlbum(st.user, st.group, st.images)
		d.send(ctx, b, st.chatID, text, albumChoiceKeyboard(token, st.pickDoc))
	case st.fresh > 0:
		text := textChooseCategory
		if st.fresh > 1 {
			text = fmt.Sprintf(textChooseCategoryGroup, st.fresh, plural(st.fresh, "файл", "файла", "файлов"))
		}
		if st.dups > 0 {
			text += "\n\n" + fmt.Sprintf(textAlbumDuplicates, st.dups)
		}
		d.send(ctx, b, st.chatID, text, categoryKeyboard(actCategoryGroup, st.pickDoc))
	case st.dups > 0:
		reply(ctx, b, d.log, st.chatID, fmt.Sprintf(textAlbumDuplicates, st.dups))
	}
	if text, ok := rejectionText(st.reject); ok {
		reply(ctx, b, d.log, st.chatID, text)
	}
}

// offersPDF reports whether the album gets the "one PDF or one by one" choice:
// two or more fresh files, all of them images, none refused.
func (d *docs) offersPDF(st albumState) bool {
	return d.registerAlbum != nil && st.fresh >= 2 && len(st.images) == st.fresh && !st.nonImage && st.reject == nil
}

// onGroupPick turns the album choice into the category picker for the whole
// album: d:gpk:<docID>.
func (d *docs) onGroupPick(ctx context.Context, b *bot.Bot, u *models.Update, parts []string) {
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	d.editMessage(ctx, b, chatID, msgID, textChooseCategoryAlbum, categoryKeyboard(actCategoryGroup, id))
}

// rejectionText is the notice for an intake limit reply.
func rejectionText(rep docsbot.Reply) (string, bool) {
	switch rep.(type) {
	case docsbot.TooManyPending:
		return textTooManyPending, true
	case docsbot.RateLimited:
		return textRateLimited, true
	case docsbot.QuotaExceeded:
		return fmt.Sprintf(textQuotaExceeded, docsbot.MaxDocumentsPerUser), true
	}
	return "", false
}
