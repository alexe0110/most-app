package convert

import (
	"encoding/binary"
	"image"
)

// orientationTag is the EXIF orientation tag.
const orientationTag = 0x0112

// exifOrientation reads the EXIF orientation (1..8) from a JPEG's APP1 segment.
// Anything unexpected returns 1 (upright) instead of an error.
func exifOrientation(data []byte) int {
	const upright = 1

	// JPEG starts with SOI (0xFFD8); scan segments for APP1 (0xFFE1).
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return upright
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return upright
		}
		marker := data[i+1]
		// Start of scan / end of image: no more metadata.
		if marker == 0xDA || marker == 0xD9 {
			return upright
		}
		segLen := int(data[i+2])<<8 | int(data[i+3])
		if segLen < 2 || i+2+segLen > len(data) {
			return upright
		}
		if marker == 0xE1 {
			if o, ok := orientationFromAPP1(data[i+4 : i+2+segLen]); ok {
				return o
			}
		}
		i += 2 + segLen
	}
	return upright
}

// orientationFromAPP1 reads the orientation tag from an APP1 body. ok is false
// if the segment is not EXIF/TIFF or has no valid orientation.
func orientationFromAPP1(seg []byte) (int, bool) {
	const exifHeader = "Exif\x00\x00"
	if len(seg) < len(exifHeader) || string(seg[:len(exifHeader)]) != exifHeader {
		return 0, false
	}
	tiff := seg[len(exifHeader):]
	if len(tiff) < 8 {
		return 0, false
	}

	var order binary.ByteOrder
	switch string(tiff[0:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, false
	}

	ifdOffset := int(order.Uint32(tiff[4:8]))
	if ifdOffset < 8 || ifdOffset+2 > len(tiff) {
		return 0, false
	}
	count := int(order.Uint16(tiff[ifdOffset:]))
	entries := tiff[ifdOffset+2:]
	for n := 0; n < count; n++ {
		off := n * 12
		if off+12 > len(entries) {
			return 0, false
		}
		if order.Uint16(entries[off:]) != orientationTag {
			continue
		}
		// A SHORT value sits in the first 2 bytes of the entry's value field.
		if v := int(order.Uint16(entries[off+8:])); v >= 1 && v <= 8 {
			return v, true
		}
		return 0, false
	}
	return 0, false
}

// applyOrientation bakes EXIF orientation o into the pixels. o=1 or out of
// range leaves img as is. Orientations 5..8 swap width and height.
func applyOrientation(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			nx, ny := mapOrientation(o, x, y, w, h)
			dst.Set(nx, ny, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// mapOrientation maps source pixel (x,y) of a w×h image to its new position
// for EXIF orientation o.
func mapOrientation(o, x, y, w, h int) (nx, ny int) {
	switch o {
	case 2: // mirror horizontally
		return w - 1 - x, y
	case 3: // rotate 180°
		return w - 1 - x, h - 1 - y
	case 4: // mirror vertically
		return x, h - 1 - y
	case 5: // transpose
		return y, x
	case 6: // rotate 90° clockwise
		return h - 1 - y, x
	case 7: // transverse
		return h - 1 - y, w - 1 - x
	case 8: // rotate 90° counter-clockwise
		return y, w - 1 - x
	default:
		return x, y
	}
}
