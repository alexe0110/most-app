package docsbot

import (
	"log/slog"
	"sync"
	"time"
)

// BotName identifies this bot in the shared integrations/oauth tables.
const BotName = "docs"

const (
	// pageSize is how many documents one catalog page shows.
	pageSize = 10
	// searchLimit caps search results.
	searchLimit = 10
	// exportLimit caps how many documents one "send all" request resends.
	exportLimit = 50
	// statusWindow is how long an open status message keeps absorbing new documents.
	statusWindow = 30 * time.Second
	// sweepBatch caps how many stuck uploads one sweeper run handles.
	sweepBatch = 100
	// sweepStaleAfter is how long a document may sit in pending/uploading before
	// the sweeper requeues its upload.
	sweepStaleAfter = 30 * time.Minute
	// sweepFailAfter is how long a document may stay stuck before it is failed.
	sweepFailAfter = 24 * time.Hour
	// maxQueryRunes caps a search query's length.
	maxQueryRunes = 100
	// MaxDriveBytes is the largest file the bot uploads to Drive; a bigger one
	// stays in Telegram (backup_status too_large). It equals the Bot API download
	// limit tg.MaxFileBytes, which the domain cannot import (depguard).
	MaxDriveBytes = 20 << 20
	// maxPendingPerUser caps files awaiting a category per user.
	maxPendingPerUser = 20
	// maxFilesPerMinute caps how many files one user may send per minute.
	maxFilesPerMinute = 30
	// MaxDocumentsPerUser caps one user's total documents.
	MaxDocumentsPerUser = 5000
	// intakeWindow is the flood-check window.
	intakeWindow = time.Minute
	// pendingTTL is how long a document may wait for a category before it is purged.
	pendingTTL = 24 * time.Hour
	// remoteCheckTimeout bounds the Drive existence check made when a card opens.
	remoteCheckTimeout = 3 * time.Second
	// remoteCheckEvery is the minimum gap between existence checks of one document.
	remoteCheckEvery = time.Minute
)

// Service holds the Most Docs use cases. It depends only on ports, never on
// Telegram, Postgres or Drive SDKs.
type Service struct {
	repo    Repo
	drive   Drive
	files   FileSource
	uploads Uploads
	jobs    JobCanceler
	folders Folders
	status  StatusMessages
	syncs   Syncs
	events  Events // nil: product events are not recorded
	log     *slog.Logger
	now     func() time.Time
	loc     *time.Location

	// checks remembers when each document's Drive file was last looked up, to
	// throttle OpenCard; guarded by checksMu.
	checksMu sync.Mutex
	checks   map[DocumentID]time.Time

	pages PageCounter // nil: pages are not counted
}

// Option tunes a Service at construction.
type Option func(*Service)

// WithClock replaces the time source; used to pin the status-message window in tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithPageCounter sets how the pages of a PDF are counted when it is backed up.
// Without it the page count of such a document stays unknown.
func WithPageCounter(pc PageCounter) Option {
	return func(s *Service) { s.pages = pc }
}

// WithLogger sets the logger for failures the service absorbs, such as a product
// event that could not be written. The default discards them.
func WithLogger(log *slog.Logger) Option {
	return func(s *Service) { s.log = log }
}

// WithLocation sets the time zone used for the date in default titles.
func WithLocation(loc *time.Location) Option {
	return func(s *Service) { s.loc = loc }
}

// Ports are the external dependencies of a Service, named so that wiring cannot
// swap two of the same shape.
type Ports struct {
	Repo    Repo
	Drive   Drive
	Files   FileSource
	Uploads Uploads
	Jobs    JobCanceler
	Folders Folders
	Status  StatusMessages
	Syncs   Syncs
	Events  Events // optional
}

// NewService wires the service around its ports.
func NewService(p Ports, opts ...Option) *Service {
	s := &Service{
		repo: p.Repo, drive: p.Drive, files: p.Files, uploads: p.Uploads, jobs: p.Jobs,
		folders: p.Folders, status: p.Status, syncs: p.Syncs, events: p.Events,
		log: slog.New(slog.DiscardHandler),
		now: time.Now,
		loc: defaultLocation(),

		checks: make(map[DocumentID]time.Time),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func clamp(v, lo, hi int) int {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}

// defaultLocation is the zone for default-title dates: Asia/Almaty, or a fixed
// UTC+5 if the zone database is unavailable.
func defaultLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Almaty"); err == nil {
		return loc
	}
	return time.FixedZone("UTC+5", 5*60*60)
}
