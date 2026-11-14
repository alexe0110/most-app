package telegram

import (
	"time"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// TTLs of the in-memory stores. All of them are memory only: a restart empties
// them, and each user of a store tolerates that (see its comment).
const (
	renameTTL   = 5 * time.Minute
	originTTL   = 30 * time.Minute
	searchTTL   = 10 * time.Minute
	confirmTTL  = 2 * time.Minute
	settingsTTL = 24 * time.Hour // the user may take a while on Google's consent screen
)

// renameEntry is a rename the user armed: the document, and the card message that
// shows the prompt and then the renamed card. A stale arm (older than renameTTL)
// is ignored.
type renameEntry struct {
	id   docsbot.DocumentID
	chat int64
	msg  int
}

// originKey is a card message; its origin is where the card was opened from (a
// list page "l:<cat>:<page>", the search "s", or nowhere ""), so edits of the card
// keep its "Назад". Losing it after originTTL only loses the way back.
type originKey struct {
	chat int64
	msg  int
}

// dropRenamesOn forgets every rename armed on that message, whoever armed it. A
// message that is redrawn (a status message) no longer shows the prompt.
func dropRenamesOn(m *tg.TTLMap[int64, renameEntry], chat int64, msg int) {
	m.DeleteFunc(func(_ int64, e renameEntry) bool { return e.chat == chat && e.msg == msg })
}

// dropRename forgets the user's rename if it is armed on that message; one armed
// on another message (a newer prompt) is left alone.
func dropRename(m *tg.TTLMap[int64, renameEntry], user, chat int64, msg int) {
	m.DeleteFunc(func(u int64, e renameEntry) bool { return u == user && e.chat == chat && e.msg == msg })
}

// forgetSettings drops the chat's settings message if it is msg: that message
// stopped being the settings screen. A newer settings message is left alone.
func forgetSettings(m *tg.TTLMap[int64, int], chat int64, msg int) {
	m.DeleteFunc(func(c int64, id int) bool { return c == chat && id == msg })
}

// Renames is the rename state shared by the docs handlers and the Notifier: the
// worker redraws status messages, and a redraw ends a rename prompt shown on one.
type Renames struct {
	m *tg.TTLMap[int64, renameEntry]
}

// NewRenames makes the shared rename state; pass it to Register (WithRenames) and
// to NewNotifier (WithNotifierRenames).
func NewRenames() *Renames {
	return &Renames{m: tg.NewTTLMap[int64, renameEntry](renameTTL, tg.MaxStoreEntries, time.Now)}
}

// SettingsMessages is the settings-message memory shared by the docs handlers
// (which record the message) and NotifyConnected (which edits it).
type SettingsMessages struct{ m *tg.TTLMap[int64, int] }

// NewSettingsMessages makes the shared memory; pass it to Register
// (WithSettingsMessages) and to NotifyConnected (WithConnectSettings).
func NewSettingsMessages() *SettingsMessages {
	return &SettingsMessages{m: tg.NewTTLMap[int64, int](settingsTTL, tg.MaxStoreEntries, time.Now)}
}

// WithSettingsMessages makes the docs handlers record their /settings messages
// in m.
func WithSettingsMessages(m *SettingsMessages) Option {
	return func(d *docs) { d.settingsMsgs = m.m }
}

// origin is where the card message was opened from; "" when unknown or expired.
func (d *docs) origin(chat int64, msg int) string {
	orig, _ := d.origins.Get(originKey{chat, msg})
	return orig
}
