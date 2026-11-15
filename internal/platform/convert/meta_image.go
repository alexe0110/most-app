package convert

import (
	"bytes"
	"context"
	"math"
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
	opts := imagemeta.Options{
		R:            bytes.NewReader(data),
		ImageFormat:  imageMetaFormat(info.Type),
		Sources:      imagemeta.EXIF | imagemeta.XMP | imagemeta.IPTC,
		LimitNumTags: maxMetaTags,
		LimitTagSize: maxMetaTagBytes,
		HandleTag: func(ti imagemeta.TagInfo) error {
			tags.Add(ti)
			return nil
		},
	}
	// The reader has no context; its own timeout stops it when the caller's
	// deadline does.
	if dl, ok := ctx.Deadline(); ok {
		opts.Timeout = max(time.Until(dl), time.Millisecond)
	}
	if _, err := imagemeta.Decode(opts); err != nil {
		return unreadable(err)
	}

	exif, xmp, iptc := tags.EXIF(), tags.XMP(), tags.IPTC()
	info.Capture.Make = tagText(exif, "Make")
	info.Capture.Model = tagText(exif, "Model")
	info.Capture.Software = firstNonEmpty(tagText(exif, "Software"), tagText(xmp, "CreatorTool"))
	info.People.Author = firstNonEmpty(tagText(exif, "Artist"), tagText(xmp, "Creator"))
	// An unparseable date is just not shown.
	if t, err := tags.GetDateTime(); err == nil {
		info.Capture.Taken = t
	}
	if lat, lon, err := tags.GetLatLong(); err == nil && validPlace(lat, lon) {
		info.Location = &GeoPoint{Lat: lat, Lon: lon}
	}

	info.Blocks = imageBlocks(exif, xmp, iptc)
	extra, err := containerBlock(data, info.Type)
	if err != nil {
		return err
	}
	info.Blocks = append(info.Blocks, extra...)
	return nil
}

// containerBlock finds the metadata that sits in the container and not in EXIF,
// XMP or IPTC: a JPEG comment, PNG text chunks. It also rejects a damaged
// container, which the metadata reader tolerates.
func containerBlock(data []byte, t FileType) ([]MetaBlock, error) {
	switch t {
	case FileJPEG:
		segs, _, err := jpegSegments(data)
		if err != nil {
			return nil, unreadable(err)
		}
		for _, s := range segs {
			if s.marker == jpegCOM {
				return []MetaBlock{BlockComment}, nil
			}
		}
	case FilePNG:
		chunks, err := pngChunks(data)
		if err != nil {
			return nil, unreadable(err)
		}
		for _, c := range chunks {
			if isPNGText(c) {
				return []MetaBlock{BlockText}, nil
			}
		}
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
