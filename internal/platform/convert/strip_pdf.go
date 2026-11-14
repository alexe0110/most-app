package convert

import (
	"bytes"
	"context"
	"errors"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// stripPDF empties the document info dictionary and removes the catalog's XMP
// stream. pdfcpu stamps Producer and both dates on every write unless the info
// dictionary is marked as preserved, so an empty dictionary is installed first
// and preserved. XMP streams of single pages or images are left as they are.
func stripPDF(ctx context.Context, data []byte) ([]byte, error) {
	pctx, err := api.ReadAndValidate(ctx, bytes.NewReader(data), pdfConf())
	if errors.Is(err, pdfcpu.ErrWrongPassword) {
		return nil, ErrEncrypted
	}
	if err != nil {
		return nil, unreadable(err)
	}
	if pctx.Encrypt != nil {
		return nil, ErrEncrypted
	}

	info, err := pctx.IndRefForNewObject(types.NewDict())
	if err != nil {
		return nil, unreadable(err)
	}
	pctx.Info = info
	pctx.PreserveInfoDict = true
	pctx.RootDict.Delete("Metadata")

	var out bytes.Buffer
	if err := api.WriteContext(ctx, pctx, &out); err != nil {
		return nil, unreadable(err)
	}
	return out.Bytes(), nil
}
