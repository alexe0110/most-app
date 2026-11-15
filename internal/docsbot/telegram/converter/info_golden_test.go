package converter

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// update rewrites the golden renders from the current output: go test -run
// Golden -update. It is a flag, not a value the code reads, so no test depends
// on it.
var update = flag.Bool("update", false, "rewrite the golden renders of the info screen")

// The two examples of issue #128, rendered from the fixtures of platform/convert.
// A change in the output shows up here as a diff of the golden file.
func TestRenderInfo_goldenIssueExamples(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, file, title, golden string
	}{
		{"photo from an iPhone", "iphone.heic", "IMG_8092.HEIC", "info_iphone.html"},
		{"document from IronPdf", "pdf_ironpdf.pdf", "polis-oferta.pdf", "info_ironpdf.html"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info, err := convert.Inspect(t.Context(), convertFixture(t, tt.file))
			require.NoError(t, err)

			got, cut := renderInfo(tt.title, info)

			require.False(t, cut, "the example fits one message")
			assertTelegramHTML(t, got)
			path := filepath.Join("testdata", tt.golden)
			if *update {
				require.NoError(t, os.MkdirAll("testdata", 0o755))
				require.NoError(t, os.WriteFile(path, []byte(got+"\n"), 0o644))
			}
			want, err := os.ReadFile(path)
			require.NoError(t, err, "run with -update to create it")
			require.Equal(t, string(want), got+"\n")
		})
	}
}
