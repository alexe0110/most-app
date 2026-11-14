package convert

import "encoding/binary"

// isoBox is one ISO base media box: [start, end) is the whole box, the payload
// begins at body.
type isoBox struct {
	kind             string
	start, body, end int
}

// isoBoxes splits [start, end) of data into sibling boxes.
func isoBoxes(data []byte, start, end int) ([]isoBox, error) {
	var boxes []isoBox
	for i := start; i < end; {
		if end-i < 8 {
			return nil, errBadContainer
		}
		size, hdr := uint64(binary.BigEndian.Uint32(data[i:])), 8
		switch size {
		case 0: // the box runs to the end of its parent
			size = uint64(end - i)
		case 1: // 64-bit size follows the type
			if end-i < 16 {
				return nil, errBadContainer
			}
			size, hdr = binary.BigEndian.Uint64(data[i+8:]), 16
		}
		if size < uint64(hdr) || size > uint64(end-i) {
			return nil, errBadContainer
		}
		boxes = append(boxes, isoBox{kind: string(data[i+4 : i+8]), start: i, body: i + hdr, end: i + int(size)})
		i += int(size)
	}
	return boxes, nil
}

func findBox(boxes []isoBox, kind string) (isoBox, bool) {
	for _, b := range boxes {
		if b.kind == kind {
			return b, true
		}
	}
	return isoBox{}, false
}

// fieldReader reads big-endian fields; the first out-of-range read sets bad and
// makes every later read return zero, so a parser checks once at the end.
type fieldReader struct {
	b   []byte
	pos int
	bad bool
}

// uint reads an n-byte (0..8) unsigned integer.
func (r *fieldReader) uint(n int) uint64 {
	if r.bad || n < 0 || n > 8 || r.pos+n > len(r.b) {
		r.bad = true
		return 0
	}
	var v uint64
	for _, c := range r.b[r.pos : r.pos+n] {
		v = v<<8 | uint64(c)
	}
	r.pos += n
	return v
}

func (r *fieldReader) skip(n int) {
	if r.bad || n < 0 || r.pos+n > len(r.b) {
		r.bad = true
		return
	}
	r.pos += n
}

// cstring reads a NUL-terminated string.
func (r *fieldReader) cstring() string {
	start := r.pos
	for r.pos < len(r.b) && r.b[r.pos] != 0 {
		r.pos++
	}
	if r.pos >= len(r.b) {
		r.bad = true
		return ""
	}
	s := string(r.b[start:r.pos])
	r.pos++
	return s
}
