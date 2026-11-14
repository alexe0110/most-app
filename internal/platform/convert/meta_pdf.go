package convert

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// inspectPDF reads the document info dictionary and the catalog's XMP stream. A
// PDF that needs a password is reported as encrypted, not as an error.
func inspectPDF(ctx context.Context, data []byte, info *FileInfo) error {
	pctx, err := api.ReadAndValidate(ctx, bytes.NewReader(data), pdfConf())
	if errors.Is(err, pdfcpu.ErrWrongPassword) {
		info.PDF = &PDFFacts{Encrypted: true}
		return nil
	}
	if err != nil {
		return unreadable(err)
	}
	// Optimizing collects the fonts; a document with none is a scan.
	if err := api.OptimizeContext(ctx, pctx); err != nil {
		return unreadable(err)
	}

	info.PDF = &PDFFacts{
		Pages:     pctx.PageCount,
		Creator:   sanitizeText(pctx.Creator),
		Producer:  sanitizeText(pctx.Producer),
		Scanned:   len(pctx.Optimize.Fonts) == 0,
		Encrypted: pctx.Encrypt != nil,
	}
	info.People.Author = sanitizeText(pctx.Author)
	info.Dates.Created = pdfDate(pctx.XRefTable.CreationDate)
	info.Dates.Modified = pdfDate(pctx.ModDate)

	if hasPDFInfo(pctx) {
		info.Blocks = append(info.Blocks, BlockPDFInfo)
	}
	if _, ok := pctx.RootDict["Metadata"]; ok {
		info.Blocks = append(info.Blocks, BlockXMP)
	}
	return nil
}

// hasPDFInfo reports any entry of the document info dictionary, standard or
// custom.
func hasPDFInfo(pctx *model.Context) bool {
	return pctx.Title != "" || pctx.Subject != "" || pctx.Author != "" || pctx.Keywords != "" ||
		pctx.Creator != "" || pctx.Producer != "" || pctx.XRefTable.CreationDate != "" || pctx.ModDate != "" ||
		len(pctx.Properties) > 0
}

// pdfDate parses a PDF date string ("D:20240102150405+03'00'"); zero if absent
// or malformed.
func pdfDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, ok := types.DateTime(s, true)
	if !ok {
		return time.Time{}
	}
	return t
}
