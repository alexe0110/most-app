package convert

import (
	"bytes"
	"encoding/binary"
)

// JPEG markers kept or inspected by stripJPEG besides those in container.go.
const (
	jpegAPP0  = 0xE0
	jpegAPP2  = 0xE2
	jpegAPP14 = 0xEE
)

// stripJPEG drops EXIF, XMP, IPTC, comments and other application segments
// before the first scan and everything after the closing EOI (a trailing MPF
// frame has metadata of its own). Segments the decoder needs stay, and the scan
// data is copied untouched.
func stripJPEG(data []byte) ([]byte, error) {
	segs, sos, err := jpegSegments(data)
	if err != nil {
		return nil, unreadable(err)
	}
	end, err := jpegScanEnd(data, sos)
	if err != nil {
		return nil, unreadable(err)
	}

	var kept []segment
	orientation := 0
	for _, s := range segs {
		if keepJPEGSegment(data, s) {
			kept = append(kept, s)
		} else if s.marker == jpegAPP1 && orientation == 0 {
			orientation = jpegSegmentOrientation(data, s)
		}
	}

	out := make([]byte, 0, len(data))
	out = append(out, 0xFF, 0xD8)
	// JFIF wants to be the first segment, and Exif comes right after it.
	at := 0
	if len(kept) > 0 && kept[0].marker == jpegAPP0 {
		at = 1
	}
	for _, s := range kept[:at] {
		out = append(out, data[s.start:s.end]...)
	}
	if orientation > 0 {
		out = append(out, orientationSegment(orientation)...)
	}
	for _, s := range kept[at:] {
		out = append(out, data[s.start:s.end]...)
	}
	return append(out, data[sos:end]...), nil
}

// keepJPEGSegment keeps what decoding or colour needs: tables, frame headers,
// JFIF, the ICC profile and the Adobe colour transform. APP1 (EXIF, XMP), the
// other application segments and comments go.
func keepJPEGSegment(data []byte, s segment) bool {
	body := segmentBody(data, s)
	switch {
	case s.marker == jpegAPP0:
		return bytes.HasPrefix(body, []byte("JFIF\x00"))
	case s.marker == jpegAPP2:
		return bytes.HasPrefix(body, []byte("ICC_PROFILE\x00"))
	case s.marker == jpegAPP14:
		return true
	case s.marker >= 0xE1 && s.marker <= 0xEF, s.marker == jpegCOM:
		return false
	}
	return true
}

// jpegSegmentOrientation reads the orientation (2..8) of an Exif APP1 segment, or 0.
func jpegSegmentOrientation(data []byte, s segment) int {
	body := segmentBody(data, s)
	if !bytes.HasPrefix(body, []byte(exifPrefix)) {
		return 0
	}
	return keptOrientation(body[len(exifPrefix):])
}

// segmentBody is the payload of a marker segment, after the marker and length.
func segmentBody(data []byte, s segment) []byte {
	if s.end-s.start < 4 {
		return nil
	}
	return data[s.start+4 : s.end]
}

// orientationSegment is an APP1 segment with a minimal EXIF holding o.
func orientationSegment(o int) []byte {
	tiff := orientationTIFF(o)
	seg := make([]byte, 0, 4+len(exifPrefix)+len(tiff))
	seg = append(seg, 0xFF, jpegAPP1)
	seg = binary.BigEndian.AppendUint16(seg, uint16(2+len(exifPrefix)+len(tiff)))
	seg = append(seg, exifPrefix...)
	return append(seg, tiff...)
}
