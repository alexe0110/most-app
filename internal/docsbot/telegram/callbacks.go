package telegram

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// cbPrefix tags every inline button the docs feature owns, keeping it clear of
// the /convert buttons ("conv:", see the converter package).
const cbPrefix = "d:"

// Callback actions. Data is "d:<action>" or "d:<action>:<docID>[:<arg>]".
const (
	actCategory      = "cat" // pick a category for a new document
	actCategoryGroup = "ga"  // pick a category for a whole album: d:ga:<docID>:<slug>
	actGroupPick     = "gpk" // album choice -> category picker: d:gpk:<docID>
	actSetCategory   = "sc"  // change an existing document's category
	actChangeCat     = "chc" // show the category picker for a card
	actSend          = "snd" // resend the original file
	actRename        = "rn"  // ask for a new title: the card message becomes the prompt
	actRenameCancel  = "rnc" // cancel the rename prompt: d:rnc:<docID>
	actDeleteAsk     = "del" // ask to confirm deletion
	actDeleteYes     = "dy"  // confirm deletion
	actDeleteNo      = "dn"  // cancel deletion
	actListCat       = "lc"  // open a category page: d:lc:<slug>:<page>
	actListBack      = "lb"  // back to the category list
	actTransfer      = "tr"  // transfer saved documents to Drive
	actTransferYes   = "try" // confirm the transfer offer
	actTransferNo    = "trn" // decline the transfer offer
	actRetry         = "rt"  // retry a failed backup: d:rt:<docID>
	actRetrySync     = "rs"  // retry a failed cloud sync: d:rs:<docID>
	actDisconnect    = "dc"  // disconnect Google Drive
	actRetryAll      = "rta" // retry every failed backup
	actExport        = "exp" // resend all documents from Telegram
	actDeleteData    = "dd"  // start the delete-all-data confirmation
	actRetryBatch    = "rsm" // retry failed docs in a status message: d:rsm:<smID>
	actOpenCard      = "oc"  // open a card: d:oc:<docID>:<l|s>[:<cat>:<page>]
	actSearchBack    = "sb"  // re-run the last search
	actInline        = "inl" // switch inline mode: d:inl:<1|0> is the wanted state
	actNoop          = "noop"
)

// callbackData is the parsed data of an inline button: "d:<action>" plus, for the
// actions that carry one, ":<docID>", plus ":<arg>" for each argument. Telegram
// limits the data to 64 bytes, so it holds ids and short tokens only.
type callbackData struct {
	action string
	docID  docsbot.DocumentID
	args   []string
}

// carriesDocID reports whether the third segment of the action's data is a
// document id; for every other action it is the first argument.
func carriesDocID(action string) bool {
	switch action {
	case actCategory, actCategoryGroup, actGroupPick, actSetCategory, actChangeCat, actSend,
		actRename, actRenameCancel, actDeleteAsk, actDeleteYes, actDeleteNo, actOpenCard,
		actRetry, actRetrySync:
		return true
	}
	return false
}

// parseCallback splits button data of the docs feature; false when it is not.
// The parts are not validated: a handler refuses what it cannot use.
func parseCallback(data string) (callbackData, bool) {
	if !strings.HasPrefix(data, cbPrefix) {
		return callbackData{}, false
	}
	segs := strings.Split(strings.TrimPrefix(data, cbPrefix), ":")
	cd := callbackData{action: segs[0]}
	rest := segs[1:]
	if carriesDocID(cd.action) && len(rest) > 0 {
		cd.docID, rest = docsbot.DocumentID(rest[0]), rest[1:]
	}
	if len(rest) > 0 {
		cd.args = rest
	}
	return cd, true
}

// String is the inverse of parseCallback for data that has something after the
// action. An action that carries a document id writes its segment, even an empty
// one, when arguments follow, so they keep their position. Data whose only
// segment after the action is an empty id ("d:oc:") comes back without it.
func (c callbackData) String() string {
	var sb strings.Builder
	sb.WriteString(cbPrefix)
	sb.WriteString(c.action)
	if c.docID != "" || (len(c.args) > 0 && carriesDocID(c.action)) {
		sb.WriteString(":" + string(c.docID))
	}
	for _, a := range c.args {
		sb.WriteString(":" + a)
	}
	return sb.String()
}

// category reads the category slug that follows the document id in the data of
// "d:<action>:<docID>:<slug>".
func (c callbackData) category() (docsbot.Category, bool) {
	if len(c.args) < 1 {
		return "", false
	}
	return docsbot.ParseCategory(c.args[0])
}

// press is one inline-button press, read once from the update so that handlers
// neither re-parse the data nor reach into the Telegram models.
type press struct {
	queryID string
	tgUser  int64          // Telegram user id: the key of every per-user memory
	user    docsbot.UserID // internal id the auth middleware put in ctx
	hasUser bool
	chatID  int64
	msgID   int
	// hasMsg is false when Telegram no longer gives the message (too old); then
	// chatID and msgID are zero and msg is nil.
	hasMsg bool
	msg    *models.Message
	data   callbackData
}

func newPress(ctx context.Context, q *models.CallbackQuery) press {
	user, hasUser := userID(ctx)
	data, _ := parseCallback(q.Data) // the zero data matches no action
	p := press{queryID: q.ID, tgUser: q.From.ID, user: user, hasUser: hasUser, data: data}
	if m := q.Message.Message; m != nil {
		p.chatID, p.msgID, p.hasMsg, p.msg = m.Chat.ID, m.ID, true, m
	}
	return p
}

// docCB builds the data of a docs button; id is "" for an action without one.
func docCB(action string, id docsbot.DocumentID, args ...string) string {
	return callbackData{action: action, docID: id, args: args}.String()
}

// backFromOrig is the callback of the card's "Назад" button for an origin token:
// the category page for "l:<cat>:<page>", the last search for "s", "" otherwise.
func backFromOrig(orig string) string {
	switch {
	case strings.HasPrefix(orig, "l:"):
		return docCB(actListCat, "", strings.Split(orig, ":")[1:]...)
	case orig == "s":
		return docCB(actSearchBack, "")
	}
	return ""
}
