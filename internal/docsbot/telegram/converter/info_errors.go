package converter

import (
	"context"
	"errors"
	"fmt"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// errDownload marks a failure to fetch the file, as opposed to reading it.
var errDownload = errors.New("download file")

// errStillDirty means the stripped file still carries metadata.
var errStillDirty = errors.New("metadata left after cleaning")

// infoTooLargeText is the refusal for a file over the input limit.
func infoTooLargeText() string { return fmt.Sprintf(textInfoTooLarge, convert.MaxInputBytes>>20) }

// logMetaFailure logs a failed metadata operation once. The error text of a
// parser can quote bytes of the user's file, so only its class is logged; a
// download failure is token-safe and logged whole.
func (c *Converter) logMetaFailure(ctx context.Context, what string, err error, attrs ...any) {
	if errors.Is(err, errDownload) {
		c.log.ErrorContext(ctx, what+": download failed", append(attrs, "err", err)...)
		return
	}
	c.log.WarnContext(ctx, what+" failed", append(attrs, "reason", metaFailure(err))...)
}

// metaFailure names why a metadata operation failed.
func metaFailure(err error) string {
	switch {
	case errors.Is(err, convert.ErrUnsupportedFormat):
		return "unsupported"
	case errors.Is(err, convert.ErrFileTooLarge):
		return "too_large"
	case errors.Is(err, convert.ErrPDFTooManyPages):
		return "too_many_pages"
	case errors.Is(err, convert.ErrEncrypted):
		return "encrypted"
	case errors.Is(err, convert.ErrTimeout):
		return "timeout"
	case errors.Is(err, convert.ErrUnreadable):
		return "unreadable"
	case errors.Is(err, errStillDirty):
		return "still_dirty"
	}
	return "other"
}

// infoErrorText is what the user is told about a failed metadata operation.
func infoErrorText(err error) string {
	switch {
	case errors.Is(err, errDownload):
		return textConvertDownloadFailed
	case errors.Is(err, convert.ErrUnsupportedFormat):
		return textInfoUnsupported
	case errors.Is(err, convert.ErrFileTooLarge):
		return infoTooLargeText()
	case errors.Is(err, convert.ErrPDFTooManyPages):
		return fmt.Sprintf(textPDFTooMany, convert.MaxPDFPages)
	case errors.Is(err, convert.ErrEncrypted):
		return textInfoEncrypted
	case errors.Is(err, convert.ErrTimeout):
		return textPDFTimeout
	case errors.Is(err, errStillDirty):
		return textInfoCleanFailed
	default:
		return textInfoUnreadable
	}
}
