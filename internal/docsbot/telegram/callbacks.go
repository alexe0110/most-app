package telegram

import (
	"strings"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// cbPrefix tags every inline button the docs feature owns, keeping it clear of
// the /convert buttons ("conv:").
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

// Actions of the /convert buttons ("conv:<action>[:...]").
const (
	convActDoc    = "doc"  // conv:doc:<docID> opens the menu
	convActQuick  = "q"    // conv:q:<docID>:<op> converts a document
	convActSave   = "save" // conv:save:<token> saves the result
	convActSkip   = "skip" // conv:skip:<token> keeps only the file
	convActRepAsk = "rp"   // conv:rp:<token> asks to replace the original
	convActRepYes = "rpy"  // conv:rpy:<token> confirms the replacement
	convActRepNo  = "rpn"  // conv:rpn:<token> cancels it
	convActAlbum  = "alb"  // conv:alb:<token> merges an album into a PDF
	convActCancel = "x"    // closes the convert menu
	convActPDFOp  = "p"    // conv:p:<op> is a PDF operation of the session
)

// callbackData is the parsed data of an inline button: "<prefix><action>" plus,
// for the actions that carry one, ":<docID>", plus ":<arg>" for each argument.
// Telegram limits the data to 64 bytes, so it holds ids and short tokens only.
type callbackData struct {
	prefix string // cbPrefix or callbackPrefix
	action string
	docID  docsbot.DocumentID
	args   []string
}

// carriesDocID reports whether the third segment of the action's data is a
// document id; for every other action it is the first argument.
func carriesDocID(prefix, action string) bool {
	switch prefix {
	case cbPrefix:
		switch action {
		case actCategory, actCategoryGroup, actGroupPick, actSetCategory, actChangeCat, actSend,
			actRename, actRenameCancel, actDeleteAsk, actDeleteYes, actDeleteNo, actOpenCard,
			actRetry, actRetrySync:
			return true
		}
	case callbackPrefix:
		return action == convActDoc || action == convActQuick
	}
	return false
}

// parseCallback splits button data of either feature; false when it is neither's.
// The parts are not validated: a handler refuses what it cannot use.
func parseCallback(data string) (callbackData, bool) {
	var prefix string
	switch {
	case strings.HasPrefix(data, cbPrefix):
		prefix = cbPrefix
	case strings.HasPrefix(data, callbackPrefix):
		prefix = callbackPrefix
	default:
		return callbackData{}, false
	}
	segs := strings.Split(strings.TrimPrefix(data, prefix), ":")
	cd := callbackData{prefix: prefix, action: segs[0]}
	rest := segs[1:]
	if carriesDocID(prefix, cd.action) && len(rest) > 0 {
		cd.docID, rest = docsbot.DocumentID(rest[0]), rest[1:]
	}
	if len(rest) > 0 {
		cd.args = rest
	}
	return cd, true
}

// String is the exact inverse of parseCallback.
func (c callbackData) String() string {
	var sb strings.Builder
	sb.WriteString(c.prefix)
	sb.WriteString(c.action)
	if c.docID != "" {
		sb.WriteString(":" + string(c.docID))
	}
	for _, a := range c.args {
		sb.WriteString(":" + a)
	}
	return sb.String()
}

// docCB builds the data of a docs button; id is "" for an action without one.
func docCB(action string, id docsbot.DocumentID, args ...string) string {
	return callbackData{prefix: cbPrefix, action: action, docID: id, args: args}.String()
}

// convCB builds the data of a /convert button.
func convCB(action string, args ...string) string {
	return callbackData{prefix: callbackPrefix, action: action, args: args}.String()
}

// convDocCB is convCB for the buttons that carry a document id.
func convDocCB(action string, id docsbot.DocumentID, args ...string) string {
	return callbackData{prefix: callbackPrefix, action: action, docID: id, args: args}.String()
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
