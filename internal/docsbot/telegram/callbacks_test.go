package telegram

import (
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestBackFromOrig(t *testing.T) {
	require.Equal(t, "d:lc:auto:2", backFromOrig("l:auto:2"))
	require.Equal(t, "d:sb", backFromOrig("s"))
	require.Empty(t, backFromOrig(""))
}

const (
	uuidDoc  = docsbot.DocumentID("123e4567-e89b-12d3-a456-426614174000")
	hexToken = "0123456789abcdef"
)

// longestSlug is the longest category slug: the worst case for the 64-byte limit.
func longestSlug() string {
	var longest string
	for _, c := range docsbot.AllCategories {
		if len(c) > len(longest) {
			longest = string(c)
		}
	}
	return longest
}

// callbackCases is every button the bot builds, with its wire format.
func callbackCases() []struct {
	name string
	data string
	want callbackData
} {
	slug := longestSlug()
	d := func(action string, id docsbot.DocumentID, args ...string) callbackData {
		return callbackData{prefix: cbPrefix, action: action, docID: id, args: args}
	}
	conv := func(action string, id docsbot.DocumentID, args ...string) callbackData {
		return callbackData{prefix: callbackPrefix, action: action, docID: id, args: args}
	}
	return []struct {
		name string
		data string
		want callbackData
	}{
		{"cat", "d:cat:" + string(uuidDoc) + ":" + slug, d(actCategory, uuidDoc, slug)},
		{"ga", "d:ga:" + string(uuidDoc) + ":" + slug, d(actCategoryGroup, uuidDoc, slug)},
		{"sc", "d:sc:" + string(uuidDoc) + ":" + slug, d(actSetCategory, uuidDoc, slug)},
		{"gpk", "d:gpk:" + string(uuidDoc), d(actGroupPick, uuidDoc)},
		{"chc", "d:chc:" + string(uuidDoc), d(actChangeCat, uuidDoc)},
		{"snd", "d:snd:" + string(uuidDoc), d(actSend, uuidDoc)},
		{"rn", "d:rn:" + string(uuidDoc), d(actRename, uuidDoc)},
		{"rnc", "d:rnc:" + string(uuidDoc), d(actRenameCancel, uuidDoc)},
		{"del", "d:del:" + string(uuidDoc), d(actDeleteAsk, uuidDoc)},
		{"dy", "d:dy:" + string(uuidDoc), d(actDeleteYes, uuidDoc)},
		{"dn", "d:dn:" + string(uuidDoc), d(actDeleteNo, uuidDoc)},
		{"rt", "d:rt:" + string(uuidDoc), d(actRetry, uuidDoc)},
		{"rs", "d:rs:" + string(uuidDoc), d(actRetrySync, uuidDoc)},
		{"oc from list", "d:oc:" + string(uuidDoc) + ":l:" + slug + ":999", d(actOpenCard, uuidDoc, "l", slug, "999")},
		{"oc from search", "d:oc:" + string(uuidDoc) + ":s", d(actOpenCard, uuidDoc, "s")},
		{"lc", "d:lc:" + slug + ":999", d(actListCat, "", slug, "999")},
		{"inl on", "d:inl:1", d(actInline, "", "1")},
		{"inl off", "d:inl:0", d(actInline, "", "0")},
		{"rsm", "d:rsm:" + string(uuidDoc), d(actRetryBatch, "", string(uuidDoc))},
		{"lb", "d:lb", d(actListBack, "")},
		{"tr", "d:tr", d(actTransfer, "")},
		{"try", "d:try", d(actTransferYes, "")},
		{"trn", "d:trn", d(actTransferNo, "")},
		{"dc", "d:dc", d(actDisconnect, "")},
		{"rta", "d:rta", d(actRetryAll, "")},
		{"exp", "d:exp", d(actExport, "")},
		{"dd", "d:dd", d(actDeleteData, "")},
		{"sb", "d:sb", d(actSearchBack, "")},
		{"noop", "d:noop", d(actNoop, "")},

		{"conv doc", "conv:doc:" + string(uuidDoc), conv(convActDoc, uuidDoc)},
		{"conv quick", "conv:q:" + string(uuidDoc) + ":" + opCompress1, conv(convActQuick, uuidDoc, opCompress1)},
		{"conv save", "conv:save:" + hexToken, conv(convActSave, "", hexToken)},
		{"conv skip", "conv:skip:" + hexToken, conv(convActSkip, "", hexToken)},
		{"conv rp", "conv:rp:" + hexToken, conv(convActRepAsk, "", hexToken)},
		{"conv rpy", "conv:rpy:" + hexToken, conv(convActRepYes, "", hexToken)},
		{"conv rpn", "conv:rpn:" + hexToken, conv(convActRepNo, "", hexToken)},
		{"conv alb", "conv:alb:" + hexToken, conv(convActAlbum, "", hexToken)},
		{"conv x", "conv:x", conv(convActMenuCancel, "")},
		{"conv session op", "conv:p:" + opRotate, conv(convActPDFOp, "", opRotate)},
		{"conv pdf", cbPDF, conv("pdf", "")},
		{"conv pdfops", cbPDFActions, conv("pdfops", "")},
		{"conv compress", cbCompress, conv("compress", "")},
		{"conv done", cbDone, conv("done", "")},
		{"conv cancel", cbCancel, conv("cancel", "")},
		{"conv limit1", cbLimit1, conv("limit1", "")},
		{"conv limit2", cbLimit2, conv("limit2", "")},
		{"conv limit5", cbLimit5, conv("limit5", "")},
		{"conv limitcustom", cbLimitCustom, conv("limitcustom", "")},
		{"conv fmtjpeg", cbFmtJPEG, conv("fmtjpeg", "")},
		{"conv fmtpng", cbFmtPNG, conv("fmtpng", "")},
	}
}

func TestParseCallback_roundTripsEveryButton(t *testing.T) {
	for _, tt := range callbackCases() {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCallback(tt.data)
			require.True(t, ok)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.data, got.String(), "String inverts parse")
			require.LessOrEqual(t, len(tt.data), 64, "Telegram limits callback data to 64 bytes")
		})
	}
}

func TestCallbackConstructors_matchWireFormat(t *testing.T) {
	slug := longestSlug()
	require.Equal(t, "d:snd:"+string(uuidDoc), docCB(actSend, uuidDoc))
	require.Equal(t, "d:lc:"+slug+":3", docCB(actListCat, "", slug, "3"))
	require.Equal(t, "d:lb", docCB(actListBack, ""))
	require.Equal(t, "d:oc:"+string(uuidDoc)+":s", docCB(actOpenCard, uuidDoc, "s"))
	require.Equal(t, "conv:save:"+hexToken, convCB(convActSave, hexToken))
	require.Equal(t, "conv:q:"+string(uuidDoc)+":c1", convDocCB(convActQuick, uuidDoc, opCompress1))
	require.Equal(t, cbMenuCancel, convCB(convActMenuCancel))
	require.Equal(t, cbSessRotate, convCB(convActPDFOp, opRotate))
}

func TestCallbackData_worstCaseFitsTheLimit(t *testing.T) {
	slug := longestSlug()
	for _, data := range []string{
		docCB(actOpenCard, uuidDoc, "l", slug, "999"),
		docCB(actSetCategory, uuidDoc, slug),
		docCB(actListCat, "", slug, "999"),
		docCB(actRetryBatch, "", string(uuidDoc)),
		convDocCB(convActQuick, uuidDoc, opCompress5),
		convCB(convActRepYes, hexToken),
	} {
		require.LessOrEqual(t, len(data), 64, data)
	}
}

func TestParseCallback_edges(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		wantOK bool
		want   callbackData
	}{
		{"empty", "", false, callbackData{}},
		{"foreign prefix", "x:cat:1", false, callbackData{}},
		{"prefix without colon", "d", false, callbackData{}},
		{"bare prefix", "d:", true, callbackData{prefix: cbPrefix}},
		{"bare conv prefix", "conv:", true, callbackData{prefix: callbackPrefix}},
		{"doc action without id", "d:oc:", true, callbackData{prefix: cbPrefix, action: actOpenCard}},
		{"doc action without segment", "d:oc", true, callbackData{prefix: cbPrefix, action: actOpenCard}},
		{"extra colons stay arguments", "d:lc:a:1:2", true, callbackData{prefix: cbPrefix, action: actListCat, args: []string{"a", "1", "2"}}},
		{"unknown action", "d:zzz:1", true, callbackData{prefix: cbPrefix, action: "zzz", args: []string{"1"}}},
		{"unknown conv action", "conv:zzz", true, callbackData{prefix: callbackPrefix, action: "zzz"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCallback(tt.data)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
	require.NotPanics(t, func() { _ = callbackData{}.String() })
}

func TestNewPress(t *testing.T) {
	ctx := tg.ContextWithUserID(t.Context(), "u1")
	msg := &models.Message{ID: 7, Chat: models.Chat{ID: 100}}

	p := newPress(ctx, &models.CallbackQuery{
		ID: "q", From: models.User{ID: 42}, Data: "d:oc:doc:s",
		Message: models.MaybeInaccessibleMessage{Message: msg},
	})
	require.Equal(t, press{
		queryID: "q", tgUser: 42, user: "u1", hasUser: true, chatID: 100, msgID: 7, hasMsg: true, msg: msg,
		data: callbackData{prefix: cbPrefix, action: actOpenCard, docID: "doc", args: []string{"s"}},
	}, p)

	p = newPress(t.Context(), &models.CallbackQuery{ID: "q", From: models.User{ID: 42}, Data: "d:lb"})
	require.False(t, p.hasMsg, "a message Telegram no longer gives")
	require.False(t, p.hasUser, "no user from the auth middleware")
	require.Zero(t, p.chatID)
	require.Equal(t, actListBack, p.data.action)
}
