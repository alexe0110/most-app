package convert

import (
	"bytes"
)

// HEIC items stripHEIC blanks.
const (
	itemExif = "Exif"
	itemMime = "mime"
	mimeXMP  = "application/rdf+xml"
)

// heicItem is an entry of the item information box.
type heicItem struct {
	kind        string
	contentType string
}

// isMetadata reports the Exif item and XMP packets, the items stripHEIC blanks.
func (i heicItem) isMetadata() bool {
	return i.kind == itemExif || (i.kind == itemMime && i.contentType == mimeXMP)
}

// heicExtent is one piece of an item's data, as stored in the item location box.
type heicExtent struct {
	offset, length uint64
}

// heicLocation says where the bytes of one item are.
type heicLocation struct {
	id      uint32
	method  int // 0: offset in the file, 1: offset in the idat box
	base    uint64
	extents []heicExtent
}

// stripHEIC removes the metadata of a HEIC. The image cannot be decoded without
// cgo, and re-encoding would lose quality anyway, so the Exif and XMP items are
// overwritten in place with an empty equivalent of the same length: no box
// size, no offset and no other byte moves, and the file stays a valid
// container.
func stripHEIC(data []byte) ([]byte, error) {
	l, err := parseHEIC(data)
	if err != nil {
		return nil, err
	}
	out := bytes.Clone(data)
	for _, loc := range l.locs {
		item, ok := l.items[loc.id]
		if !ok || !item.isMetadata() {
			continue
		}
		if err := blankItem(out, loc, item.kind == itemExif, l); err != nil {
			return nil, unreadable(err)
		}
	}
	return out, nil
}

// heicLayout is what stripping needs from a HEIC: its items, where they lie and
// the idat box that construction method 1 points into.
type heicLayout struct {
	items map[uint32]heicItem
	locs  []heicLocation
	meta  isoBox
	idat  isoBox
}

// parseHEIC reads the item layout of the meta box. Inspect calls it too, so a
// container Strip would refuse is never reported as clean.
func parseHEIC(data []byte) (heicLayout, error) {
	top, err := isoBoxes(data, 0, len(data))
	if err != nil {
		return heicLayout{}, unreadable(err)
	}
	meta, ok := findBox(top, "meta")
	if !ok || meta.body+4 > meta.end {
		return heicLayout{}, unreadable(errBadContainer)
	}
	kids, err := isoBoxes(data, meta.body+4, meta.end) // meta is a full box: version and flags first
	if err != nil {
		return heicLayout{}, unreadable(err)
	}
	iinf, hasIinf := findBox(kids, "iinf")
	iloc, hasIloc := findBox(kids, "iloc")
	if !hasIinf || !hasIloc {
		return heicLayout{}, unreadable(errBadContainer)
	}
	idat, _ := findBox(kids, "idat")

	items, err := parseIinf(data, iinf)
	if err != nil {
		return heicLayout{}, unreadable(err)
	}
	locs, err := parseIloc(data[iloc.body:iloc.end])
	if err != nil {
		return heicLayout{}, unreadable(err)
	}
	return heicLayout{items: items, locs: locs, meta: meta, idat: idat}, nil
}

// blankItem overwrites the extents of one item with an empty Exif or XMP of the
// same total length. Every extent must lie in the data it points into: the idat
// body for construction method 1, the file outside the meta box for method 0. A
// hostile iloc could otherwise aim the blanking at the boxes that describe the
// file.
func blankItem(data []byte, loc heicLocation, isExif bool, l heicLayout) error {
	lo, hi := uint64(0), uint64(len(data))
	switch loc.method {
	case 0:
	case 1:
		if l.idat.kind != "idat" {
			return errBadContainer
		}
		lo, hi = uint64(l.idat.body), uint64(l.idat.end)
	default:
		return errBadContainer // item offsets are not used for metadata
	}

	spans := make([][2]uint64, 0, len(loc.extents))
	var total uint64
	for _, e := range loc.extents {
		room := hi - lo
		if loc.base > room || e.offset > room-loc.base || e.length > room-loc.base-e.offset {
			return errBadContainer
		}
		start := lo + loc.base + e.offset
		end := start + e.length
		if loc.method == 0 && e.length > 0 && start < uint64(l.meta.end) && end > uint64(l.meta.start) {
			return errBadContainer
		}
		if total += e.length; total > uint64(len(data)) {
			return errBadContainer
		}
		spans = append(spans, [2]uint64{start, end})
	}

	blank := blankXMP(int(total))
	if isExif {
		blank = blankExif(int(total))
	}
	for _, sp := range spans {
		n := copy(data[sp[0]:sp[1]], blank)
		blank = blank[n:]
	}
	return nil
}

// blankExif is an Exif item of n bytes with an empty IFD0, zero-padded: the
// tiff-header offset (0), a little-endian TIFF header and zero entries.
func blankExif(n int) []byte {
	b := make([]byte, n)
	copy(b, []byte{
		0, 0, 0, 0,
		'I', 'I', 42, 0, 8, 0, 0, 0,
		0, 0, 0, 0, 0, 0,
	})
	return b
}

// blankXMP is an empty XMP packet padded to n bytes with spaces, which XML
// ignores. A packet too short for the wrapper is all spaces.
func blankXMP(n int) []byte {
	const (
		head = `<?xpacket begin="` + "\xEF\xBB\xBF" + `" id="W5M0MpCehiHzreSzNTczkc9d"?><x:xmpmeta xmlns:x="adobe:ns:meta/"/>`
		tail = `<?xpacket end="w"?>`
	)
	b := bytes.Repeat([]byte{' '}, n)
	if n >= len(head)+len(tail) {
		copy(b, head)
		copy(b[n-len(tail):], tail)
	}
	return b
}

// parseIinf reads the item information box into a map by item id.
func parseIinf(data []byte, iinf isoBox) (map[uint32]heicItem, error) {
	r := fieldReader{b: data[iinf.body:iinf.end]}
	version := r.uint(1)
	r.skip(3)
	if version == 0 {
		r.uint(2) // entry count; the entries are walked instead
	} else {
		r.uint(4)
	}
	if r.bad {
		return nil, errBadContainer
	}
	entries, err := isoBoxes(data, iinf.body+r.pos, iinf.end)
	if err != nil {
		return nil, err
	}

	items := map[uint32]heicItem{}
	for _, e := range entries {
		if e.kind != "infe" {
			continue
		}
		er := fieldReader{b: data[e.body:e.end]}
		v := er.uint(1)
		er.skip(3)
		if v < 2 {
			continue // no item_type: not a HEIC-era entry
		}
		var id uint64
		if v == 2 {
			id = er.uint(2)
		} else {
			id = er.uint(4)
		}
		er.skip(2) // protection index
		item := heicItem{kind: string(er.b[min(er.pos, len(er.b)):min(er.pos+4, len(er.b))])}
		er.skip(4)
		er.cstring() // item name
		if item.kind == itemMime {
			item.contentType = er.cstring()
		}
		if er.bad {
			return nil, errBadContainer
		}
		items[uint32(id)] = item
	}
	return items, nil
}

// parseIloc reads the item location box payload (versions 0 to 2).
func parseIloc(b []byte) ([]heicLocation, error) {
	r := fieldReader{b: b}
	version := r.uint(1)
	r.skip(3)
	sizes := r.uint(1)
	offSize, lenSize := int(sizes>>4), int(sizes&15)
	sizes = r.uint(1)
	baseSize, idxSize := int(sizes>>4), int(sizes&15)
	if version > 2 || r.bad || !validFieldSize(offSize) || !validFieldSize(baseSize) || lenSize == 0 || !validFieldSize(lenSize) {
		return nil, errBadContainer
	}
	if version == 0 {
		idxSize = 0
	}

	var count uint64
	if version < 2 {
		count = r.uint(2)
	} else {
		count = r.uint(4)
	}
	var locs []heicLocation
	for i := uint64(0); i < count && !r.bad; i++ {
		var loc heicLocation
		if version < 2 {
			loc.id = uint32(r.uint(2))
		} else {
			loc.id = uint32(r.uint(4))
		}
		if version >= 1 {
			loc.method = int(r.uint(2) & 0xF)
		}
		r.uint(2) // data reference index
		loc.base = r.uint(baseSize)
		for n := r.uint(2); n > 0 && !r.bad; n-- {
			r.uint(idxSize)
			offset := r.uint(offSize)
			length := r.uint(lenSize)
			loc.extents = append(loc.extents, heicExtent{offset: offset, length: length})
		}
		locs = append(locs, loc)
	}
	if r.bad {
		return nil, errBadContainer
	}
	return locs, nil
}

// validFieldSize: sizes of offset-like iloc fields are 0, 4 or 8 bytes.
func validFieldSize(n int) bool { return n == 0 || n == 4 || n == 8 }
