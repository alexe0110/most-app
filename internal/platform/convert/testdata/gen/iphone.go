//go:build ignore

package main

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
	"log"
)

// rat is one unsigned rational (numerator/denominator) of a TIFF tag.
type rat struct{ num, den uint32 }

func rationalPairs(id uint16, pairs ...rat) tag {
	var v []byte
	for _, p := range pairs {
		v = binary.BigEndian.AppendUint32(v, p.num)
		v = binary.BigEndian.AppendUint32(v, p.den)
	}
	return tag{id, 5, uint32(len(pairs)), v}
}

func bytesTag(id uint16, typ uint16, v []byte) tag {
	return tag{id, typ, uint32(len(v)), v}
}

// iphoneTIFF is a TIFF of a recent iPhone photo: camera and lens, shooting
// settings, the local time with its zone, a vendor MakerNote blob, and GPS
// with altitude and direction. Values are invented; the layout is the usual one.
func iphoneTIFF() []byte {
	makerNote := append([]byte("Apple iOS\x00\x00\x01MM\x00\x01"), bytes.Repeat([]byte{0x5A}, 180)...)
	ifd0 := func(exifAt, gpsAt int) []byte {
		return ifd(8, []tag{
			ascii(0x010F, "Apple"),
			ascii(0x0110, "iPhone 17"),
			short(0x0112, 1),
			ascii(0x0131, "26.6"),
			ascii(0x0132, "2026:10:31 15:49:00"),
			long(0x8769, uint32(exifAt)),
			long(0x8825, uint32(gpsAt)),
		})
	}
	exif := func(at int) []byte {
		return ifd(at, []tag{
			rationalPairs(0x829A, rat{1, 120}),
			rationalPairs(0x829D, rat{178, 100}),
			short(0x8827, 64),
			bytesTag(0x9000, 7, []byte("0232")),
			ascii(0x9003, "2026:10:31 15:49:00"),
			ascii(0x9004, "2026:10:31 15:49:00"),
			bytesTag(0x9101, 7, []byte{1, 2, 3, 0}),
			bytesTag(0x927C, 7, makerNote),
			ascii(0x9011, "+05:00"),
			rationalPairs(0x920A, rat{6765, 1000}),
			ascii(0xA433, "Apple"),
			ascii(0xA434, "iPhone 17 back camera 6.765mm f/1.78"),
			short(0xA001, 1),
		})
	}
	gps := func(at int) []byte {
		return ifd(at, []tag{
			bytesTag(0, 1, []byte{2, 3, 0, 0}),
			ascii(1, "N"),
			rationalPairs(2, rat{43, 1}, rat{15, 1}, rat{549, 10}),
			ascii(3, "E"),
			rationalPairs(4, rat{76, 1}, rat{58, 1}, rat{365, 10}),
			bytesTag(5, 1, []byte{0}),
			rationalPairs(6, rat{812, 1}),
			ascii(0x0010, "T"),
			rationalPairs(0x0011, rat{245, 1}),
			ascii(0x001D, "2026:10:31"),
			rationalPairs(0x0007, rat{10, 1}, rat{49, 1}, rat{0, 1}),
		})
	}
	exifAt := 8 + len(ifd0(0, 0))
	gpsAt := exifAt + len(exif(0))
	out := []byte{'M', 'M', 0, 42, 0, 0, 0, 8}
	out = append(out, ifd0(exifAt, gpsAt)...)
	out = append(out, exif(exifAt)...)
	return append(out, gps(gpsAt)...)
}

// iphoneJPEG is the photo of the iPhone fixture: iphoneTIFF as EXIF and an XMP
// packet with the content identifier Apple writes for Live Photos. The HEIC
// fixture is converted from it by sips (see README.md).
func iphoneJPEG() []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, picture(64, 48), &jpeg.Options{Quality: 80}); err != nil {
		log.Fatal(err)
	}
	b := buf.Bytes()
	xmp := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
		`<rdf:Description rdf:about="" xmlns:apple="http://ns.apple.com/apple/1.0/">` +
		`<apple:ContentIdentifier>A1B2C3D4-0000-4000-8000-0123456789AB</apple:ContentIdentifier>` +
		`</rdf:Description></rdf:RDF></x:xmpmeta>`)
	out := append([]byte{}, b[:2]...)
	out = append(out, segment(0xE1, append([]byte("Exif\x00\x00"), iphoneTIFF()...))...)
	out = append(out, segment(0xE1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), xmp...))...)
	return append(out, b[2:]...)
}
