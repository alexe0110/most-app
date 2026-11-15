# Test fixtures

All files are synthetic: the "camera", "author" and coordinates are made up, so no real person's data is in the repository.

| File | What it carries |
|---|---|
| `gps.jpg` | EXIF (Apple / iPhone 15 Pro, Software, DateTimeOriginal, GPS, Orientation=6) and an XMP packet |
| `text.png` | tEXt, iTXt (XMP) and eXIf (Orientation=3) chunks |
| `exif.webp` | VP8X with EXIF and XMP chunks around the 1x1 picture of `tiny.webp` |
| `gps.heic` | `gps.jpg` converted by Apple's encoder: `sips -s format heic gps.jpg --out gps.heic` (macOS); EXIF with GPS and XMP survive the conversion |
| `word.pdf` | Info dictionary (Author, Creator "Microsoft Word 2016", Producer, dates) and an XMP stream |
| `author.docx` | `docProps` with author, last editor, company and dates |

`gen/main.go` regenerates everything except `gps.heic`: `go run internal/platform/convert/testdata/gen/main.go` from the repository root.

No third-party images are copied here. A real iPhone HEIC (MIT, from `github.com/bep/imagemeta` testdata) was used for a one-off manual check only.
