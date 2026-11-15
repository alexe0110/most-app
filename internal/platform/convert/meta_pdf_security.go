package convert

import (
	"context"
	"math"
	"regexp"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// pdfRisks are the things in a PDF that act or hide: found by looking at every
// object, not at the catalog only, since a script can sit in any annotation or
// outline item.
type pdfRisks struct {
	javascript  bool
	embedded    bool
	signed      bool
	annotations int // comments and markup; links and form widgets are not counted
}

// scanPDFObjects looks through all objects of the document.
func scanPDFObjects(pctx *model.Context) pdfRisks {
	var r pdfRisks
	for _, e := range pctx.Table {
		if e == nil || e.Free {
			continue
		}
		var d types.Dict
		switch o := e.Object.(type) {
		case types.Dict:
			d = o
		case types.StreamDict:
			d = o.Dict
		default:
			continue
		}
		if s := d.NameEntry("S"); (s != nil && *s == "JavaScript") || d.HasEntry("JS") {
			r.javascript = true
		}
		if d.HasEntry("ByteRange") && d.HasEntry("Contents") {
			r.signed = true
		}
		t := ""
		if p := d.Type(); p != nil {
			t = *p
		}
		switch t {
		case "EmbeddedFile":
			r.embedded = true
		case "Filespec":
			r.embedded = r.embedded || d.HasEntry("EF")
		case "Annot":
			if st := d.Subtype(); st == nil || (*st != "Link" && *st != "Widget" && *st != "Popup") {
				r.annotations++
			}
		}
	}
	return r
}

// startxrefLine is a real trailer: the keyword alone on its line, then the offset.
// The same word inside a title or a stream sits mid-line.
var startxrefLine = regexp.MustCompile(`(?m)^startxref[ \t]*\r?\n[ \t]*\d+`)

// incrementalUpdates counts the saves appended to the original file: every
// update adds a startxref, and a linearized file has a second one by design.
func incrementalUpdates(data []byte, pctx *model.Context) int64 {
	n := len(startxrefLine.FindAll(data, -1)) - 1
	if pctx.Read != nil && pctx.Read.Linearized {
		n--
	}
	return int64(max(n, 0))
}

// pageSize names the format of the first page (A4, Letter) or gives its size in
// millimetres.
func pageSize(ctx context.Context, pctx *model.Context) any {
	dims, err := pctx.PageDims(ctx)
	if err != nil || len(dims) == 0 {
		return nil
	}
	w, h := dims[0].Width, dims[0].Height
	if w > h {
		w, h = h, w
	}
	const tolerance = 3 // points; printers and exporters round
	for _, f := range []struct {
		name string
		w, h float64
	}{
		{"A3", 842, 1191}, {"A4", 595, 842}, {"A5", 420, 595}, {"A6", 298, 420},
		{"Letter", 612, 792}, {"Legal", 612, 1008},
	} {
		if math.Abs(w-f.w) <= tolerance && math.Abs(h-f.h) <= tolerance {
			return f.name
		}
	}
	const mmPerPoint = 25.4 / 72
	if w <= 0 || h <= 0 || math.IsNaN(w) || math.IsNaN(h) {
		return nil
	}
	return Dimensions{Width: int(math.Round(w * mmPerPoint)), Height: int(math.Round(h * mmPerPoint))}
}

// xmpProps reads the XMP packet of the catalog; nil if there is none or it can't
// be decoded.
func xmpProps(pctx *model.Context) []xmpProp {
	obj, ok := pctx.RootDict["Metadata"]
	if !ok {
		return nil
	}
	sd, _, err := pctx.DereferenceStreamDict(obj)
	if err != nil || sd == nil {
		return nil
	}
	if err := sd.DecodeWithLimit(maxXMPBytes); err != nil {
		return nil
	}
	return parseXMP(sd.Content)
}
