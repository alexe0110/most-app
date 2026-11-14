package convert

import (
	"archive/zip"
	"bytes"
	"io"
)

// Minimal valid replacements for the properties parts.
const (
	emptyCoreXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" ` +
		`xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"/>`
	emptyAppXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" ` +
		`xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"/>`
	emptyCustomXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/custom-properties" ` +
		`xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"/>`
)

// stripOffice rewrites the properties parts of a DOCX, XLSX or PPTX as empty and
// copies every other entry raw, without unpacking or recompressing it. A part
// that the package doesn't have is not added: the content types and relations
// would have to follow.
func stripOffice(data []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, unreadable(err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		replacement, replace := officeBlank(f.Name)
		if !replace {
			if err := zw.Copy(f); err != nil {
				return nil, unreadable(err)
			}
			continue
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate})
		if err != nil {
			return nil, unreadable(err)
		}
		if _, err := io.WriteString(w, replacement); err != nil {
			return nil, unreadable(err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, unreadable(err)
	}
	return out.Bytes(), nil
}

// officeBlank returns the empty replacement for a properties part.
func officeBlank(name string) (string, bool) {
	switch name {
	case officeCorePart:
		return emptyCoreXML, true
	case officeAppPart:
		return emptyAppXML, true
	case officeCustomPart:
		return emptyCustomXML, true
	}
	return "", false
}
