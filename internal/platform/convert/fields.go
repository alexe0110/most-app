package convert

import (
	"slices"
	"time"
)

// Group is the section of the info screen a field belongs to. The numbers are
// the order of the sections.
type Group int

// Groups of FileInfo, in display order.
const (
	GroupLocation Group = iota
	GroupAuthor
	GroupDevice
	GroupDates
	GroupShooting
	GroupImage
	GroupDocument
	GroupSecurity
	GroupOther
)

// Field is one value found in a file. Key names the tag with its source
// ("EXIF:Make", "PDF:Producer"); labels and number formatting are the caller's
// business. Value is one of: string, int64, float64, bool, Moment, Coordinates,
// Dimensions, Binary, or a []any of those.
type Field struct {
	Key       string
	Value     any
	Sensitive bool
}

// GroupFields are the fields of one group.
type GroupFields struct {
	Group  Group
	Fields []Field
}

// Moment is a date and time as the file has it. HasZone is false when the file
// gives no zone and Time is only a wall-clock reading.
type Moment struct {
	Time    time.Time
	HasZone bool
}

// Coordinates is a place in degrees; south and west are negative.
type Coordinates struct {
	Lat, Lon float64
}

// Dimensions is a size in pixels.
type Dimensions struct {
	Width, Height int
}

// Binary stands for a value that is not text; only its size is kept.
type Binary struct {
	Bytes int
}

// PDF page content kinds, the values of the "PDF:Content" field.
const (
	ContentText = "text"
	ContentScan = "scan" // no fonts: the pages are pictures
)

// Field returns the field with the given key.
func (i FileInfo) Field(key string) (Field, bool) {
	for _, g := range i.Groups {
		for _, f := range g.Fields {
			if f.Key == key {
				return f, true
			}
		}
	}
	return Field{}, false
}

// FieldCount is the number of fields found, those past the cap included.
func (i FileInfo) FieldCount() int {
	n := i.Dropped
	for _, g := range i.Groups {
		n += len(g.Fields)
	}
	return n
}

// SensitiveFields lists the fields that reveal a person, a place or a device.
func (i FileInfo) SensitiveFields() []Field {
	var out []Field
	for _, g := range i.Groups {
		for _, f := range g.Fields {
			if f.Sensitive {
				out = append(out, f)
			}
		}
	}
	return out
}

// fieldSet collects fields while a file is read. The first field with a key
// wins, so the main image's tags beat a thumbnail's.
type fieldSet struct {
	groups  map[Group][]Field
	seen    map[string]bool
	count   int
	dropped int // fields past maxFields
}

// maxFields caps the fields of one file: PNG text chunks, PDF custom properties
// and XMP are a stranger's, and each field is memory and screen.
const maxFields = 1000

func newFieldSet() *fieldSet {
	return &fieldSet{groups: map[Group][]Field{}, seen: map[string]bool{}}
}

// add stores a field; empty values and repeated keys are dropped.
func (s *fieldSet) add(g Group, key string, value any, sensitive bool) {
	if value == nil || value == "" || s.seen[key] {
		return
	}
	if s.count == maxFields {
		s.dropped++
		return
	}
	s.count++
	s.seen[key] = true
	s.groups[g] = append(s.groups[g], Field{Key: key, Value: value, Sensitive: sensitive})
}

// result lists the non-empty groups in display order.
func (s *fieldSet) result() []GroupFields {
	out := make([]GroupFields, 0, len(s.groups))
	for g, fields := range s.groups {
		out = append(out, GroupFields{Group: g, Fields: fields})
	}
	slices.SortFunc(out, func(a, b GroupFields) int { return int(a.Group) - int(b.Group) })
	return out
}
