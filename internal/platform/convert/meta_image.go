package convert

import (
	"bytes"
	"context"
	"math"
	"strings"
	"time"

	"github.com/bep/imagemeta"
)

// Limits for the metadata reader: a hostile file must not make it collect
// thousands of tags or huge values.
const (
	maxMetaTags     = 1000
	maxMetaTagBytes = 8192
)

// orientationName is the EXIF tag that Strip keeps, so it does not count as
// metadata.
const orientationName = "Orientation"

// imageMetaFormat maps a file type to the reader's format.
func imageMetaFormat(t FileType) imagemeta.ImageFormat {
	switch t {
	case FileJPEG:
		return imagemeta.JPEG
	case FilePNG:
		return imagemeta.PNG
	case FileWebP:
		return imagemeta.WebP
	default:
		return imagemeta.HEIF
	}
}

// inspectImage reads EXIF, XMP and IPTC of an image into info. The library only
// reads; cleaning is done by the strip_* files.
func inspectImage(ctx context.Context, data []byte, info *FileInfo) error {
	var tags imagemeta.Tags
	var found []imagemeta.TagInfo
	opts := imagemeta.Options{
		R:            bytes.NewReader(data),
		ImageFormat:  imageMetaFormat(info.Type),
		Sources:      imagemeta.EXIF | imagemeta.XMP | imagemeta.IPTC | imagemeta.CONFIG,
		LimitNumTags: maxMetaTags,
		LimitTagSize: maxMetaTagBytes,
		HandleTag: func(ti imagemeta.TagInfo) error {
			tags.Add(ti)
			found = append(found, ti)
			return nil
		},
	}
	// The reader has no context; its own timeout stops it when the caller's
	// deadline does.
	if dl, ok := ctx.Deadline(); ok {
		opts.Timeout = max(time.Until(dl), time.Millisecond)
	}
	res, err := imagemeta.Decode(opts)
	if err != nil {
		return unreadable(err)
	}

	fs := newFieldSet()
	addImageTags(fs, &tags, found)
	if c := res.ImageConfig; c.Width > 0 && c.Height > 0 {
		fs.add(GroupImage, "Image:Size", Dimensions{Width: c.Width, Height: c.Height}, false)
	}

	info.Blocks = imageBlocks(tags.EXIF(), tags.XMP(), tags.IPTC())
	extra, err := containerBlock(data, info.Type, fs)
	if err != nil {
		return err
	}
	info.Blocks = append(info.Blocks, extra...)
	info.Groups, info.Dropped = fs.result(), fs.dropped
	return nil
}

// addImageTags puts every tag into its group. Pairs that read as one fact are
// merged: the coordinates, the altitude with its sign, a local date with its
// zone.
func addImageTags(fs *fieldSet, tags *imagemeta.Tags, found []imagemeta.TagInfo) {
	exif := tags.EXIF()
	merged := map[string]bool{}
	// An unparseable place is shown tag by tag instead.
	if lat, lon, err := tags.GetLatLong(); err == nil && validPlace(lat, lon) {
		fs.add(GroupLocation, "GPS:Coordinates", Coordinates{Lat: lat, Lon: lon}, true)
		for _, n := range []string{"GPSLatitude", "GPSLongitude", "GPSLatitudeRef", "GPSLongitudeRef"} {
			merged[n] = true
		}
	}
	if alt, ok := exif["GPSAltitude"].Value.(interface{ Float64() float64 }); ok {
		v := alt.Float64()
		if ref, ok := normalize(exif["GPSAltitudeRef"].Value).(int64); ok && ref == 1 {
			v = -v
		}
		fs.add(GroupLocation, "EXIF:GPSAltitude", finite(v), true)
		merged["GPSAltitude"], merged["GPSAltitudeRef"] = true, true
	}
	for _, n := range []string{"GPSImgDirectionRef", "GPSDestBearingRef"} {
		merged[n] = true // the "true or magnetic north" flag has no use apart from the value
	}
	// A local date takes the zone from its OffsetTime tag; the tag is then
	// not shown on its own.
	for _, n := range []string{"DateTime", "ModifyDate", "DateTimeOriginal", "DateTimeDigitized", "CreateDate"} {
		if m, ok := parseMoment(tagText(exif, n), tagText(exif, offsetOf(n))); ok && m.HasZone {
			merged[offsetOf(n)] = true
		}
	}

	for _, ti := range found {
		if ti.Source == imagemeta.CONFIG || merged[ti.Tag] || isThumbnailPointer(ti.Tag) {
			continue
		}
		v := normalize(ti.Value)
		if s, ok := v.(string); ok && strings.HasPrefix(ti.Tag, "MakerNote") {
			v = Binary{Bytes: len(s)} // vendor data is not text even when it happens to decode
		}
		addValue(fs, sourcePrefix(ti.Source)+":"+ti.Tag, ti.Tag, v, tagText(exif, offsetOf(ti.Tag)))
	}
}

// containerBlock finds the metadata that sits in the container and not in EXIF,
// XMP or IPTC: a JPEG comment, PNG text chunks; their text goes into fs. It also
// rejects a damaged container, which the metadata reader tolerates.
func containerBlock(data []byte, t FileType, fs *fieldSet) ([]MetaBlock, error) {
	switch t {
	case FileJPEG:
		segs, _, err := jpegSegments(data)
		if err != nil {
			return nil, unreadable(err)
		}
		var blocks []MetaBlock
		for _, s := range segs {
			if s.marker != jpegCOM {
				continue
			}
			blocks = []MetaBlock{BlockComment}
			if s.end-s.start > 4 {
				fs.add(GroupOther, "JPEG:Comment", sanitizeText(string(data[s.start+4:s.end])), false)
			}
		}
		return blocks, nil
	case FilePNG:
		chunks, err := pngChunks(data)
		if err != nil {
			return nil, unreadable(err)
		}
		var blocks []MetaBlock
		for _, c := range chunks {
			if isPNGText(c) {
				blocks = []MetaBlock{BlockText}
				addPNGText(fs, data, c)
			}
		}
		return blocks, nil
	case FileWebP:
		if _, err := riffChunks(data); err != nil {
			return nil, unreadable(err)
		}
	case FileHEIC:
		if _, err := parseHEIC(data); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// addPNGText reads a text chunk (length, type, data, CRC): plain tEXt is shown,
// compressed or international text and the time stamp as what they are.
func addPNGText(fs *fieldSet, data []byte, c segment) {
	body := data[c.start+8 : c.end-4]
	switch c.kind {
	case "tEXt":
		keyword, text, _ := bytes.Cut(body, []byte{0})
		name := sanitizeText(string(keyword))
		g, sensitive := tagGroup(name)
		fs.add(g, "PNG:"+name, sanitizeText(string(text)), sensitive)
	case "tIME":
		if len(body) == 7 {
			t := time.Date(int(body[0])<<8|int(body[1]), time.Month(body[2]), int(body[3]), int(body[4]), int(body[5]), int(body[6]), 0, time.UTC)
			fs.add(GroupDates, "PNG:tIME", Moment{Time: t, HasZone: true}, false)
		}
	default:
		keyword, _, _ := bytes.Cut(body, []byte{0})
		fs.add(GroupOther, "PNG:"+sanitizeText(string(keyword)), Binary{Bytes: len(body)}, false)
	}
}

// imageBlocks lists the metadata groups an image really carries. The kept
// orientation tag alone does not make an EXIF block.
func imageBlocks(exif, xmp, iptc map[string]imagemeta.TagInfo) []MetaBlock {
	var blocks []MetaBlock
	for name := range exif {
		if name != orientationName {
			blocks = append(blocks, BlockEXIF)
			break
		}
	}
	if len(xmp) > 0 {
		blocks = append(blocks, BlockXMP)
	}
	if len(iptc) > 0 {
		blocks = append(blocks, BlockIPTC)
	}
	return blocks
}

// validPlace drops garbage and the (0, 0) of a camera with no fix.
func validPlace(lat, lon float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lon) || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return false
	}
	return lat != 0 || lon != 0
}

// tagText reads a string tag (or the first of a list) and cleans it for display.
func tagText(m map[string]imagemeta.TagInfo, name string) string {
	switch v := m[name].Value.(type) {
	case string:
		return sanitizeText(v)
	case []string:
		if len(v) > 0 {
			return sanitizeText(v[0])
		}
	}
	return ""
}
