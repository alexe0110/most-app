package convert

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"time"
)

// Office package parts that hold the document properties.
const (
	officeCorePart   = "docProps/core.xml"
	officeAppPart    = "docProps/app.xml"
	officeCustomPart = "docProps/custom.xml"
)

// maxOfficePartBytes bounds one properties part read from a package: the zip's
// declared sizes are the stranger's word, so a bomb can't be left to Read.
const maxOfficePartBytes = 1 << 20

// coreProps is docProps/core.xml; elements match by local name.
type coreProps struct {
	Creator        string `xml:"creator"`
	LastModifiedBy string `xml:"lastModifiedBy"`
	Created        string `xml:"created"`
	Modified       string `xml:"modified"`
	Title          string `xml:"title"`
	Subject        string `xml:"subject"`
	Keywords       string `xml:"keywords"`
	Description    string `xml:"description"`
}

// appProps is docProps/app.xml.
type appProps struct {
	Company     string `xml:"Company"`
	Manager     string `xml:"Manager"`
	Application string `xml:"Application"`
}

// customProps is docProps/custom.xml: only whether it holds any property.
type customProps struct {
	Props []struct{} `xml:"property"`
}

// inspectOffice reads docProps of a DOCX, XLSX or PPTX.
func inspectOffice(data []byte, info *FileInfo) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return unreadable(err)
	}
	var core coreProps
	var app appProps
	var custom customProps
	for _, f := range zr.File {
		var dst any
		switch f.Name {
		case officeCorePart:
			dst = &core
		case officeAppPart:
			dst = &app
		case officeCustomPart:
			dst = &custom
		default:
			continue
		}
		if err := decodePart(f, dst); err != nil {
			return unreadable(err)
		}
	}

	info.People = People{
		Author:         sanitizeText(core.Creator),
		LastModifiedBy: sanitizeText(core.LastModifiedBy),
		Company:        sanitizeText(app.Company),
	}
	info.Dates = Dates{Created: officeTime(core.Created), Modified: officeTime(core.Modified)}
	info.Capture.Software = sanitizeText(app.Application)

	if core != (coreProps{}) || app != (appProps{}) || len(custom.Props) > 0 {
		info.Blocks = append(info.Blocks, BlockOfficeProps)
	}
	return nil
}

// decodePart parses one XML part, reading at most maxOfficePartBytes.
func decodePart(f *zip.File, dst any) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }() // read-only: a close error loses nothing

	dec := xml.NewDecoder(io.LimitReader(rc, maxOfficePartBytes))
	return dec.Decode(dst)
}

// officeTime parses a W3CDTF value; zero if absent or malformed.
func officeTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}
