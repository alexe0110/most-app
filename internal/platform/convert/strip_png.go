package convert

import (
	"encoding/binary"
	"hash/crc32"
)

// stripPNG drops the EXIF, text and time chunks and copies every other chunk
// as it is, CRC included. Like for JPEG, only a non-trivial orientation survives,
// as a minimal eXIf chunk right after IHDR.
func stripPNG(data []byte) ([]byte, error) {
	chunks, err := pngChunks(data)
	if err != nil {
		return nil, unreadable(err)
	}
	orientation := 0
	for _, c := range chunks {
		if c.kind == "eXIf" && orientation == 0 {
			orientation = keptOrientation(data[c.start+8 : c.end-4])
		}
	}

	out := make([]byte, 0, len(data))
	out = append(out, data[:len(pngSignature)]...)
	for _, c := range chunks {
		switch c.kind {
		case "eXIf", "tEXt", "zTXt", "iTXt", "tIME":
			continue
		}
		out = append(out, data[c.start:c.end]...)
		if c.kind == "IHDR" && orientation > 0 {
			out = append(out, pngChunk("eXIf", orientationTIFF(orientation))...)
		}
	}
	return out, nil
}

// pngChunk builds a chunk of the given type.
func pngChunk(kind string, body []byte) []byte {
	c := make([]byte, 0, 12+len(body))
	c = binary.BigEndian.AppendUint32(c, uint32(len(body)))
	c = append(c, kind...)
	c = append(c, body...)
	return binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(c[4:]))
}
