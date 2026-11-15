package convert

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// rdfPacket wraps the given Description content in an XMP packet.
func rdfPacket(content string) []byte {
	return []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/" rdf:about="">` +
		content + `</rdf:Description></rdf:RDF></x:xmpmeta>`)
}

func TestParseXMP(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		packet []byte
		want   []xmpProp
	}{
		{"element", rdfPacket(`<xmp:CreatorTool>Photoshop</xmp:CreatorTool>`),
			[]xmpProp{{"CreatorTool", []any{"Photoshop"}}}},
		{"attribute of the description", []byte(`<rdf:Description xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:rdf="x" rdf:about="" xmp:CreatorTool="Photoshop"/>`),
			[]xmpProp{{"CreatorTool", []any{"Photoshop"}}}},
		{"list items are joined in order", rdfPacket(`<dc:creator><rdf:Seq><rdf:li>Ann</rdf:li><rdf:li>Bob</rdf:li></rdf:Seq></dc:creator>`),
			[]xmpProp{{"Creator", []any{"Ann", "Bob"}}}},
		{"language alternatives", rdfPacket(`<dc:title><rdf:Alt><rdf:li xml:lang="x-default">Offer</rdf:li></rdf:Alt></dc:title>`),
			[]xmpProp{{"Title", []any{"Offer"}}}},
		{"text outside a description is ignored", []byte(`<root><x>secret</x></root>`), []xmpProp{}},
		{"broken packet keeps what was read before the break", rdfPacket(`<xmp:CreatorTool>Photoshop</xmp:CreatorTool><xmp:Label>cut`),
			[]xmpProp{{"CreatorTool", []any{"Photoshop"}}, {"Label", []any{"cut"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, parseXMP(tt.packet))
		})
	}
}

func TestParseXMP_countsAreCapped(t *testing.T) {
	t.Parallel()
	var props strings.Builder
	for i := range maxXMPProps + 50 {
		fmt.Fprintf(&props, "<xmp:P%03d>v</xmp:P%03d>", i, i)
	}
	require.Len(t, parseXMP(rdfPacket(props.String())), maxXMPProps)

	var items strings.Builder
	for i := range maxXMPPropItems + 10 {
		fmt.Fprintf(&items, "<rdf:li>%d</rdf:li>", i)
	}
	got := parseXMP(rdfPacket(`<dc:subject><rdf:Bag>` + items.String() + `</rdf:Bag></dc:subject>`))
	require.Len(t, got, 1)
	require.Len(t, got[0].Value, maxXMPPropItems)
}

func TestParseXMP_hostileNesting(t *testing.T) {
	t.Parallel()
	deep := strings.Repeat("<a>", 100000) + "text" + strings.Repeat("</a>", 100000)

	props := parseXMP(rdfPacket(`<xmp:Before>kept</xmp:Before><xmp:Deep>` + deep + `</xmp:Deep>`))

	require.NotEmpty(t, props)
	require.Equal(t, "Before", props[0].Name, "what came before the nesting is kept")
}

func TestParseXMP_textInNestedListsBelongsToTheProperty(t *testing.T) {
	t.Parallel()

	props := parseXMP(rdfPacket(`<dc:creator><rdf:Seq><rdf:li>Ann</rdf:li><rdf:li>Bob</rdf:li></rdf:Seq></dc:creator>`))

	require.Len(t, props, 1)
	require.Equal(t, "Creator", props[0].Name)
	require.Equal(t, []any{"Ann", "Bob"}, props[0].Value)
}
