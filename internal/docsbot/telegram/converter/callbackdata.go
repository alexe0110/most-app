package converter

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// callbackPrefix tags every inline button this feature owns.
const callbackPrefix = "conv:"

// Callback data for the inline buttons that carry no argument.
const (
	cbPDF         = callbackPrefix + convActPDF
	cbCompress    = callbackPrefix + convActCompress
	cbDone        = callbackPrefix + convActDone
	cbCancel      = callbackPrefix + convActCancel
	cbLimit1      = callbackPrefix + convActLimit1
	cbLimit2      = callbackPrefix + convActLimit2
	cbLimit5      = callbackPrefix + convActLimit5
	cbLimitCustom = callbackPrefix + convActLimitCustom
	cbFmtJPEG     = callbackPrefix + convActFmtJPEG
	cbFmtPNG      = callbackPrefix + convActFmtPNG
	cbMenuCancel  = callbackPrefix + convActMenuCancel
	cbPDFActions  = callbackPrefix + convActPDFOps

	// PDF operations on a PDF sent to /convert (bound to the session).
	cbSessRotate = callbackPrefix + convActPDFOp + ":" + opRotate
	cbSessKeep   = callbackPrefix + convActPDFOp + ":" + opKeep
	cbSessDrop   = callbackPrefix + convActPDFOp + ":" + opDrop
	cbSessMerge  = callbackPrefix + convActPDFOp + ":" + opMerge
)

// Actions of the /convert buttons ("conv:<action>[:...]").
const (
	convActDoc        = "doc"  // conv:doc:<docID> opens the menu
	convActQuick      = "q"    // conv:q:<docID>:<op> converts a document
	convActSave       = "save" // conv:save:<token> saves the result
	convActSkip       = "skip" // conv:skip:<token> keeps only the file
	convActRepAsk     = "rp"   // conv:rp:<token> asks to replace the original
	convActRepYes     = "rpy"  // conv:rpy:<token> confirms the replacement
	convActRepNo      = "rpn"  // conv:rpn:<token> cancels it
	convActAlbum      = "alb"  // conv:alb:<token> merges an album into a PDF
	convActMenuCancel = "x"    // closes the convert menu
	convActPDFOp      = "p"    // conv:p:<op> is a PDF operation of the session

	// Buttons of a /convert session, valid on the dialogue message at their step.
	convActPDF         = "pdf"
	convActPDFOps      = "pdfops"
	convActCompress    = "compress"
	convActDone        = "done"
	convActCancel      = "cancel"
	convActLimit1      = "limit1"
	convActLimit2      = "limit2"
	convActLimit5      = "limit5"
	convActLimitCustom = "limitcustom"
	convActFmtJPEG     = "fmtjpeg"
	convActFmtPNG      = "fmtpng"
)

// callbackData is the parsed data of an inline button: "conv:<action>" plus, for
// the actions that carry one, ":<docID>", plus ":<arg>" for each argument.
// Telegram limits the data to 64 bytes, so it holds ids and short tokens only.
type callbackData struct {
	action string
	docID  docsbot.DocumentID
	args   []string
}

// carriesDocID reports whether the third segment of the action's data is a
// document id; for every other action it is the first argument.
func carriesDocID(action string) bool {
	return action == convActDoc || action == convActQuick
}

// parseCallback splits button data of the converter; false when it is not its own.
// The parts are not validated: a handler refuses what it cannot use.
func parseCallback(data string) (callbackData, bool) {
	if !strings.HasPrefix(data, callbackPrefix) {
		return callbackData{}, false
	}
	segs := strings.Split(strings.TrimPrefix(data, callbackPrefix), ":")
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
// segment after the action is an empty id ("conv:doc:") comes back without it.
func (c callbackData) String() string {
	var sb strings.Builder
	sb.WriteString(callbackPrefix)
	sb.WriteString(c.action)
	if c.docID != "" || (len(c.args) > 0 && carriesDocID(c.action)) {
		sb.WriteString(":" + string(c.docID))
	}
	for _, a := range c.args {
		sb.WriteString(":" + a)
	}
	return sb.String()
}

// token is the data after the action, "" when it has none.
func (c callbackData) token() string {
	return strings.Join(c.args, ":")
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

// userID is the internal id of the sender, put in ctx by the auth middleware.
// The docs adapter has the same helper; a copy, because the converter cannot
// import it.
func userID(ctx context.Context) (docsbot.UserID, bool) {
	id, ok := tg.UserIDFromContext(ctx)
	return docsbot.UserID(id), ok
}

// convCB builds the data of a /convert button.
func convCB(action string, args ...string) string {
	return callbackData{action: action, args: args}.String()
}

// convDocCB is convCB for the buttons that carry a document id.
func convDocCB(action string, id docsbot.DocumentID, args ...string) string {
	return callbackData{action: action, docID: id, args: args}.String()
}

// MenuCB is the data of the button that opens the convert menu of a stored
// document. The docs adapter puts it on its cards and pickers.
func MenuCB(id docsbot.DocumentID) string { return convDocCB(convActDoc, id) }

// AlbumCB is the data of the "one PDF" button of an album; token comes from
// RegisterAlbum.
func AlbumCB(token string) string { return convCB(convActAlbum, token) }
