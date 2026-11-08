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

func (NeedCategory) isReply() {}
func (AlreadySaved) isReply() {}
func (Saved) isReply()        {}

// Card is one document's detail view.
type Card struct{ Doc Document }

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
}

// Found holds search results for a query.
type Found struct {
	Query string
	Docs  []Document
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
