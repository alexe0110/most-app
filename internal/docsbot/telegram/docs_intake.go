package telegram

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// incomingFile reads the file out of a message; ok is false for a non-file one.
func incomingFile(m *models.Message, user docsbot.UserID) (docsbot.IncomingFile, bool) {
	switch {
	case len(m.Photo) > 0:
		p := m.Photo[len(m.Photo)-1] // Telegram sorts sizes ascending
		return docsbot.IncomingFile{
			UserID: user, Kind: docsbot.KindPhoto,
			FileID: p.FileID, FileUniqueID: p.FileUniqueID, Title: photoTitle(m),
			FileSize: int64(p.FileSize), MIME: "", MediaGroupID: m.MediaGroupID,
			Width: p.Width, Height: p.Height,
		}, true
	case m.Document != nil:
		return docsbot.IncomingFile{
			UserID: user, Kind: docsbot.KindDocument,
			FileID: m.Document.FileID, FileUniqueID: m.Document.FileUniqueID, Title: documentTitle(m),
			FileSize: m.Document.FileSize, MIME: m.Document.MimeType, MediaGroupID: m.MediaGroupID,
		}, true
	default:
		return docsbot.IncomingFile{}, false
	}
}

// documentTitle is the file name without its extension, or the caption; "" when neither exists (the domain
// then assigns a default name).
func documentTitle(m *models.Message) string {
	if m.Document.FileName != "" {
		return docsbot.TrimExt(m.Document.FileName)
	}
	return m.Caption
}

// photoTitle is the caption, "" when absent.
func photoTitle(m *models.Message) string {
	return m.Caption
}

// receive stores an incoming file and asks for a category (or reports a dup).
func (d *docs) receive(ctx context.Context, b *bot.Bot, u *models.Update) {
	user, ok := userID(ctx)
	if !ok {
		return
	}
	in, ok := incomingFile(u.Message, user)
	if !ok {
		return
	}
	chatID := u.Message.Chat.ID
	rep, err := d.svc.Receive(ctx, in)
	if err != nil {
		d.fail(ctx, b, chatID, "receive document", err)
		return
	}
	if in.MediaGroupID != "" {
		d.logReceived(ctx, user, rep)
		d.collectAlbum(ctx, b, chatID, user, in.MediaGroupID, rep, u.Message.ID, in)
		return
	}
	switch r := rep.(type) {
	case docsbot.NeedCategory:
		d.logReceived(ctx, user, rep)
		d.out(b).send(ctx, chatID, textChooseCategory, withKeyboard(pickerKeyboard(r.Doc)))
	case docsbot.AlreadySaved:
		d.logReceived(ctx, user, rep)
		d.sendAlreadySaved(ctx, b, chatID, user, r.Doc.ID)
	default:
		d.log.InfoContext(ctx, "document refused", "user_id", user, "reason", fmt.Sprintf("%T", rep))
		if text, ok := rejectionText(rep); ok {
			d.out(b).send(ctx, chatID, text)
		}
	}
}

// sendAlreadySaved shows the card of a document the user already has.
func (d *docs) sendAlreadySaved(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID) {
	card, err := d.svc.Card(ctx, user, id)
	if err != nil {
		d.fail(ctx, b, chatID, "load card", err)
		return
	}
	d.sendCard(ctx, b, chatID, card.Doc, textAlreadySaved, card.CategoryFolderID)
}

// sendReplaced shows the card of a document whose file the converter replaced.
func (d *docs) sendReplaced(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID) {
	card, err := d.svc.Card(ctx, user, id)
	if err != nil {
		d.fail(ctx, b, chatID, "load card", err)
		return
	}
	d.log.InfoContext(ctx, "original replaced", "user_id", user, "doc_id", id)
	d.sendCard(ctx, b, chatID, card.Doc, textReplacedDone, card.CategoryFolderID)
}

// logReceived logs an accepted file: ids and kind only, never the title.
func (d *docs) logReceived(ctx context.Context, user docsbot.UserID, rep docsbot.Reply) {
	switch r := rep.(type) {
	case docsbot.NeedCategory:
		d.log.InfoContext(ctx, "document received", "user_id", user, "doc_id", r.Doc.ID, "kind", r.Doc.Kind)
	case docsbot.AlreadySaved:
		d.log.InfoContext(ctx, "document received", "user_id", user, "doc_id", r.Doc.ID, "kind", r.Doc.Kind, "duplicate", true)
	}
}

// onCategory saves a new document under the chosen category. The picker message
// becomes the card (Drive off) or a live upload card / batch status (Drive on).
func (d *docs) onCategory(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, cat, ok := docCategory(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	saved, err := d.svc.ChooseCategory(ctx, user, id, cat)
	if errors.Is(err, docsbot.ErrNotFound) {
		d.gone(ctx, b, chatID, msgID)
		return
	}
	if err != nil {
		d.fail(ctx, b, chatID, "choose category", err)
		return
	}
	if saved.Already {
		return // double tap: the first tap already saved and tracked it
	}
	d.renderSaved(ctx, b, chatID, msgID, user, saved)
}

// renderSaved turns the text message msgID into the saved document's card (Drive
// off) or a live upload card / batch status (Drive on), then offers Drive.
func (d *docs) renderSaved(ctx context.Context, b *bot.Bot, chatID int64, msgID int, user docsbot.UserID, saved docsbot.Saved) {
	d.log.InfoContext(ctx, "document saved", "user_id", user, "doc_id", saved.Doc.ID,
		"category", saved.Doc.Category, "backup_status", saved.Doc.BackupStatus)
	if saved.Doc.BackupStatus == docsbot.BackupPending {
		// Drive is connected: the upload is queued and tracked in a status message.
		d.trackUpload(ctx, b, chatID, user, saved.Doc, msgID)
		return
	}
	d.editCard(ctx, b, chatID, msgID, saved.Doc, "", "")
	if saved.OfferDrive {
		d.offerDrive(ctx, b, chatID, user)
	}
}

// onCategoryGroup saves a whole album under the chosen category. The picker
// message becomes the card (one file), a batch status (Drive on) or a summary.
func (d *docs) onCategoryGroup(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, cat, ok := docCategory(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	group, err := d.svc.ChooseCategoryGroup(ctx, user, id, cat)
	if errors.Is(err, docsbot.ErrNotFound) {
		d.gone(ctx, b, chatID, msgID)
		return
	}
	if err != nil {
		d.fail(ctx, b, chatID, "choose album category", err)
		return
	}
	if group.Already {
		return // double tap: the first tap already saved and tracked the album
	}
	d.log.InfoContext(ctx, "album saved", "user_id", user, "category", cat, "count", len(group.Docs))

	var tracked []docsbot.Document // queued for upload, or kept in Telegram for size
	tooLarge := 0
	for _, doc := range group.Docs {
		switch doc.BackupStatus {
		case docsbot.BackupPending:
			tracked = append(tracked, doc)
		case docsbot.BackupTooLarge:
			tooLarge++
		}
	}
	switch {
	case len(tracked) > 0:
		// Show too-large files as lines of the same status message.
		for _, doc := range group.Docs {
			if doc.BackupStatus == docsbot.BackupTooLarge {
				tracked = append(tracked, doc)
			}
		}
		d.trackGroupUpload(ctx, b, chatID, user, tracked, msgID)
	case len(group.Docs) == 1:
		d.editCard(ctx, b, chatID, msgID, group.Docs[0], "", "")
	default:
		text := fmt.Sprintf(textGroupSaved, len(group.Docs), plural(len(group.Docs), pluralFileOne, pluralFileFew, pluralFileMany), cat.Label())
		if tooLarge > 0 {
			text += "\n" + fmt.Sprintf(textGroupTooLarge, tooLarge)
		}
		d.out(b).edit(ctx, chatID, msgID, text, withKeyboard(nil))
	}
	if group.OfferDrive {
		d.offerDrive(ctx, b, chatID, user)
	}
}

// trackGroupUpload turns the album picker into one status message holding every
// document of the album.
func (d *docs) trackGroupUpload(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, docs []docsbot.Document, pickerMsgID int) {
	kind := docsbot.StatusSingle
	if len(docs) > 1 {
		kind = docsbot.StatusBatch
	}
	text, kb := renderStatusMessage(docsbot.StatusMessage{Kind: kind}, docs, nil)
	d.out(b).edit(ctx, chatID, pickerMsgID, text, withHTML(), withKeyboard(kb))
	sm, err := d.svc.OpenStatus(ctx, user, chatID, pickerMsgID, docs[0].ID, kind)
	if err != nil {
		d.fail(ctx, b, chatID, "open status", err)
		return
	}
	for _, doc := range docs[1:] {
		if err := d.svc.AttachStatus(ctx, user, doc.ID, sm.ID); err != nil {
			d.fail(ctx, b, chatID, "attach status", err)
			return
		}
	}
}

// trackUpload shows a queued document's upload status. A lone document turns the
// picker message into a live card; further documents inside the grouping window
// share one batch message, and their pickers are retired or become that batch.
func (d *docs) trackUpload(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, doc docsbot.Document, pickerMsgID int) {
	sm, reuse, err := d.svc.StatusTarget(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "status target", err)
		return
	}
	kind := docsbot.StatusSingle
	switch {
	case reuse && sm.Kind == docsbot.StatusBatch:
		if err := d.svc.AttachStatus(ctx, user, doc.ID, sm.ID); err != nil {
			d.fail(ctx, b, chatID, "attach status", err)
			return
		}
		d.out(b).delete(ctx, chatID, pickerMsgID)
		d.refreshStatus(ctx, b, user, doc.ID)
		return
	case reuse && sm.Kind == docsbot.StatusSingle:
		// The earlier single card stays as is; a new batch starts from this picker.
		kind = docsbot.StatusBatch
	case reuse && sm.Kind == docsbot.StatusTransfer:
		// An open transfer is intentionally not grouped with: this picker becomes
		// its own single card (the default below).
	}
	text, kb := renderStatusMessage(docsbot.StatusMessage{Kind: kind}, []docsbot.Document{doc}, nil)
	d.out(b).edit(ctx, chatID, pickerMsgID, text, withHTML(), withKeyboard(kb))
	if _, err := d.svc.OpenStatus(ctx, user, chatID, pickerMsgID, doc.ID, kind); err != nil {
		d.fail(ctx, b, chatID, "open status", err)
	}
}
