package telegram

import (
	"time"
)

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
