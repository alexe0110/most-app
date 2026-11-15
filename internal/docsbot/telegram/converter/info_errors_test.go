package converter

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

func wrapped(err error) error { return fmt.Errorf("operation: %w", err) }

func TestMetaFailure_namesTheClassOfTheError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"unsupported", wrapped(convert.ErrUnsupportedFormat), "unsupported"},
		{"too large", wrapped(convert.ErrFileTooLarge), "too_large"},
		{"too many pages", wrapped(convert.ErrPDFTooManyPages), "too_many_pages"},
		{"encrypted", wrapped(convert.ErrEncrypted), "encrypted"},
		{"timeout", wrapped(convert.ErrTimeout), "timeout"},
		{"unreadable", wrapped(convert.ErrUnreadable), "unreadable"},
		{"still dirty", wrapped(errStillDirty), "still_dirty"},
		{"anything else", errors.New("boom"), "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, metaFailure(tt.err))
		})
	}
}

func TestInfoErrorText_mapsEachErrorToItsText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"download", wrapped(errDownload), textConvertDownloadFailed},
		{"unsupported", wrapped(convert.ErrUnsupportedFormat), textInfoUnsupported},
		{"too large", wrapped(convert.ErrFileTooLarge), infoTooLargeText()},
		{"too many pages", wrapped(convert.ErrPDFTooManyPages), fmt.Sprintf(textPDFTooMany, convert.MaxPDFPages)},
		{"encrypted", wrapped(convert.ErrEncrypted), textInfoEncrypted},
		{"timeout", wrapped(convert.ErrTimeout), textPDFTimeout},
		{"still dirty", wrapped(errStillDirty), textInfoCleanFailed},
		{"unreadable", wrapped(convert.ErrUnreadable), textInfoUnreadable},
		{"anything else", errors.New("boom"), textInfoUnreadable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, infoErrorText(tt.err))
		})
	}
}

func TestPDFErrorText_mapsEachErrorToItsText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"too large", wrapped(convert.ErrPDFTooLarge), pdfTooLargeText()},
		{"too many pages", wrapped(convert.ErrPDFTooManyPages), fmt.Sprintf(textPDFTooMany, convert.MaxPDFPages)},
		{"timeout", wrapped(convert.ErrTimeout), textPDFTimeout},
		{"bad page range", wrapped(convert.ErrBadPageRange), textPDFBadRange},
		{"anything else", errors.New("boom"), textPDFInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, pdfErrorText(tt.err))
		})
	}
}
