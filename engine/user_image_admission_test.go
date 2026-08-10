package engine

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

const (
	testUserImagePNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	// testUserImageWebPBase64 is golang.org/x/image's
	// gopher-doc.1bpp.lossless.webp decoder fixture encoded inline.
	testUserImageWebPBase64 = "UklGRrIBAABXRUJQVlA4TKUBAAAvSsAYAA8w//M///MfeJAkbXvaSG7m8Q3GfYSBJekwQztm/IcZlgwnmWImn2BK7aFmBtnVir6q//8VOkFE/xm4baTIu8c48ArEo6+B3zFKYln3pqClSCKX0begFTAXFOLXHSyF8cCNcZEG4OywuA4KVVfJCiArU7GAgJI8+lJP/OKMT/fBAjevg1cYB7YVkFuWga2lyPi5I0HFy5YTpWIHg0RZpkniRVW9odHAKOwosWuOGdxIyn2OvaCDvhg/we6TwadPBPbqBV58MsLmMJ8yZnOWk8SRz4N+QoyPL+MnamzMvcE1rHNEr91F9GKZPVUcS9w7PhhH36suB9qPeYb/oLk6cuTiJ0wOK3m5h1cKjW6EVZCYMK7dxcKCBdgP9HkKr9gkAO2P8GKZGWVdIAatQa+1IDpt6qyorVwdy01xdW8Jkfk6xjEXmVQQ+HQdFr6OKhIN34dXWq0+0qr6EJSCeeVLH9+gvGTLyqM65PQ44ihzlTXxQKjKbAvshXgir7Lil9w4L2bvMycmjQcqXaMCO6BlY28i+FOLzbfI1vEqxAhotocAAA=="
)

func TestUserImageAdmissionAcceptsSupportedFormats(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mimeType string
		data     []byte
	}{
		{name: "png", mimeType: " IMAGE/PNG ", data: decodeTestBase64(t, testUserImagePNGBase64)},
		{name: "jpeg", mimeType: "image/jpeg", data: encodeTestRaster(t, "image/jpeg")},
		{name: "webp", mimeType: "image/webp", data: decodeTestBase64(t, testUserImageWebPBase64)},
		{name: "single frame gif", mimeType: "image/gif", data: encodeTestRaster(t, "image/gif")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateUserImages([]UserImage{{
				Name:       "private-name",
				Path:       "/private/path",
				MIMEType:   tc.mimeType,
				Base64Data: base64.StdEncoding.EncodeToString(tc.data),
			}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUserImageAdmissionRejectsUnsafeInput(t *testing.T) {
	pngData := decodeTestBase64(t, testUserImagePNGBase64)
	jpegData := encodeTestRaster(t, "image/jpeg")
	gifData := encodeTestRaster(t, "image/gif")
	webpData := decodeTestBase64(t, testUserImageWebPBase64)

	for _, tc := range []struct {
		name     string
		mimeType string
		data     []byte
		encoded  string
		code     string
	}{
		{name: "missing data", mimeType: "image/png", code: "missing_base64_data"},
		{name: "missing MIME", data: pngData, code: "missing_mime_type"},
		{name: "invalid base64", mimeType: "image/png", encoded: "%%%private", code: "invalid_base64_data"},
		{name: "base64 whitespace", mimeType: "image/png", encoded: testUserImagePNGBase64 + "\n", code: "invalid_base64_data"},
		{name: "MIME parameters", mimeType: "image/png; charset=binary", data: pngData, code: "invalid_mime_type"},
		{name: "unsupported MIME", mimeType: "image/svg+xml", data: []byte("<svg/>"), code: "unsupported_mime_type"},
		{name: "HEIC MIME", mimeType: "image/heic", data: []byte("heic"), code: "unsupported_mime_type"},
		{name: "TIFF MIME", mimeType: "image/tiff", data: []byte("tiff"), code: "unsupported_mime_type"},
		{name: "MIME mismatch", mimeType: "image/jpeg", data: pngData, code: "mime_type_mismatch"},
		{name: "unknown image", mimeType: "image/png", data: []byte("not an image"), code: "invalid_image"},
		{name: "truncated PNG", mimeType: "image/png", data: pngData[:len(pngData)-5], code: "invalid_image"},
		{name: "PNG trailing payload", mimeType: "image/png", data: append(append([]byte(nil), pngData...), []byte("secret")...), code: "trailing_payload"},
		{name: "JPEG trailing payload", mimeType: "image/jpeg", data: append(append([]byte(nil), jpegData...), []byte("secret")...), code: "trailing_payload"},
		{name: "GIF trailing payload", mimeType: "image/gif", data: append(append([]byte(nil), gifData...), []byte("secret")...), code: "trailing_payload"},
		{name: "WebP trailing payload", mimeType: "image/webp", data: append(append([]byte(nil), webpData...), []byte("secret")...), code: "trailing_payload"},
		{name: "animated GIF", mimeType: "image/gif", data: encodeAnimatedTestGIF(t), code: "animated_image_unsupported"},
		{name: "animated WebP", mimeType: "image/webp", data: animatedTestWebP(), code: "animated_image_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded := tc.encoded
			if encoded == "" && tc.data != nil {
				encoded = base64.StdEncoding.EncodeToString(tc.data)
			}
			err := validateUserImages([]UserImage{{
				Name:       "name-secret",
				Path:       "/path-secret",
				MIMEType:   tc.mimeType,
				Base64Data: encoded,
			}})
			requireUserImageValidationError(t, err, 0, tc.code)
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("validation error leaked provenance or content: %v", err)
			}
		})
	}
}

func TestUserImageAdmissionEnforcesResourceBounds(t *testing.T) {
	valid := UserImage{
		MIMEType:   "image/png",
		Base64Data: testUserImagePNGBase64,
	}
	tooMany := make([]UserImage, maxUserImagesPerPrompt+1)
	for index := range tooMany {
		tooMany[index] = valid
	}
	requireUserImageValidationError(
		t,
		validateUserImages(tooMany),
		maxUserImagesPerPrompt,
		"too_many_images",
	)

	tooLarge := base64.StdEncoding.EncodeToString(
		make([]byte, maxUserImageBytes+1),
	)
	requireUserImageValidationError(
		t,
		validateUserImages([]UserImage{{
			MIMEType:   "image/png",
			Base64Data: tooLarge,
		}}),
		0,
		"image_too_large",
	)

	paddedGIF := gifWithCommentPadding(
		t,
		maxUserImageBytesPerPrompt/3+1024,
	)
	paddedBase64 := base64.StdEncoding.EncodeToString(paddedGIF)
	requireUserImageValidationError(
		t,
		validateUserImages([]UserImage{
			{MIMEType: "image/gif", Base64Data: paddedBase64},
			{MIMEType: "image/gif", Base64Data: paddedBase64},
			{MIMEType: "image/gif", Base64Data: paddedBase64},
		}),
		2,
		"prompt_images_too_large",
	)

	oversizedPixels := pngConfigFixture(maxUserImagePixels+1, 1)
	requireUserImageValidationError(
		t,
		validateUserImages([]UserImage{{
			MIMEType: "image/png",
			Base64Data: base64.StdEncoding.EncodeToString(
				oversizedPixels,
			),
		}}),
		0,
		"image_too_many_pixels",
	)
}

func requireUserImageValidationError(
	t *testing.T,
	err error,
	index int,
	code string,
) {
	t.Helper()
	var imageErr *UserImageValidationError
	if !errors.As(err, &imageErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if imageErr.ImageIndex != index || imageErr.ReasonCode != code {
		t.Fatalf("validation error = %#v", imageErr)
	}
}

func decodeTestBase64(t *testing.T, encoded string) []byte {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func encodeTestRaster(t *testing.T, mediaType string) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, 2, 2))
	source.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	source.Set(1, 0, color.RGBA{G: 0xff, A: 0xff})
	source.Set(0, 1, color.RGBA{B: 0xff, A: 0xff})
	source.Set(1, 1, color.RGBA{R: 0xff, G: 0xff, A: 0xff})

	var encoded bytes.Buffer
	var err error
	switch mediaType {
	case "image/png":
		err = png.Encode(&encoded, source)
	case "image/jpeg":
		err = jpeg.Encode(&encoded, source, nil)
	case "image/gif":
		err = gif.Encode(&encoded, source, nil)
	default:
		t.Fatalf("unsupported test media type %q", mediaType)
	}
	if err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func encodeAnimatedTestGIF(t *testing.T) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second.Pix[0] = 1
	var encoded bytes.Buffer
	err := gif.EncodeAll(&encoded, &gif.GIF{
		Image: []*image.Paletted{first, second},
		Delay: []int{0, 0},
		Config: image.Config{
			ColorModel: palette,
			Width:      1,
			Height:     1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func gifWithCommentPadding(t *testing.T, minimumBytes int) []byte {
	t.Helper()
	base := encodeTestRaster(t, "image/gif")
	if len(base) == 0 || base[len(base)-1] != 0x3b {
		t.Fatal("test GIF has no trailer")
	}
	padded := append([]byte(nil), base[:len(base)-1]...)
	padded = append(padded, 0x21, 0xfe)
	for len(padded)+2 < minimumBytes {
		length := min(255, minimumBytes-len(padded)-2)
		padded = append(padded, byte(length))
		padded = append(padded, make([]byte, length)...)
	}
	padded = append(padded, 0x00, 0x3b)
	return padded
}

func pngConfigFixture(width, height int) []byte {
	result := append([]byte(nil), []byte("\x89PNG\r\n\x1a\n")...)
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(width))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(height))
	ihdr[8] = 8
	ihdr[9] = 2
	result = appendPNGTestChunk(result, "IHDR", ihdr)
	result = appendPNGTestChunk(result, "IEND", nil)
	return result
}

func animatedTestWebP() []byte {
	result := []byte("RIFF")
	result = append(result, make([]byte, 4)...)
	result = append(result, []byte("WEBPVP8X")...)
	result = binary.LittleEndian.AppendUint32(result, 10)
	result = append(result, 0x02)
	result = append(result, make([]byte, 9)...)
	binary.LittleEndian.PutUint32(result[4:8], uint32(len(result)-8))
	return result
}

func appendPNGTestChunk(result []byte, chunkType string, payload []byte) []byte {
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(payload)))
	result = append(result, length...)
	result = append(result, chunkType...)
	result = append(result, payload...)
	checksum := crc32.ChecksumIEEE(append([]byte(chunkType), payload...))
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, checksum)
	return append(result, crc...)
}
