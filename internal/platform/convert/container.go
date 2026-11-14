package convert

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// errBadContainer is the cause of every structural failure below; callers wrap
// it with unreadable.
var errBadContainer = errors.New("malformed container")

// segment is a span [start, end) of a file: a JPEG marker segment, a PNG chunk
// (length and CRC included) or a RIFF chunk (padding included). marker is the
// JPEG marker byte; kind is the PNG or RIFF chunk type.
type segment struct {
	marker     byte
	kind       string
	start, end int
}

// JPEG markers the scanners care about.
const (
	jpegAPP1 = 0xE1
	jpegCOM  = 0xFE
	jpegSOS  = 0xDA
	jpegEOI  = 0xD9
)

// jpegSegments splits a JPEG into the marker segments before its first scan. sos
// is the offset of the SOS marker.
func jpegSegments(data []byte) (segs []segment, sos int, err error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, 0, errBadContainer
	}
	for i := 2; ; {
		for i+1 < len(data) && data[i] == 0xFF && data[i+1] == 0xFF {
			i++ // fill bytes before a marker
		}
		if i+2 > len(data) || data[i] != 0xFF {
			return nil, 0, errBadContainer
		}
		m := data[i+1]
		switch {
		case m == jpegSOS:
			return segs, i, nil
		case m == jpegEOI || m == 0x00:
			return nil, 0, errBadContainer
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7): // markers without a payload
			segs = append(segs, segment{marker: m, start: i, end: i + 2})
			i += 2
			continue
		}
		if i+4 > len(data) {
			return nil, 0, errBadContainer
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			return nil, 0, errBadContainer
		}
		segs = append(segs, segment{marker: m, start: i, end: i + 2 + n})
		i += 2 + n
	}
}

// jpegScanEnd returns the offset just after the EOI that closes the image whose
// first scan starts at sos. Progressive images have several scans, so it walks
// the markers instead of looking for the last FFD9 (a trailing MPF frame has its
// own).
func jpegScanEnd(data []byte, sos int) (int, error) {
	for i := sos; i+1 < len(data); {
		j := bytes.IndexByte(data[i:], 0xFF)
		if j < 0 {
			break
		}
		i += j
		if i+1 >= len(data) {
			break
		}
		m := data[i+1]
		switch {
		case m == jpegEOI:
			return i + 2, nil
		case m == 0x00 || (m >= 0xD0 && m <= 0xD7):
			i += 2 // stuffed byte or restart marker inside entropy data
		case m == 0xFF:
			i++
		default:
			if i+4 > len(data) {
				return 0, errBadContainer
			}
			i += 2 + int(binary.BigEndian.Uint16(data[i+2:]))
		}
	}
	return 0, errBadContainer
}

// pngChunks splits a PNG into chunks up to and including IEND. kind is the
// 4-letter chunk type.
func pngChunks(data []byte) ([]segment, error) {
	if !bytes.HasPrefix(data, []byte(pngSignature)) {
		return nil, errBadContainer
	}
	var chunks []segment
	for i := len(pngSignature); i+12 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[i:]))
		end := i + 12 + n
		if n > len(data) || end > len(data) {
			return nil, errBadContainer
		}
		kind := string(data[i+4 : i+8])
		chunks = append(chunks, segment{kind: kind, start: i, end: end})
		if kind == "IEND" {
			return chunks, nil
		}
		i = end
	}
	return nil, errBadContainer
}

// riffChunks splits a WebP into its chunks after the 12-byte RIFF header. kind
// is the fourcc; end includes the pad byte of an odd-sized payload.
func riffChunks(data []byte) ([]segment, error) {
	if len(data) < 12 {
		return nil, errBadContainer
	}
	total := 8 + int(binary.LittleEndian.Uint32(data[4:]))
	if total < 12 || total > len(data) {
		return nil, errBadContainer
	}
	var chunks []segment
	for i := 12; i+8 <= total; {
		size := int(binary.LittleEndian.Uint32(data[i+4:]))
		if i+8+size > total {
			return nil, errBadContainer
		}
		end := min(i+8+size+(size&1), total)
		chunks = append(chunks, segment{kind: string(data[i : i+4]), start: i, end: end})
		i = end
	}
	return chunks, nil
}

// isPNGText reports a chunk that carries free text or a date: tEXt, zTXt, tIME,
// and iTXt other than the XMP packet, which counts as XMP.
func isPNGText(data []byte, c segment) bool {
	switch c.kind {
	case "tEXt", "zTXt", "tIME":
		return true
	case "iTXt":
		body := data[c.start+8 : c.end-4]
		keyword, _, _ := bytes.Cut(body, []byte{0})
		return string(keyword) != "XML:com.adobe.xmp"
	}
	return false
}
