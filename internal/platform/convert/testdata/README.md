# Test fixtures

All files are synthetic: the "camera", "author" and coordinates are made up, so no real person's data is in the repository. Field counts are what `convert.Inspect` finds.

| File | What it carries |
|---|---|
| `gps.jpg` | EXIF (Apple / iPhone 15 Pro, Software, DateTimeOriginal, GPS, Orientation=6) and an XMP packet |
| `text.png` | tEXt, iTXt (XMP) and eXIf (Orientation=3) chunks |
| `exif.webp` | VP8X with EXIF and XMP chunks around the 1x1 picture of `tiny.webp` |
| `gps.heic` | `gps.jpg` converted by Apple's encoder: `sips -s format heic gps.jpg --out gps.heic` (macOS); EXIF with GPS and XMP survive the conversion |
| `word.pdf` | Info dictionary (Author, Creator "Microsoft Word 2016", Producer, dates) and an XMP stream |
| `author.docx` | `docProps` with author, last editor, company and dates |
| `iphone.jpg` | Photo as an iPhone 17 writes it: camera and lens, shooting settings, local time with `OffsetTimeOriginal` (+05:00), GPS with altitude and direction, a vendor MakerNote blob, XMP with `ContentIdentifier` (25 fields) |
| `iphone.heic` | `iphone.jpg` converted by Apple's encoder: `sips -s format heic iphone.jpg --out iphone.heic` (macOS). The EXIF survives, the XMP `ContentIdentifier` does not (28 fields) |
| `pdf_scan.pdf` | A scanner's output: one A4 page that is a picture, with no fonts, so `PDF:Content` is "scan" (13 fields) |
| `pdf_ironpdf.pdf` | A four-page document of a converter that names itself: Creator and Producer "IronPdf", XMP with `DocumentID`, a standard font referenced and not embedded, one save after creation (16 fields) |
| `pdf_active.pdf` | JavaScript on open and in the name tree, an embedded file, a signature field with an empty value, a comment annotation (JavaScript, embedded files and signature all true) |

`gen/*.go` regenerates everything except the HEIC files, which need a HEIC encoder: `go run internal/platform/convert/testdata/gen/*.go` from the repository root. The generator is split over files by meaning (`main.go` images and office files, `iphone.go` the iPhone photo, `pdf.go` the PDF writer), so all of them are passed.

The PDFs of `pdf.go` are written byte by byte, so their cross-reference tables and revisions are exact: the tests check what a reader sees, not what a library writes.

No third-party images are copied here. A real iPhone HEIC (MIT, from `github.com/bep/imagemeta` testdata) was used for a one-off manual check only.
