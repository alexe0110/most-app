package convert

import (
	"bytes"
	"encoding/xml"
	"io"
)

// Limits for a standalone XMP packet (a PDF's Metadata stream): it is a stranger's
// XML.
const (
	maxXMPBytes     = 1 << 20
	maxXMPProps     = 200
	maxXMPPropItems = 16
)

// xmpProp is one top-level XMP property; list values (rdf:Seq, rdf:Bag, rdf:Alt)
// are joined into Value.
type xmpProp struct {
	Name  string
	Value any
}

// parseXMP flattens an XMP packet into its properties in document order. A
// broken packet yields what was read before the break.
func parseXMP(packet []byte) []xmpProp {
	dec := xml.NewDecoder(io.LimitReader(bytes.NewReader(packet), maxXMPBytes))
	var (
		stack  []string
		order  []string
		values = map[string][]any{}
	)
	add := func(name, value string) {
		if _, seen := values[name]; !seen {
			if len(order) == maxXMPProps {
				return
			}
			order = append(order, name)
		}
		if len(values[name]) < maxXMPPropItems {
			values[name] = append(values[name], value)
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			if t.Name.Local != "Description" {
				continue
			}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" || a.Name.Local == "about" {
					continue
				}
				add(a.Name.Local, a.Value)
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if text := string(bytes.TrimSpace(t)); text != "" {
				if name := xmpPropertyName(stack); name != "" {
					add(name, text)
				}
			}
		}
	}

	props := make([]xmpProp, 0, len(order))
	for _, name := range order {
		props = append(props, xmpProp{Name: capitalize(name), Value: values[name]})
	}
	return props
}

// xmpPropertyName is the property a text belongs to: the element directly under
// rdf:Description, however deep the text sits in lists and structures.
func xmpPropertyName(stack []string) string {
	for i := 1; i < len(stack); i++ {
		if stack[i-1] == "Description" {
			return stack[i]
		}
	}
	return ""
}

// capitalize makes "creator" (dc:creator) match the tag names of EXIF and IPTC.
func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
