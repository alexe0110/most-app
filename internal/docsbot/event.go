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
	EventFileInfo          EventName = "file_info"
	EventFileCleaned       EventName = "file_cleaned"
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

// FileFormat names the type of a file in a file_info or file_cleaned event. It
// is the type found from the content, never the name or the declared MIME type.
type FileFormat string

// File formats of the «О файле» screen.
const (
	FormatJPEG FileFormat = "jpeg"
	FormatPNG  FileFormat = "png"
	FormatWebP FileFormat = "webp"
	FormatHEIC FileFormat = "heic"
	FormatPDF  FileFormat = "pdf"
	FormatDOCX FileFormat = "docx"
	FormatXLSX FileFormat = "xlsx"
	FormatPPTX FileFormat = "pptx"
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
	Format    FileFormat
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
	if err := s.events.Record(ctx, e); err != nil {
		s.warnEvent(ctx, e.Name, e.User, err)
	}
}

// Start records a /start of the user. payload is the deep-link argument; it
// becomes the acquisition source only if ParseSource accepts it, and is stored
// as the user's source only on their first start. Like Track it never fails: a
// lost event must not change what /start answers.
func (s *Service) Start(ctx context.Context, user UserID, payload string) {
	source, _ := ParseSource(payload)
	if _, err := s.events.RecordStart(ctx, user, source); err != nil {
		s.warnEvent(ctx, EventStart, user, err)
	}
}

// warnEvent logs a lost event. Only the name is logged: props may carry the
// acquisition source.
func (s *Service) warnEvent(ctx context.Context, name EventName, user UserID, err error) {
	s.log.WarnContext(ctx, "record event", "event", name, "user_id", user, "err", err)
}
