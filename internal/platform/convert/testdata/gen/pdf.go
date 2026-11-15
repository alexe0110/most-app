//go:build ignore

package main

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"log"
)

// pdfObject is one indirect object: its number and its body, as written between
// "N 0 obj" and "endobj".
type pdfObject struct {
	num  int
	body []byte
}

// pdfStream is a stream object body: the dictionary entries and the raw data.
func pdfStream(dict string, data []byte) []byte {
	return []byte(fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data))
}

// pdfRevision appends a revision to file (a new file when it is empty): the
// objects, a cross-reference table for them and a trailer. prev is the startxref
// of the revision before, or -1 for the first one. It returns the file and the
// startxref of the new revision.
func pdfRevision(file []byte, objs []pdfObject, info int, prev int) ([]byte, int) {
	if len(file) == 0 {
		file = []byte("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	}
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = len(file)
		file = append(file, fmt.Sprintf("%d 0 obj\n", o.num)...)
		file = append(file, o.body...)
		file = append(file, "\nendobj\n"...)
	}
	xref := len(file)
	var buf bytes.Buffer
	buf.WriteString("xref\n")
	size := 0
	for i, o := range objs {
		fmt.Fprintf(&buf, "%d 1\n%010d 00000 n \n", o.num, offsets[i])
		size = max(size, o.num+1)
	}
	trailer := fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R", size, info)
	if prev >= 0 {
		trailer += fmt.Sprintf(" /Prev %d", prev)
	}
	fmt.Fprintf(&buf, "%s >>\nstartxref\n%d\n%%%%EOF\n", trailer, xref)
	return append(file, buf.Bytes()...), xref
}

// pdfPage is a letter-size page (A4 is used for the scan) showing content.
func pdfPage(num, parent, content int, resources string) pdfObject {
	return pdfObject{num, []byte(fmt.Sprintf(
		"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 595 842] /Resources << %s >> /Contents %d 0 R >>",
		parent, resources, content))}
}

// scanPDF is what a scanner writes: one page that is a picture, with no fonts.
func scanPDF() []byte {
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, picture(640, 480), &jpeg.Options{Quality: 70}); err != nil {
		log.Fatal(err)
	}
	objs := []pdfObject{
		{1, []byte("<< /Type /Catalog /Pages 2 0 R >>")},
		{2, []byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")},
		pdfPage(3, 2, 5, "/XObject << /Im1 4 0 R >>"),
		{4, pdfStream("/Type /XObject /Subtype /Image /Width 640 /Height 480 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode", jpg.Bytes())},
		{5, pdfStream("", []byte("q 595 0 0 842 0 0 cm /Im1 Do Q"))},
		{6, []byte("<< /Creator (Scan Station) /Producer (Scanner Firmware 3.2) /CreationDate (D:20260520062000+05'00') /ModDate (D:20260520062000+05'00') >>")},
	}
	out, _ := pdfRevision(nil, objs, 6, -1)
	return out
}

// ironPDF is a four-page document of a converter that names itself in the Info
// dictionary and in XMP, with a standard font referenced and not embedded. The
// file was saved once after it was made, as an edited file is.
func ironPDF() []byte {
	const xmp = `<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>` +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmlns:xmpMM="http://ns.adobe.com/xap/1.0/mm/" xmlns:pdf="http://ns.adobe.com/pdf/1.3/">` +
		`<xmp:CreatorTool>IronPdf</xmp:CreatorTool><pdf:Producer>IronPdf v2023.1.11387</pdf:Producer>` +
		`<xmpMM:DocumentID>uuid:5f2c0d7e-3a9b-4c1e-8d2f-6b7a9e0c1d42</xmpMM:DocumentID>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta><?xpacket end="w"?>`
	const resources = "/Font << /F1 8 0 R >>"
	info := func(modDate string) pdfObject {
		return pdfObject{10, []byte("<< /Creator (IronPdf) /Producer (IronPdf v2023.1.11387) " +
			"/CreationDate (D:20260520062000+00'00') /ModDate (" + modDate + ") >>")}
	}
	objs := []pdfObject{
		{1, []byte("<< /Type /Catalog /Pages 2 0 R /Metadata 9 0 R >>")},
		{2, []byte("<< /Type /Pages /Kids [3 0 R 5 0 R 7 0 R 11 0 R] /Count 4 >>")},
		pdfPage(3, 2, 4, resources),
		{4, pdfStream("", []byte("BT /F1 12 Tf 72 770 Td (Offer) Tj ET"))},
		pdfPage(5, 2, 4, resources),
		pdfPage(7, 2, 4, resources),
		{8, []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")},
		{9, pdfStream("/Type /Metadata /Subtype /XML", []byte(xmp))},
		info("D:20260520062000+00'00'"),
		pdfPage(11, 2, 4, resources),
	}
	base, xref := pdfRevision(nil, objs, 10, -1)
	out, _ := pdfRevision(base, []pdfObject{info("D:20260521174300+00'00'")}, 10, xref)
	return out
}

// activePDF carries what a reader should warn about: JavaScript that runs on
// open, a script in the name tree, an embedded file, a comment and a signature
// field with its value.
func activePDF() []byte {
	objs := []pdfObject{
		{1, []byte("<< /Type /Catalog /Pages 2 0 R /OpenAction 7 0 R /Names << /JavaScript 8 0 R /EmbeddedFiles 9 0 R >> /AcroForm << /Fields [12 0 R] /SigFlags 3 >> >>")},
		{2, []byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")},
		{3, []byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R /Annots [14 0 R] >>")},
		{4, pdfStream("", []byte("BT /F1 12 Tf 72 770 Td (Contract) Tj ET"))},
		{5, []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")},
		{6, []byte("<< /Producer (Active Writer 4) /CreationDate (D:20260101120000Z) >>")},
		{7, []byte("<< /Type /Action /S /JavaScript /JS (app.alert\\(\"Hello\"\\);) >>")},
		{8, []byte("<< /Names [(script) 7 0 R] >>")},
		{9, []byte("<< /Names [(note.txt) 10 0 R] >>")},
		{10, []byte("<< /Type /Filespec /F (note.txt) /EF << /F 11 0 R >> >>")},
		{11, pdfStream("/Type /EmbeddedFile", []byte("Attached note\n"))},
		{12, []byte("<< /FT /Sig /T (Signature1) /V 13 0 R /Rect [0 0 0 0] /P 3 0 R >>")},
		{13, []byte("<< /Type /Sig /Filter /Adobe.PPKLite /SubFilter /adbe.pkcs7.detached /ByteRange [0 10 20 30] /Contents <00> >>")},
		{14, []byte("<< /Type /Annot /Subtype /Text /Rect [100 700 120 720] /Contents (Reviewer comment) /P 3 0 R >>")},
	}
	out, _ := pdfRevision(nil, objs, 6, -1)
	return out
}
