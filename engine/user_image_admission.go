package engine

import (
	"encoding/base64"
	"mime"
	"strings"

	"github.com/abietic/yhc/engine/internal/mediaimage"
)

const (
	maxUserImagesPerPrompt     = 20
	maxUserImageBytes          = 5 * 1024 * 1024
	maxUserImageBytesPerPrompt = 10 * 1024 * 1024
	maxUserImagePixels         = 25_000_000
)

func validateUserImages(images []UserImage) error {
	if len(images) > maxUserImagesPerPrompt {
		return newUserImageValidationError(
			maxUserImagesPerPrompt,
			"too_many_images",
		)
	}

	totalBytes := 0
	for index, input := range images {
		if input.Base64Data == "" {
			return newUserImageValidationError(index, "missing_base64_data")
		}
		if strings.TrimSpace(input.MIMEType) == "" {
			return newUserImageValidationError(index, "missing_mime_type")
		}

		declaredMIME, reason := normalizedUserImageMIME(input.MIMEType)
		if reason != "" {
			return newUserImageValidationError(index, reason)
		}
		decoded, reason := decodeUserImageBase64(input.Base64Data)
		if reason != "" {
			return newUserImageValidationError(index, reason)
		}
		if len(decoded) > maxUserImageBytes {
			return newUserImageValidationError(index, "image_too_large")
		}
		if totalBytes > maxUserImageBytesPerPrompt-len(decoded) {
			return newUserImageValidationError(
				index,
				"prompt_images_too_large",
			)
		}
		totalBytes += len(decoded)

		if reason := validateDecodedUserImage(decoded, declaredMIME); reason != "" {
			return newUserImageValidationError(index, reason)
		}
	}
	return nil
}

func newUserImageValidationError(index int, reason string) error {
	return &UserImageValidationError{
		ImageIndex: index,
		ReasonCode: reason,
	}
}

func normalizedUserImageMIME(raw string) (string, string) {
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(raw))
	if err != nil || len(params) != 0 {
		return "", "invalid_mime_type"
	}
	mediaType = strings.ToLower(mediaType)
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return mediaType, ""
	default:
		return "", "unsupported_mime_type"
	}
}

func decodeUserImageBase64(encoded string) ([]byte, string) {
	if strings.ContainsAny(encoded, " \t\r\n") {
		return nil, "invalid_base64_data"
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxUserImageBytes) {
		return nil, "image_too_large"
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, "invalid_base64_data"
	}
	return decoded, ""
}

func validateDecodedUserImage(data []byte, declaredMIME string) string {
	_, reason := mediaimage.Inspect(data, declaredMIME)
	return reason
}
