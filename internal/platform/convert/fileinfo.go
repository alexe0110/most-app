package convert

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// FileType is the real kind of a file, found from its content and never from a
// name or a declared MIME type.
type FileType string

// File types Inspect and Strip support.
const (
	FileJPEG FileType = "jpeg"
	FilePNG  FileType = "png"
	FileWebP FileType = "webp"
	FileHEIC FileType = "heic"
	FilePDF  FileType = "pdf"
	FileDOCX FileType = "docx"
	FileXLSX FileType = "xlsx"
	FilePPTX FileType = "pptx"
)

// MetaBlock names a kind of metadata that is physically present in a file. It is
// coarser than FileInfo's fields: a block can exist without anything the screen
// can show (an IPTC record, a comment), and the file is clean only without any.
type MetaBlock string

// Metadata blocks reported in FileInfo.Blocks.
const (
	BlockEXIF        MetaBlock = "exif"
	BlockXMP         MetaBlock = "xmp"
	BlockIPTC        MetaBlock = "iptc"
	BlockText        MetaBlock = "text"
	BlockComment     MetaBlock = "comment"
	BlockPDFInfo     MetaBlock = "pdf_info"
	BlockOfficeProps MetaBlock = "office_props"
)

// Errors of reading and cleaning files; callers map them to user messages with
// errors.Is.
var (
	// ErrUnreadable means the file is broken, truncated or hostile: the parser
	// failed or panicked.
	ErrUnreadable = errors.New("convert: file is unreadable")
	// ErrEncrypted means the file is password-protected, so its metadata can
	// neither be read nor removed.
	ErrEncrypted = errors.New("convert: file is encrypted")
	// ErrFileTooLarge means the file exceeds MaxInputBytes.
	ErrFileTooLarge = errors.New("convert: file exceeds size limit")
)

// maxValueRunes caps one metadata string: the values come from a stranger's file
// and end up in a chat message.
const maxValueRunes = 256

// Capture is what the file says about the shot.
type Capture struct {
	Taken    time.Time
	Make     string
	Model    string
	Software string
}

// GeoPoint is a place in degrees; south and west are negative.
type GeoPoint struct {
	Lat, Lon float64
}

// People is who made or touched the file.
type People struct {
	Author         string
	Company        string
	LastModifiedBy string
}

// Dates are the creation and modification times written in the file.
type Dates struct {
	Created  time.Time
	Modified time.Time
}

// PDFFacts are the PDF specifics.
type PDFFacts struct {
	Pages     int
	Creator   string
	Producer  string
	Scanned   bool // no fonts: the pages are pictures
	Encrypted bool
}

// FileInfo is what Inspect found. Zero values mean "not in the file".
type FileInfo struct {
	Type     FileType
	Size     int
	Capture  Capture
	Location *GeoPoint
	People   People
	Dates    Dates
	PDF      *PDFFacts
	// Blocks lists the metadata physically in the file; empty means clean.
	Blocks []MetaBlock
}

// Clean reports that the file carries no metadata at all.
func (i FileInfo) Clean() bool { return len(i.Blocks) == 0 }

// Inspect reads the metadata of a JPEG, PNG, WebP, HEIC, PDF, DOCX, XLSX or PPTX
// file. The type comes from the content; an unknown one is ErrUnsupportedFormat.
// A password-protected PDF is not an error: PDF.Encrypted is set and nothing
// else can be known about it.
func Inspect(ctx context.Context, data []byte) (FileInfo, error) {
	if len(data) > MaxInputBytes {
		return FileInfo{}, ErrFileTooLarge
	}
	var info FileInfo
	err := metaOp(ctx, func(ctx context.Context) error {
		var err error
		info, err = inspect(ctx, data)
		return err
	})
	if err != nil {
		return FileInfo{}, err
	}
	return info, nil
}

func inspect(ctx context.Context, data []byte) (FileInfo, error) {
	t, ok := DetectType(data)
	if !ok {
		return FileInfo{}, ErrUnsupportedFormat
	}
	info := FileInfo{Type: t, Size: len(data)}
	var err error
	switch t {
	case FileJPEG, FilePNG, FileWebP, FileHEIC:
		err = inspectImage(ctx, data, &info)
	case FilePDF:
		err = inspectPDF(ctx, data, &info)
	case FileDOCX, FileXLSX, FilePPTX:
		err = inspectOffice(data, &info)
	}
	if err != nil {
		return FileInfo{}, err
	}
	return info, nil
}

// unreadable marks err as a parse failure of the file, keeping the cause.
func unreadable(err error) error {
	return fmt.Errorf("%w: %w", ErrUnreadable, err)
}

// sanitizeText makes a metadata value fit for a chat message: no control or
// invisible formatting characters (a right-to-left override could reorder what
// the reader sees), trimmed and capped.
func sanitizeText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxValueRunes {
		s = string(r[:maxValueRunes])
	}
	return s
}
