package docsbot

import "context"

// EventName is the name of a product event.
type EventName string

// Product events. They only ever carry the enumerated props of EventProps.
const (
	EventStart             EventName = "start"
	EventDocSaved          EventName = "doc_saved"
	EventDocOpened         EventName = "doc_opened"
	EventSearch            EventName = "search"
	EventInlineSent        EventName = "inline_sent"
	EventDriveConnected    EventName = "drive_connected"
	EventDriveDisconnected EventName = "drive_disconnected"
	EventConvertDone       EventName = "convert_done"
	EventExport            EventName = "export"
	EventDataDeleted       EventName = "data_deleted"
)

// ConvertOp names a converter operation in a convert_done event. Only these
// values reach the event props.
type ConvertOp string

// Converter operations.
const (
	ConvertPhotoPDF  ConvertOp = "photo_pdf"  // photos or one stored photo -> PDF
	ConvertAlbumPDF  ConvertOp = "album_pdf"  // an album -> one PDF
	ConvertCompress  ConvertOp = "compress"   // image re-encoded to a size limit
	ConvertWebPJPG   ConvertOp = "webp_jpg"   // WEBP -> JPEG
	ConvertPDFRotate ConvertOp = "pdf_rotate" // PDF pages rotated
	ConvertPDFKeep   ConvertOp = "pdf_keep"   // only the chosen PDF pages kept
	ConvertPDFRemove ConvertOp = "pdf_remove" // the chosen PDF pages removed
	ConvertPDFMerge  ConvertOp = "pdf_merge"  // several PDFs merged
)

// EventProps are the details of an event. The fields are enumerated values; Source
// is the only text that comes from the user, and ParseSource validates it. Titles,
// captions, search text and file ids never belong here. Empty fields are omitted.
type EventProps struct {
	Source    string
	New       *bool // start: whether this is the user's first /start
	Category  Category
	Kind      Kind
	Operation ConvertOp
}

// Event is one product event of a user. An empty User is an anonymous event, kept
// without a link to anyone (data_deleted).
type Event struct {
	User  UserID
	Name  EventName
	Props EventProps
}

const maxSourceLen = 32

// ParseSource reports whether payload is a valid acquisition source: 1 to 32
// characters of a-z, 0-9 and "_". Anything else is not stored, so a crafted
// deep link cannot put free text into the events.
func ParseSource(payload string) (string, bool) {
	if payload == "" || len(payload) > maxSourceLen {
		return "", false
	}
	for _, r := range payload {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return "", false
		}
	}
	return payload, true
}

// Track records a product event for the adapters whose scenario only they can
// observe (search, inline, converter). A failed write is logged at Warn and
// dropped: an event never breaks or changes the scenario it describes.
func (s *Service) Track(ctx context.Context, user UserID, name EventName, p EventProps) {
	s.track(ctx, Event{User: user, Name: name, Props: p})
}

func (s *Service) track(ctx context.Context, e Event) {
	if s.events == nil {
		return
	}
	if err := s.events.Record(ctx, e); err != nil {
		// Only the name is logged: props may carry the acquisition source.
		s.log.WarnContext(ctx, "record event", "event", e.Name, "user_id", e.User, "err", err)
	}
}
