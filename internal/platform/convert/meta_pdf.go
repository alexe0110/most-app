package convert

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// inspectPDF reads the document info dictionary and the page facts. A PDF that
// needs a password is reported as encrypted, not as an error.
func inspectPDF(ctx context.Context, data []byte, info *FileInfo) error {
	pctx, err := api.ReadAndValidate(ctx, bytes.NewReader(data), pdfConf())
	if errors.Is(err, pdfcpu.ErrWrongPassword) {
		info.Encrypted = true
		fs := newFieldSet()
		fs.add(GroupSecurity, "PDF:Encrypted", true, false)
		info.Groups = fs.result()
		return nil
	}
	if err != nil {
		return unreadable(err)
	}
	if pctx.PageCount > MaxPDFPages {
		return ErrPDFTooManyPages
	}
	// Optimizing collects the fonts; a document with none is a scan.
	if err := api.OptimizeContext(ctx, pctx); err != nil {
		return unreadable(err)
	}

	fs := newFieldSet()
	info.Encrypted = pctx.Encrypt != nil
	addPDFInfo(fs, pctx)
	addPDFFacts(ctx, fs, pctx, data, info.Encrypted)
	for _, p := range xmpProps(pctx) {
		addValue(fs, "XMP:"+sanitizeText(p.Name), p.Name, normalize(p.Value), "")
	}
	info.Groups = fs.result()

	if hasPDFInfo(pctx) {
		info.Blocks = append(info.Blocks, BlockPDFInfo)
	}
	if _, ok := pctx.RootDict["Metadata"]; ok {
		info.Blocks = append(info.Blocks, BlockXMP)
	}
	return nil
}

// addPDFFacts adds what the document is and what it can do.
func addPDFFacts(ctx context.Context, fs *fieldSet, pctx *model.Context, data []byte, encrypted bool) {
	risks := scanPDFObjects(pctx)
	content := ContentText
	if len(pctx.Optimize.Fonts) == 0 {
		content = ContentScan
	}
	fs.add(GroupDocument, "PDF:Pages", int64(pctx.PageCount), false)
	fs.add(GroupDocument, "PDF:PageSize", pageSize(ctx, pctx), false)
	fs.add(GroupDocument, "PDF:Content", content, false)
	if risks.annotations > 0 {
		fs.add(GroupDocument, "PDF:Annotations", int64(risks.annotations), false)
	}
	if pctx.HeaderVersion != nil {
		fs.add(GroupDocument, "PDF:Version", pctx.HeaderVersion.String(), false)
	}
	fs.add(GroupDates, "PDF:IncrementalUpdates", incrementalUpdates(data, pctx), false)
	fs.add(GroupSecurity, "PDF:Encrypted", encrypted, false)
	fs.add(GroupSecurity, "PDF:JavaScript", risks.javascript, false)
	fs.add(GroupSecurity, "PDF:EmbeddedFiles", risks.embedded, false)
	fs.add(GroupSecurity, "PDF:Signature", risks.signed, false)
}

// addPDFInfo adds every entry of the document info dictionary, standard or
// custom.
func addPDFInfo(fs *fieldSet, pctx *model.Context) {
	fs.add(GroupAuthor, "PDF:Author", sanitizeText(pctx.Author), true)
	fs.add(GroupDocument, "PDF:Title", sanitizeText(pctx.Title), false)
	fs.add(GroupDocument, "PDF:Subject", sanitizeText(pctx.Subject), false)
	fs.add(GroupDocument, "PDF:Keywords", sanitizeText(pctx.Keywords), false)
	fs.add(GroupDocument, "PDF:Creator", sanitizeText(pctx.Creator), false)
	fs.add(GroupDocument, "PDF:Producer", sanitizeText(pctx.Producer), false)
	if m, ok := pdfMoment(pctx.XRefTable.CreationDate); ok {
		fs.add(GroupDates, "PDF:CreationDate", m, false)
	}
	if m, ok := pdfMoment(pctx.ModDate); ok {
		fs.add(GroupDates, "PDF:ModDate", m, false)
	}
	for _, name := range slices.Sorted(maps.Keys(pctx.Properties)) {
		value := sanitizeText(pctx.Properties[name])
		fs.add(GroupOther, "PDF:"+sanitizeText(name), value, looksLikeUserPath(value))
	}
}

// hasPDFInfo reports any entry of the document info dictionary, standard or
// custom.
func hasPDFInfo(pctx *model.Context) bool {
	return pctx.Title != "" || pctx.Subject != "" || pctx.Author != "" || pctx.Keywords != "" ||
		pctx.Creator != "" || pctx.Producer != "" || pctx.XRefTable.CreationDate != "" || pctx.ModDate != "" ||
		len(pctx.Properties) > 0
}

// pdfMoment parses a PDF date string ("D:20240102150405+03'00'"); false if absent
// or malformed. The zone is known when the string has one after the seconds.
func pdfMoment(s string) (Moment, bool) {
	if s == "" {
		return Moment{}, false
	}
	t, ok := types.DateTime(s, true)
	if !ok {
		return Moment{}, false
	}
	return Moment{Time: t, HasZone: strings.ContainsAny(strings.TrimPrefix(s, "D:"), "Z+-")}, true
}
