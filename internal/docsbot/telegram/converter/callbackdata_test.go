package converter

import (
	"cmp"
	"slices"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

const (
	uuidDoc  = docsbot.DocumentID("123e4567-e89b-12d3-a456-426614174000")
	hexToken = "0123456789abcdef"
)

// callbackCases is every button the converter builds, with its wire format.
func callbackCases() []struct {
	name string
	data string
	want callbackData
} {
	conv := func(action string, id docsbot.DocumentID, args ...string) callbackData {
		return callbackData{action: action, docID: id, args: args}
	}
	return []struct {
		name string
		data string
		want callbackData
	}{
		{"conv doc", "conv:doc:" + string(uuidDoc), conv(convActDoc, uuidDoc)},
		{"conv quick", "conv:q:" + string(uuidDoc) + ":" + opCompress1, conv(convActQuick, uuidDoc, opCompress1)},
		{"conv quick c2", "conv:q:" + string(uuidDoc) + ":" + opCompress2, conv(convActQuick, uuidDoc, opCompress2)},
		{"conv quick c5", "conv:q:" + string(uuidDoc) + ":" + opCompress5, conv(convActQuick, uuidDoc, opCompress5)},
		{"conv quick pdf", "conv:q:" + string(uuidDoc) + ":" + opPDF, conv(convActQuick, uuidDoc, opPDF)},
		{"conv quick jpg", "conv:q:" + string(uuidDoc) + ":" + opJPG, conv(convActQuick, uuidDoc, opJPG)},
		{"conv quick rotate", "conv:q:" + string(uuidDoc) + ":" + opRotate, conv(convActQuick, uuidDoc, opRotate)},
		{"conv quick keep", "conv:q:" + string(uuidDoc) + ":" + opKeep, conv(convActQuick, uuidDoc, opKeep)},
		{"conv quick drop", "conv:q:" + string(uuidDoc) + ":" + opDrop, conv(convActQuick, uuidDoc, opDrop)},
		{"conv quick merge", "conv:q:" + string(uuidDoc) + ":" + opMerge, conv(convActQuick, uuidDoc, opMerge)},
		{"conv info", "conv:i:" + string(uuidDoc), conv(convActInfo, uuidDoc)},
		{"conv clean", "conv:cl:" + string(uuidDoc), conv(convActClean, uuidDoc)},
		{"conv keep clean", "conv:kc:" + hexToken, conv(convActKeepClean, "", hexToken)},
		{"conv keep original", "conv:ko:" + hexToken, conv(convActKeepOrig, "", hexToken)},
		{"conv save", "conv:sv:" + hexToken, conv(convActSave, "", hexToken)},
		{"conv legacy save", "conv:save:" + hexToken, conv(convActSaveLegacy, "", hexToken)},
		{"conv skip", "conv:skip:" + hexToken, conv(convActSkip, "", hexToken)},
		{"conv rp", "conv:rp:" + hexToken, conv(convActRepAsk, "", hexToken)},
		{"conv rpy", "conv:rpy:" + hexToken, conv(convActRepYes, "", hexToken)},
		{"conv rpn", "conv:rpn:" + hexToken, conv(convActRepNo, "", hexToken)},
		{"conv alb", "conv:alb:" + hexToken, conv(convActAlbum, "", hexToken)},
		{"conv x", "conv:x", conv(convActMenuCancel, "")},
		{"conv session op", "conv:p:" + opRotate, conv(convActPDFOp, "", opRotate)},
		{"conv session keep", "conv:p:" + opKeep, conv(convActPDFOp, "", opKeep)},
		{"conv session drop", "conv:p:" + opDrop, conv(convActPDFOp, "", opDrop)},
		{"conv session merge", "conv:p:" + opMerge, conv(convActPDFOp, "", opMerge)},
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
	require.Equal(t, "conv:sv:"+hexToken, convCB(convActSave, hexToken))
	require.Equal(t, "conv:q:"+string(uuidDoc)+":c1", convDocCB(convActQuick, uuidDoc, opCompress1))
	require.Equal(t, cbMenuCancel, convCB(convActMenuCancel))
	require.Equal(t, cbSessRotate, convCB(convActPDFOp, opRotate))
	require.Equal(t, "conv:doc:"+string(uuidDoc), MenuCB(uuidDoc), "the docs adapter puts it on its cards")
	require.Equal(t, "conv:alb:"+hexToken, AlbumCB(hexToken))
}

func TestCallbackData_worstCaseFitsTheLimit(t *testing.T) {
	for _, data := range []string{
		convDocCB(convActQuick, uuidDoc, opCompress5),
		convCB(convActRepYes, hexToken),
	} {
		require.LessOrEqual(t, len(data), 64, data)
		cd, ok := parseCallback(data)
		require.True(t, ok)
		require.Equal(t, data, cd.String())
	}
}

func TestParseCallback_edges(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		wantOK bool
		want   callbackData
		str    string // String() when it differs from data: an empty id with nothing after it is dropped
	}{
		{"empty", "", false, callbackData{}, ""},
		{"foreign prefix", "x:cat:1", false, callbackData{}, ""},
		{"docs prefix is not ours", "d:lb", false, callbackData{}, ""},
		{"prefix without colon", "conv", false, callbackData{}, ""},
		{"prefix is case sensitive", "Conv:pdf", false, callbackData{}, ""},
		{"bare prefix", "conv:", true, callbackData{}, ""},
		{"unknown action", "conv:zzz", true, callbackData{action: "zzz"}, ""},
		{"quick without op", "conv:q:abc", true, callbackData{action: convActQuick, docID: "abc"}, ""},
		{"quick without id", "conv:q::c1", true, callbackData{action: convActQuick, args: []string{"c1"}}, ""},
		{"quick with empty op", "conv:q:abc:", true, callbackData{action: convActQuick, docID: "abc", args: []string{""}}, ""},
		{"quick with unknown op", "conv:q:abc:zzz", true, callbackData{action: convActQuick, docID: "abc", args: []string{"zzz"}}, ""},
		{"token empty", "conv:save:", true, callbackData{action: convActSaveLegacy, args: []string{""}}, ""},
		{"session op empty", "conv:p:", true, callbackData{action: convActPDFOp, args: []string{""}}, ""},
		{"menu without id", "conv:doc:", true, callbackData{action: convActDoc}, "conv:doc"},
		{"extra colons stay arguments", "conv:save:a:1:2", true, callbackData{action: convActSaveLegacy, args: []string{"a", "1", "2"}}, ""},
		{"a huge number of arguments", callbackPrefix + convActSaveLegacy + strings.Repeat(":a", 50_000), true,
			callbackData{action: convActSaveLegacy, args: slices.Repeat([]string{"a"}, 50_000)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCallback(tt.data)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
			if ok {
				require.Equal(t, cmp.Or(tt.str, tt.data), got.String())
			}
		})
	}
	require.NotPanics(t, func() { _ = callbackData{}.String() })
}

func TestNewPress(t *testing.T) {
	ctx := tg.ContextWithUserID(t.Context(), "u1")
	msg := &models.Message{ID: 7, Chat: models.Chat{ID: 100}}

	p := newPress(ctx, &models.CallbackQuery{
		ID: "q", From: models.User{ID: 42}, Data: "conv:sv:" + hexToken,
		Message: models.MaybeInaccessibleMessage{Message: msg},
	})
	require.Equal(t, press{
		queryID: "q", tgUser: 42, user: "u1", hasUser: true, chatID: 100, msgID: 7, hasMsg: true, msg: msg,
		data: callbackData{action: convActSave, args: []string{hexToken}},
	}, p)

	p = newPress(t.Context(), &models.CallbackQuery{ID: "q", From: models.User{ID: 42}, Data: "conv:x"})
	require.False(t, p.hasMsg, "a message Telegram no longer gives")
	require.False(t, p.hasUser, "no user from the auth middleware")
	require.Zero(t, p.chatID)
	require.Equal(t, convActMenuCancel, p.data.action)
}

func TestParseCallback_wellFormedDataRoundTrips(t *testing.T) {
	// Every shape of up to four non-empty segments after the prefix, with an
	// action that carries a document id and one that does not.
	segments := []string{"q", "doc", "save", "p", "zzz", "1", "a"}
	var shapes [][]string
	var build func(prefix []string)
	build = func(prefix []string) {
		shapes = append(shapes, prefix)
		if len(prefix) == 4 {
			return
		}
		for _, s := range segments {
			build(append(append([]string(nil), prefix...), s))
		}
	}
	for _, s := range segments {
		build([]string{s})
	}

	for _, segs := range shapes {
		data := callbackPrefix + strings.Join(segs, ":")
		got, ok := parseCallback(data)
		require.True(t, ok, data)
		require.Equal(t, segs[0], got.action, data)
		require.Equal(t, data, got.String(), "String inverts parse")
	}
}

func TestCallbackData_tokenIsEverythingAfterTheAction(t *testing.T) {
	tests := []struct {
		data string
		want string
	}{
		{"conv:save:" + hexToken, hexToken},
		{"conv:save:", ""},
		{"conv:x", ""},
		{"conv:p:rot", "rot"},
		{"conv:save:a:b", "a:b"},
	}
	for _, tt := range tests {
		t.Run(tt.data, func(t *testing.T) {
			cd, _ := parseCallback(tt.data)
			require.Equal(t, tt.want, cd.token())
		})
	}
}

// builtButtons is the callback data of every inline keyboard the converter renders.
func builtButtons() []string {
	token := tg.NewToken()
	var data []string
	for _, kb := range []*models.InlineKeyboardMarkup{
		actionKeyboard(), pdfKeyboard(), limitKeyboard(), formatKeyboard(), cancelKeyboard(),
		quickKeyboard(uuidDoc, true), quickKeyboard(uuidDoc, false), pdfMenuKeyboard(uuidDoc), pdfActionsKeyboard(),
		resultKeyboard(token, resultPDF, true), resultKeyboard(token, resultPDF, false),
		resultKeyboard(token, resultCompressed, true), resultKeyboard(token, resultCompressed, false),
		resultKeyboard(token, resultJPG, false), replaceKeyboard(token),
		infoKeyboard(uuidDoc, true), infoKeyboard(uuidDoc, false), cleanedKeyboard(token),
	} {
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				data = append(data, b.CallbackData)
			}
		}
	}
	return append(data, MenuCB(uuidDoc), AlbumCB(token), infoCB(uuidDoc))
}

func TestBuiltButtons_everyOneParsesFitsAndIsKnown(t *testing.T) {
	known := map[string]bool{}
	for _, tt := range callbackCases() {
		known[tt.want.action] = true
	}

	data := builtButtons()
	require.NotEmpty(t, data)
	for _, d := range data {
		cd, ok := parseCallback(d)
		require.True(t, ok, d)
		require.True(t, known[cd.action], "%s: an action with no round-trip case", d)
		require.Equal(t, d, cd.String(), "String inverts parse")
		require.LessOrEqual(t, len(d), 64, d)
	}
}
