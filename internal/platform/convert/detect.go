package convert

import (
	"archive/zip"
	"bytes"
	"strings"
)

// pngSignature is the 8-byte PNG file header.
const pngSignature = "\x89PNG\r\n\x1a\n"

// maxFtypBytes bounds the brand list read from the ftyp box.
const maxFtypBytes = 256

// DetectType finds the file type by magic bytes. The name and the declared MIME
// type are never trusted: ok is false for anything but the supported types.
func DetectType(data []byte) (FileType, bool) {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return FileJPEG, true
	case bytes.HasPrefix(data, []byte(pngSignature)):
		return FilePNG, true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return FileWebP, true
	case isHEIF(data):
		return FileHEIC, true
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return FilePDF, true
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return detectOffice(data)
	}
	return "", false
}

// isHEIF reads the brands of the leading ftyp box. AVIF shares the container and
// the generic brands, so its own brands veto the match.
func isHEIF(data []byte) bool {
	if len(data) < 16 || string(data[4:8]) != "ftyp" {
		return false
	}
	size := int(data[0])<<24 | int(data[1])<<16 | int(data[2])<<8 | int(data[3])
	if size < 16 || size > maxFtypBytes || size > len(data) {
		return false
	}
	// Major brand, then (after the minor version) the compatible brands.
	found := isHEIFBrand(data[8:12])
	for i := 16; i+4 <= size; i += 4 {
		found = found || isHEIFBrand(data[i:i+4])
	}
	for i := 8; i+4 <= size; i += 4 {
		if i == 12 {
			continue // minor version, not a brand
		}
		if b := string(data[i : i+4]); b == "avif" || b == "avis" {
			return false
		}
	}
	return found
}

func isHEIFBrand(b []byte) bool {
	switch string(b) {
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1", "heif":
		return true
	}
	return false
}

// detectOffice recognises an OOXML package by its directory layout. Only the
// zip's central directory is read.
func detectOffice(data []byte) (FileType, bool) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", false
	}
	hasTypes := false
	var t FileType
	for _, f := range zr.File {
		switch {
		case f.Name == "[Content_Types].xml":
			hasTypes = true
		case strings.HasPrefix(f.Name, "word/"):
			t = FileDOCX
		case strings.HasPrefix(f.Name, "xl/"):
			t = FileXLSX
		case strings.HasPrefix(f.Name, "ppt/"):
			t = FilePPTX
		}
	}
	return t, hasTypes && t != ""
}
