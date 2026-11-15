package convert

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// FileInfo is what Inspect found.
type FileInfo struct {
	Type FileType
	Size int
	// Encrypted marks an encrypted PDF. With a user password nothing else can be
	// read (the Security group only); with an owner password alone the file reads
	// in full, but Strip refuses it.
	Encrypted bool
	// Groups holds every field found, by group in display order; empty groups
	// are left out.
	Groups []GroupFields
	// Dropped counts the fields found past maxFields; they are in no group.
	Dropped int
	// NeedsPassword marks a PDF whose content hides behind a user password: only
	// the Security group is known.
	NeedsPassword bool
	// Blocks lists the metadata physically in the file; empty means clean.
	Blocks []MetaBlock
}

// Clean reports that the file carries no metadata at all.
func (i FileInfo) Clean() bool { return len(i.Blocks) == 0 }

// Inspect reads the metadata of a JPEG, PNG, WebP, HEIC, PDF, DOCX, XLSX or PPTX
// file. The type comes from the content; an unknown one is ErrUnsupportedFormat.
// An encrypted PDF is not an error: Encrypted is set. With a user password
// nothing else can be known about it; with an owner password alone it is read
// in full.
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
	s = capBytes(s, maxValueRunes)
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

// capBytes cuts a string to at most 4 bytes per rune of the limit before it is
// processed, so a huge value costs no more than a small one. A rune split by the
// cut is replaced by strings.Map.
func capBytes(s string, runes int) string {
	if len(s) > runes*4 {
		return s[:runes*4]
	}
	return s
}
