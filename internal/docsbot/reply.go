package docsbot

// Reply is the meaning of a use case's answer; the channel adapter renders it
// into messages and buttons. Only the branching use cases return this interface;
// single-outcome ones return their concrete type.
type Reply interface{ isReply() }

// NeedCategory asks the user to choose a category for a freshly received file.
type NeedCategory struct{ Doc Document }

// AlreadySaved means the same file was saved before (deduplicated).
type AlreadySaved struct{ Doc Document }

// Saved means the document is stored. OfferDrive nudges the user to connect
// Drive (on the 1st and 5th document, while Drive is not connected).
type Saved struct {
	Doc        Document
	OfferDrive bool
}

// TooManyPending means the user already has too many files awaiting a category;
// the new file was not stored.
type TooManyPending struct{}

// RateLimited means the user sent too many files within a minute; the file was not stored.
type RateLimited struct{}

// QuotaExceeded means the user reached the document limit; the file was not stored.
type QuotaExceeded struct{}

func (NeedCategory) isReply()   {}
func (AlreadySaved) isReply()   {}
func (Saved) isReply()          {}
func (TooManyPending) isReply() {}
func (RateLimited) isReply()    {}
func (QuotaExceeded) isReply()  {}

// SavedGroup is the outcome of saving a whole album: every document with its
// own Saved result. OfferDrive is true if any of them asked for the Drive nudge.
type SavedGroup struct {
	Docs       []Document
	OfferDrive bool
}

// Card is one document's detail view.
type Card struct {
	Doc Document
	// CategoryFolderID is the Drive folder id of the document's category; empty
	// while the folder has not been created.
	CategoryFolderID string
	// FileInOldFolder is set right after a backed-up document changed category:
	// the file stays in its previous Drive folder, so the card links to the file.
	FileInOldFolder bool
}

// Catalog lists categories with their saved-document counts. NotInDrive and
// DriveConnected drive the 📥 banner, shown only once Drive is connected.
type Catalog struct {
	Counts         []CategoryCount
	NotInDrive     int
	DriveConnected bool
}

// CategoryPage is one page of a category's documents.
type CategoryPage struct {
	Category       Category
	Docs           []Document
	Page           int // 1-based
	Pages          int
	Total          int
	DriveConnected bool
	FolderID       string // Drive folder of the category; empty if not created yet
}

// Found holds search results for a query. BlankQuery is set when the query was
// empty after trimming: nothing was searched and the channel shows a hint.
type Found struct {
	Query      string
	Docs       []Document
	BlankQuery bool
}

// SettingsView is the /settings screen: Drive state and document counts.
// OnlyTelegram is saved documents neither backed up nor failed.
type SettingsView struct {
	DriveConnected bool
	Total          int
	InDrive        int
	OnlyTelegram   int
	Failed         int
}

// Export is the set of documents to resend from Telegram. Truncated is true when
// the user has more saved documents than one export may send.
type Export struct {
	Docs      []Document
	Total     int
	Truncated bool
}

// StatusView is what the channel needs to render/refresh a batch status message.
// Grouped is false when the document is not part of a status message (then the
// adapter falls back to a single per-document message).
type StatusView struct {
	Doc         Document
	Grouped     bool
	Message     StatusMessage
	Lines       []Document
	FolderByCat map[Category]string
	AllTerminal bool
}
