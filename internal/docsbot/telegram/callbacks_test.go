package telegram

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

func TestBackFromOrig(t *testing.T) {
	require.Equal(t, "d:lc:auto:2", backFromOrig("l:auto:2"))
	require.Equal(t, "d:sb", backFromOrig("s"))
	require.Empty(t, backFromOrig(""))
}

const uuidDoc = docsbot.DocumentID("123e4567-e89b-12d3-a456-426614174000")

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
		return callbackData{action: action, docID: id, args: args}
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
		{"dca", "d:dca", d(actDisconnectAsk, "")},
		{"dc", "d:dc", d(actDisconnect, "")},
		{"dcn", "d:dcn", d(actDisconnectNo, "")},
		{"cna", "d:cna", d(actConnectAsk, "")},
		{"cnc", "d:cnc", d(actConnectNo, "")},
		{"cb", "d:cb:" + string(uuidDoc), d(actCardBack, uuidDoc)},
		{"rta", "d:rta", d(actRetryAll, "")},
		{"exp", "d:exp", d(actExport, "")},
		{"fb settings", "d:fb:s", d(actFeedback, "", "s")},
		{"fb error", "d:fb:e:load_card:1795000000", d(actFeedback, "", "e", "load_card", "1795000000")},
		{"fbx", "d:fbx", d(actFeedbackCancel, "")},
		{"dd", "d:dd", d(actDeleteData, "")},
		{"ddn", "d:ddn", d(actDeleteDataNo, "")},
		{"sb", "d:sb", d(actSearchBack, "")},
		{"noop", "d:noop", d(actNoop, "")},
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
}

func TestCallbackData_worstCaseFitsTheLimit(t *testing.T) {
	slug := longestSlug()
	worst := []string{
		docCB(actOpenCard, uuidDoc, "l", slug, "999"),
		docCB(actSetCategory, uuidDoc, slug),
		docCB(actListCat, "", slug, "999"),
		docCB(actRetryBatch, "", string(uuidDoc)),
	}
	for _, c := range docsbot.AllCategories {
		worst = append(worst,
			docCB(actCategory, uuidDoc, string(c)),
			docCB(actCategoryGroup, uuidDoc, string(c)),
			docCB(actSetCategory, uuidDoc, string(c)),
			docCB(actListCat, "", string(c), "999"),
			docCB(actOpenCard, uuidDoc, "l", string(c), "999"),
		)
	}
	for _, data := range worst {
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
		{"converter prefix is not ours", "conv:save:x", false, callbackData{}, ""},
		{"prefix without colon", "d", false, callbackData{}, ""},
		{"bare prefix", "d:", true, callbackData{}, ""},
		{"doc action without id", "d:oc:", true, callbackData{action: actOpenCard}, "d:oc"},
		{"doc action without segment", "d:oc", true, callbackData{action: actOpenCard}, ""},
		{"extra colons stay arguments", "d:lc:a:1:2", true, callbackData{action: actListCat, args: []string{"a", "1", "2"}}, ""},
		{"unknown action", "d:zzz:1", true, callbackData{action: "zzz", args: []string{"1"}}, ""},
		{"prefix is case sensitive", "D:cat:1", false, callbackData{}, ""},
		{"leading space", " d:lb", false, callbackData{}, ""},
		{"prefix only inside", "x" + "d:lb", false, callbackData{}, ""},
		{"category without slug", "d:cat:id", true, callbackData{action: actCategory, docID: "id"}, ""},
		{"category with empty slug", "d:cat:id:", true, callbackData{action: actCategory, docID: "id", args: []string{""}}, ""},
		{"doc id never swallows a list argument", "d:lc:auto:2", true, callbackData{action: actListCat, args: []string{"auto", "2"}}, ""},
		{"retry batch keeps its id as argument", "d:rsm:sm", true, callbackData{action: actRetryBatch, args: []string{"sm"}}, ""},
		{"open card from search with extras", "d:oc:id:s:x", true, callbackData{action: actOpenCard, docID: "id", args: []string{"s", "x"}}, ""},
		{"unicode argument", "d:lc:паспорт:1", true, callbackData{action: actListCat, args: []string{"паспорт", "1"}}, ""},
		{"empty id with arguments keeps the arguments in place", "d:cat::auto", true,
			callbackData{action: actCategory, args: []string{"auto"}}, ""},
		{"open card without id from search", "d:oc::s", true, callbackData{action: actOpenCard, args: []string{"s"}}, ""},
		{"a huge number of arguments", cbPrefix + actListCat + strings.Repeat(":a", 50_000), true,
			callbackData{action: actListCat, args: slices.Repeat([]string{"a"}, 50_000)}, ""},
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
		ID: "q", From: models.User{ID: 42}, Data: "d:oc:doc:s",
		Message: models.MaybeInaccessibleMessage{Message: msg},
	})
	require.Equal(t, press{
		queryID: "q", tgUser: 42, user: "u1", hasUser: true, chatID: 100, msgID: 7, hasMsg: true, msg: msg,
		data: callbackData{action: actOpenCard, docID: "doc", args: []string{"s"}},
	}, p)

	p = newPress(t.Context(), &models.CallbackQuery{ID: "q", From: models.User{ID: 42}, Data: "d:lb"})
	require.False(t, p.hasMsg, "a message Telegram no longer gives")
	require.False(t, p.hasUser, "no user from the auth middleware")
	require.Zero(t, p.chatID)
	require.Equal(t, actListBack, p.data.action)
}

func TestParseCallback_wellFormedDataRoundTrips(t *testing.T) {
	// Every shape of up to four non-empty segments after the prefix, with an
	// action that carries a document id and one that does not.
	segments := []string{"oc", "lc", "q", "doc", "save", "zzz", "1", "a"}
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
		data := cbPrefix + strings.Join(segs, ":")
		got, ok := parseCallback(data)
		require.True(t, ok, data)
		require.Equal(t, segs[0], got.action, data)
		require.Equal(t, data, got.String(), "String inverts parse")
	}
}

func TestParseCallback_positionsMatchTheSplitHandlersUsedToRead(t *testing.T) {
	// The handlers used to index strings.Split(data, ":"): parts[1] the action,
	// parts[2] the document id or the first argument, parts[3:] the rest.
	var corpus []string
	for _, tt := range callbackCases() {
		corpus = append(corpus, tt.data)
	}
	corpus = append(corpus, "d:", "d:cat:", "d:cat:d1:other", "d:cat:missing:", "d:sc:1:other", "d:dy:", "d:rt", "d:inl",
		"d:inl:x", "d:lc:a:1:2", "d:oc:x1:l:auto:2", "d:oc:missing:s", "d:zzz:1", "d:rsm:", "d:rn")

	for _, data := range corpus {
		if !strings.HasPrefix(data, cbPrefix) {
			continue
		}
		t.Run(data, func(t *testing.T) {
			parts := strings.Split(data, ":")
			got, ok := parseCallback(data)
			require.True(t, ok)
			require.Equal(t, parts[1], got.action)

			rest := parts[2:]
			if carriesDocID(got.action) && len(rest) > 0 {
				require.Equal(t, docsbot.DocumentID(rest[0]), got.docID)
				rest = rest[1:]
			}
			if len(rest) == 0 {
				require.Empty(t, got.args)
				return
			}
			require.Equal(t, rest, got.args)
		})
	}
}

func TestCallbackData_categoryReadsTheSlugAfterTheDocID(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		want   docsbot.Category
		wantOK bool
	}{
		{"every category is readable", "d:cat:id:" + string(docsbot.CatPassport), docsbot.CatPassport, true},
		{"slug missing", "d:cat:id", "", false},
		{"slug empty", "d:cat:id:", "", false},
		{"slug unknown", "d:cat:id:zzz", "", false},
		{"slug in the document id position", "d:cat:" + string(docsbot.CatPassport), "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cd, ok := parseCallback(tt.data)
			require.True(t, ok)

			got, gotOK := cd.category()

			require.Equal(t, tt.wantOK, gotOK)
			if gotOK {
				require.Equal(t, tt.want, got)
			}
		})
	}
	for _, c := range docsbot.AllCategories {
		cd, _ := parseCallback(docCB(actSetCategory, uuidDoc, string(c)))
		got, ok := cd.category()
		require.True(t, ok, c)
		require.Equal(t, c, got)
	}
}

func TestBackFromOrig_roundTripsThroughTheCardButtons(t *testing.T) {
	tests := []struct {
		orig       string
		wantAction string
		wantArgs   []string
	}{
		{"l:" + longestSlug() + ":999", actListCat, []string{longestSlug(), "999"}},
		{"s", actSearchBack, nil},
	}
	for _, tt := range tests {
		t.Run(tt.orig, func(t *testing.T) {
			cd, ok := parseCallback(backFromOrig(tt.orig))
			require.True(t, ok)
			require.Equal(t, tt.wantAction, cd.action)
			require.Equal(t, tt.wantArgs, cd.args)
		})
	}
	// An origin that is neither a list nor the search gets no back button, and
	// the one an open-card button carries is what the card keeps.
	require.Empty(t, backFromOrig("x"))
	require.Empty(t, backFromOrig("l"))
	open, _ := parseCallback(docCB(actOpenCard, uuidDoc, "l", longestSlug(), "999"))
	require.Equal(t, "l:"+longestSlug()+":999", strings.Join(open.args, ":"))
}
