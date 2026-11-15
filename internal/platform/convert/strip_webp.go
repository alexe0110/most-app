package convert

import (
	"bytes"
	"encoding/binary"
)

// VP8X feature flags in the first payload byte.
const (
	webpFlagEXIF = 0x08
	webpFlagXMP  = 0x04
)

// stripWebP drops the EXIF and XMP chunks, clears their flags in VP8X, and
// rewrites the RIFF size. Every other chunk is copied untouched. A non-trivial
// orientation is kept as a minimal EXIF chunk at the end.
func stripWebP(data []byte) ([]byte, error) {
	chunks, err := riffChunks(data)
	if err != nil {
		return nil, unreadable(err)
	}
	orientation, hasVP8X := 0, false
	for _, c := range chunks {
		switch c.kind {
		case "EXIF":
			if orientation == 0 {
				tiff, _ := bytes.CutPrefix(data[c.start+8:c.end], []byte(exifPrefix))
				orientation = keptOrientation(tiff)
			}
		case "VP8X":
			hasVP8X = true
		}
	}
	// Without VP8X the file can't legally carry EXIF, so nothing is kept.
	if !hasVP8X {
		orientation = 0
	}

	out := make([]byte, 0, len(data))
	out = append(out, data[:12]...)
	for _, c := range chunks {
		switch c.kind {
		case "EXIF", "XMP ":
			continue
		case "VP8X":
			start := len(out)
			out = append(out, data[c.start:c.end]...)
			if c.end-c.start > 8 {
				flags := &out[start+8]
				*flags &^= webpFlagEXIF | webpFlagXMP
				if orientation > 0 {
					*flags |= webpFlagEXIF
				}
			}
		default:
			out = append(out, data[c.start:c.end]...)
		}
	}
	if orientation > 0 {
		tiff := orientationTIFF(orientation)
		out = append(out, "EXIF"...)
		out = binary.LittleEndian.AppendUint32(out, uint32(len(tiff)))
		out = append(out, tiff...)
	}
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	return out, nil
}
