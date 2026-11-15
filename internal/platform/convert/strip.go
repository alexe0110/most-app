package convert

import (
	"context"
)

// exifPrefix starts an Exif payload in a JPEG APP1 segment.
const exifPrefix = "Exif\x00\x00"

// Strip returns a copy of a JPEG, PNG, WebP, HEIC, PDF, DOCX, XLSX or PPTX file
// without its metadata. Pictures are never re-encoded: the pixel data stays
// byte for byte, and only a non-trivial EXIF orientation is kept (as a minimal
// EXIF), because dropping it would turn the photo on its side. The type comes
// from the content; an unknown one is ErrUnsupportedFormat, a password-protected
// PDF is ErrEncrypted.
func Strip(ctx context.Context, data []byte) ([]byte, error) {
	if len(data) > MaxInputBytes {
		return nil, ErrFileTooLarge
	}
	var out []byte
	err := metaOp(ctx, func(ctx context.Context) error {
		var err error
		out, err = strip(ctx, data)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func strip(ctx context.Context, data []byte) ([]byte, error) {
	t, ok := DetectType(data)
	if !ok {
		return nil, ErrUnsupportedFormat
	}
	switch t {
	case FileJPEG:
		return stripJPEG(data)
	case FilePNG:
		return stripPNG(data)
	case FileWebP:
		return stripWebP(data)
	case FileHEIC:
		return stripHEIC(data)
	case FilePDF:
		return stripPDF(ctx, data)
	default:
		return stripOffice(data)
	}
}

// orientationTIFF is a little-endian TIFF block holding only the orientation tag,
// the smallest EXIF that keeps a rotated photo upright.
func orientationTIFF(o int) []byte {
	return []byte{
		'I', 'I', 42, 0, 8, 0, 0, 0, // header, IFD0 at 8
		1, 0, // one entry
		0x12, 0x01, 3, 0, 1, 0, 0, 0, byte(o), 0, 0, 0, // Orientation, SHORT, 1 value
		0, 0, 0, 0, // no next IFD
	}
}

// keptOrientation returns the orientation (2..8) worth preserving from a TIFF
// block, or 0 when the picture is upright or the block is unreadable.
func keptOrientation(tiff []byte) int {
	o, ok := orientationFromAPP1(append([]byte(exifPrefix), tiff...))
	if !ok || o < 2 {
		return 0
	}
	return o
}
