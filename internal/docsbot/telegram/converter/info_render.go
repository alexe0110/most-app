package converter

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// maxShownRunes cuts a metadata value on the screen: the values are a stranger's
// text and one of them must not fill the message.
const maxShownRunes = 64

// renderInfo is the HTML of the info screen. Every value from the file goes
// through html.EscapeString; the labels come from texts.go.
func renderInfo(title string, info convert.FileInfo) string {
	var sb strings.Builder
	sb.WriteString("<b>" + textInfoTitle + "</b>\n")

	writeGroup(&sb, groupFile, []string{
		field(fieldName, title),
		field(fieldType, typeName(info.Type)),
		field(fieldSize, formatSize(info.Size)),
	})
	if isImageType(info.Type) {
		writeGroup(&sb, groupCapture, []string{
			field(fieldTaken, formatMoment(info.Capture.Taken)),
			field(fieldCamera, cameraName(info.Capture.Make, info.Capture.Model)),
			field(fieldSoftware, info.Capture.Software),
		})
	}
	if loc := info.Location; loc != nil {
		writeGroup(&sb, groupLocation, []string{
			textInfoLocationWarn,
			field(fieldCoords, fmt.Sprintf("%.5f, %.5f", loc.Lat, loc.Lon)),
			fmt.Sprintf(`<a href="https://maps.google.com/?q=%.6f,%.6f">%s</a>`, loc.Lat, loc.Lon, textInfoMapLink),
		})
	}
	people := []string{
		field(fieldAuthor, info.People.Author),
		field(fieldCompany, info.People.Company),
		field(fieldModifier, info.People.LastModifiedBy),
	}
	if !isImageType(info.Type) {
		people = append(people, field(fieldSoftware, info.Capture.Software))
	}
	writeGroup(&sb, groupPeople, people)
	writeGroup(&sb, groupDates, []string{
		field(fieldCreated, formatMoment(info.Dates.Created)),
		field(fieldModified, formatMoment(info.Dates.Modified)),
	})
	if info.PDF != nil {
		writeGroup(&sb, groupPDF, pdfLines(info.PDF))
	}

	sb.WriteString("\n")
	switch {
	case info.PDF != nil && info.PDF.Encrypted:
		sb.WriteString(textInfoEncrypted)
	case info.Clean():
		sb.WriteString("✅ " + textInfoClean)
	default:
		fmt.Fprintf(&sb, textInfoHidden, html.EscapeString(blockNames(info, false)))
	}
	if isDocumentType(info.Type) && (info.PDF == nil || !info.PDF.Encrypted) {
		// Only the properties are read; say so instead of letting «чистый» cover more.
		sb.WriteString("\n" + textInfoDocCaveat)
	}
	return sb.String()
}

func pdfLines(pdf *convert.PDFFacts) []string {
	lines := []string{field(fieldEncrypted, yesNo(pdf.Encrypted))}
	if pdf.Encrypted {
		return lines // the rest could not be read
	}
	content := valueText
	if pdf.Scanned {
		content = valueScan
	}
	return append(lines,
		field(fieldPages, fmt.Sprint(pdf.Pages)),
		field(fieldCreator, pdf.Creator),
		field(fieldProducer, pdf.Producer),
		field(fieldContent, content),
	)
}

// writeGroup adds a titled group of lines; a group with no lines is left out.
func writeGroup(sb *strings.Builder, title string, lines []string) {
	var shown []string
	for _, l := range lines {
		if l != "" {
			shown = append(shown, l)
		}
	}
	if len(shown) == 0 {
		return
	}
	sb.WriteString("\n<b>" + title + "</b>\n" + strings.Join(shown, "\n") + "\n")
}

// field is "label: value" with the value escaped and cut; "" for an empty value.
func field(label, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if r := []rune(value); len(r) > maxShownRunes {
		value = string(r[:maxShownRunes]) + "…"
	}
	return label + ": " + html.EscapeString(value)
}

func yesNo(v bool) string {
	if v {
		return valueYes
	}
	return valueNo
}

// cameraName joins make and model without repeating the make that many models
// already start with ("Canon" + "Canon EOS 5D").
func cameraName(maker, model string) string {
	if strings.HasPrefix(model, maker) {
		return model
	}
	return strings.TrimSpace(maker + " " + model)
}

// formatMoment writes a date and time as the file has them, without a zone
// conversion; "" for an absent one.
func formatMoment(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("02.01.2006 15:04")
}

// formatSize writes a byte count in the largest fitting unit.
func formatSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f %s", float64(n)/(1<<20), unitMB)
	case n >= 1<<10:
		return fmt.Sprintf("%d %s", n>>10, unitKB)
	default:
		return fmt.Sprintf("%d %s", n, unitB)
	}
}

// isDocumentType reports the types whose content may hold more than the
// properties Inspect reads: comments, tracked changes, embedded pictures.
func isDocumentType(t convert.FileType) bool {
	switch t {
	case convert.FilePDF, convert.FileDOCX, convert.FileXLSX, convert.FilePPTX:
		return true
	}
	return false
}

func isImageType(t convert.FileType) bool {
	switch t {
	case convert.FileJPEG, convert.FilePNG, convert.FileWebP, convert.FileHEIC:
		return true
	}
	return false
}

func typeName(t convert.FileType) string {
	switch t {
	case convert.FileJPEG:
		return typeJPEG
	case convert.FilePNG:
		return typePNG
	case convert.FileWebP:
		return typeWebP
	case convert.FileHEIC:
		return typeHEIC
	case convert.FilePDF:
		return typePDF
	case convert.FileDOCX:
		return typeDOCX
	case convert.FileXLSX:
		return typeXLSX
	case convert.FilePPTX:
		return typePPTX
	}
	return ""
}

// blockNames lists what the file carries (or, with location, also the place) as
// plain text; callers escape it where needed.
func blockNames(info convert.FileInfo, withLocation bool) string {
	var names []string
	if withLocation && info.Location != nil {
		names = append(names, blockLocation)
	}
	for _, b := range info.Blocks {
		switch b {
		case convert.BlockEXIF:
			names = append(names, blockEXIF)
		case convert.BlockXMP:
			names = append(names, blockXMP)
		case convert.BlockIPTC:
			names = append(names, blockIPTC)
		case convert.BlockText:
			names = append(names, blockText)
		case convert.BlockComment:
			names = append(names, blockComment)
		case convert.BlockPDFInfo:
			names = append(names, blockPDFInfo)
		case convert.BlockOfficeProps:
			names = append(names, blockOfficeProps)
		}
	}
	return strings.Join(names, ", ")
}
