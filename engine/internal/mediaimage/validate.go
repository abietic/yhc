package mediaimage

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/webp"
)

const maxPixels = 25_000_000

// Info is strict, decoded raster metadata.
type Info struct {
	MIMEType string
	Width    int
	Height   int
}

// Inspect applies the same complete-content checks at initial admission and
// durable replay. The returned reason is stable and contains no input bytes.
func Inspect(data []byte, declaredMIME string) (Info, string) {
	detectedMIME := detectMIME(data)
	if detectedMIME == "" {
		return Info{}, "invalid_image"
	}
	if detectedMIME != declaredMIME {
		return Info{}, "mime_type_mismatch"
	}
	if reason := validateTerminal(data, detectedMIME); reason != "" {
		return Info{}, reason
	}

	config, err := decodeConfig(data, detectedMIME)
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return Info{}, "invalid_image"
	}
	width := uint64(config.Width)
	height := uint64(config.Height)
	if width > maxPixels ||
		height > maxPixels ||
		width > uint64(maxPixels)/height {
		return Info{}, "image_too_many_pixels"
	}
	if reason := decodeComplete(data, detectedMIME); reason != "" {
		return Info{}, reason
	}
	return Info{
		MIMEType: detectedMIME,
		Width:    config.Width,
		Height:   config.Height,
	}, ""
}

func detectMIME(data []byte) string {
	switch {
	case len(data) >= 8 &&
		bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(data) >= 3 &&
		data[0] == 0xff &&
		data[1] == 0xd8 &&
		data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 6 &&
		(bytes.Equal(data[:6], []byte("GIF87a")) ||
			bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif"
	case len(data) >= 12 &&
		bytes.Equal(data[:4], []byte("RIFF")) &&
		bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return ""
	}
}

func validateTerminal(data []byte, mediaType string) string {
	switch mediaType {
	case "image/png":
		return validatePNGTerminal(data)
	case "image/jpeg":
		return validateJPEGTerminal(data)
	case "image/webp":
		return validateWebPTerminal(data)
	default:
		return ""
	}
}

func validatePNGTerminal(data []byte) string {
	const pngSignatureBytes = 8
	offset := pngSignatureBytes
	for {
		if offset+12 > len(data) {
			return "invalid_image"
		}
		length := uint64(binary.BigEndian.Uint32(data[offset : offset+4]))
		chunkEnd := uint64(offset) + 12 + length
		if chunkEnd > uint64(len(data)) {
			return "invalid_image"
		}
		chunkType := string(data[offset+4 : offset+8])
		offset = int(chunkEnd)
		if chunkType != "IEND" {
			continue
		}
		if length != 0 {
			return "invalid_image"
		}
		if offset != len(data) {
			return "trailing_payload"
		}
		return ""
	}
}

func validateJPEGTerminal(data []byte) string {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return "invalid_image"
	}
	offset := 2
	inScan := false
	for {
		var marker byte
		if inScan {
			found, nextOffset, ok := nextJPEGScanMarker(data, offset)
			if !ok {
				return "invalid_image"
			}
			marker = found
			offset = nextOffset
			if marker >= 0xd0 && marker <= 0xd7 {
				continue
			}
			inScan = false
		} else {
			found, nextOffset, ok := nextJPEGMarker(data, offset)
			if !ok {
				return "invalid_image"
			}
			marker = found
			offset = nextOffset
		}

		switch {
		case marker == 0xd9:
			if offset != len(data) {
				return "trailing_payload"
			}
			return ""
		case marker == 0xd8 || marker == 0x00:
			return "invalid_image"
		case marker == 0x01:
			continue
		case marker >= 0xd0 && marker <= 0xd7:
			continue
		}

		if offset+2 > len(data) {
			return "invalid_image"
		}
		segmentLength := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if segmentLength < 2 || segmentLength > len(data)-offset {
			return "invalid_image"
		}
		offset += segmentLength
		if marker == 0xda {
			inScan = true
		}
	}
}

func nextJPEGMarker(data []byte, offset int) (byte, int, bool) {
	if offset >= len(data) || data[offset] != 0xff {
		return 0, offset, false
	}
	for offset < len(data) && data[offset] == 0xff {
		offset++
	}
	if offset >= len(data) {
		return 0, offset, false
	}
	marker := data[offset]
	return marker, offset + 1, marker != 0
}

func nextJPEGScanMarker(data []byte, offset int) (byte, int, bool) {
	for offset < len(data) {
		if data[offset] != 0xff {
			offset++
			continue
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			return 0, offset, false
		}
		marker := data[offset]
		offset++
		if marker == 0x00 {
			continue
		}
		return marker, offset, true
	}
	return 0, offset, false
}

func validateWebPTerminal(data []byte) string {
	if len(data) < 12 {
		return "invalid_image"
	}
	declaredLength := uint64(binary.LittleEndian.Uint32(data[4:8])) + 8
	if declaredLength < uint64(len(data)) {
		return "trailing_payload"
	}
	if declaredLength != uint64(len(data)) {
		return "invalid_image"
	}

	offset := 12
	for offset < len(data) {
		if offset+8 > len(data) {
			return "invalid_image"
		}
		chunkType := string(data[offset : offset+4])
		chunkLength := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		chunkEnd := uint64(offset) + 8 + chunkLength
		if chunkEnd > uint64(len(data)) {
			return "invalid_image"
		}
		if chunkType == "ANIM" || chunkType == "ANMF" ||
			(chunkType == "VP8X" &&
				chunkLength > 0 &&
				data[offset+8]&0x02 != 0) {
			return "animated_image_unsupported"
		}
		if chunkLength%2 != 0 {
			chunkEnd++
		}
		if chunkEnd > uint64(len(data)) {
			return "invalid_image"
		}
		offset = int(chunkEnd)
	}
	if offset != len(data) {
		return "invalid_image"
	}
	return ""
}

func decodeConfig(data []byte, mediaType string) (image.Config, error) {
	reader := bytes.NewReader(data)
	switch mediaType {
	case "image/png":
		return png.DecodeConfig(reader)
	case "image/jpeg":
		return jpeg.DecodeConfig(reader)
	case "image/gif":
		return gif.DecodeConfig(reader)
	case "image/webp":
		return webp.DecodeConfig(reader)
	default:
		return image.Config{}, image.ErrFormat
	}
}

func decodeComplete(data []byte, mediaType string) string {
	if mediaType == "image/gif" {
		reader := bytes.NewBuffer(data)
		decoded, err := gif.DecodeAll(reader)
		if err != nil {
			return "invalid_image"
		}
		if reader.Len() != 0 {
			return "trailing_payload"
		}
		if len(decoded.Image) != 1 {
			return "animated_image_unsupported"
		}
		return ""
	}

	reader := bytes.NewReader(data)
	var err error
	switch mediaType {
	case "image/png":
		_, err = png.Decode(reader)
	case "image/jpeg":
		_, err = jpeg.Decode(reader)
	case "image/webp":
		_, err = webp.Decode(reader)
	default:
		return "unsupported_mime_type"
	}
	if err != nil {
		return "invalid_image"
	}
	return ""
}
