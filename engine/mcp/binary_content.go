package mcp

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Default and max binary content size limits.
const (
	// defaultMaxBinarySize is the default maximum binary content size (10MB).
	defaultMaxBinarySize = 10 * 1024 * 1024
	// envMaxBinarySize is the environment variable to override the default max.
	envMaxBinarySize = "MCP_MAX_BINARY_SIZE"
)

// BinaryContent represents binary data with its content type and encoding.
type BinaryContent struct {
	// Data is the raw binary data.
	Data []byte `json:"data"`
	// MIMEType is the content type (e.g., "image/png", "application/octet-stream").
	MIMEType string `json:"mimeType"`
	// Base64 is the base64-encoded representation of Data.
	Base64 string `json:"base64,omitempty"`
}

// getMaxBinarySize returns the configured maximum binary content size.
func getMaxBinarySize() int {
	if envVal := os.Getenv(envMaxBinarySize); envVal != "" {
		if size, err := strconv.Atoi(envVal); err == nil && size > 0 {
			return size
		}
	}
	return defaultMaxBinarySize
}

// EncodeBinaryContent encodes raw binary data into a BinaryContent structure
// with base64 encoding. The content type is auto-detected if not provided.
// Returns an error if the data exceeds the configured size limit.
func EncodeBinaryContent(data []byte, mimeType string) (*BinaryContent, error) {
	maxSize := getMaxBinarySize()
	if len(data) > maxSize {
		return nil, fmt.Errorf("mcp: binary content size %d exceeds maximum %d bytes", len(data), maxSize)
	}

	if mimeType == "" {
		mimeType = DetectContentType(data)
	}

	return &BinaryContent{
		Data:     data,
		MIMEType: mimeType,
		Base64:   base64.StdEncoding.EncodeToString(data),
	}, nil
}

// DecodeBinaryContent decodes a base64-encoded string into a BinaryContent structure.
// Returns an error if decoding fails or the decoded data exceeds size limits.
func DecodeBinaryContent(encoded, mimeType string) (*BinaryContent, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("mcp: failed to decode base64 content: %w", err)
	}

	maxSize := getMaxBinarySize()
	if len(data) > maxSize {
		return nil, fmt.Errorf("mcp: decoded binary content size %d exceeds maximum %d bytes", len(data), maxSize)
	}

	if mimeType == "" {
		mimeType = DetectContentType(data)
	}

	return &BinaryContent{
		Data:     data,
		MIMEType: mimeType,
		Base64:   encoded,
	}, nil
}

// DetectContentType uses magic bytes to determine the content type of binary data.
// Falls back to "application/octet-stream" if no known signature is found.
func DetectContentType(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}

	// Check well-known magic bytes first for common image types.
	if mimeType := detectByMagicBytes(data); mimeType != "" {
		return mimeType
	}

	// Fall back to Go's http.DetectContentType which inspects the first 512 bytes.
	detected := http.DetectContentType(data)
	return detected
}

// detectByMagicBytes checks the initial bytes of data against well-known signatures.
func detectByMagicBytes(data []byte) string {
	if len(data) < 4 {
		return ""
	}

	// PNG: 89 50 4E 47 0D 0A 1A 0A
	if len(data) >= 8 &&
		data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 &&
		data[4] == 0x0D && data[5] == 0x0A && data[6] == 0x1A && data[7] == 0x0A {
		return "image/png"
	}

	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}

	// GIF: GIF87a or GIF89a
	if data[0] == 'G' && data[1] == 'I' && data[2] == 'F' && data[3] == '8' {
		return "image/gif"
	}

	// WebP: RIFF....WEBP
	if len(data) >= 12 &&
		data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' &&
		data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
		return "image/webp"
	}

	// PDF: %PDF
	if data[0] == '%' && data[1] == 'P' && data[2] == 'D' && data[3] == 'F' {
		return "application/pdf"
	}

	return ""
}

// IsImageMIMEType returns true if the MIME type represents an image format.
func IsImageMIMEType(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/")
}

// ContentBlockFromBinary creates a ContentBlock from BinaryContent.
// For image types, the block type is "image"; otherwise it is "resource".
func ContentBlockFromBinary(bc *BinaryContent) ContentBlock {
	blockType := "resource"
	if IsImageMIMEType(bc.MIMEType) {
		blockType = "image"
	}
	return ContentBlock{
		Type: blockType,
		Text: bc.Base64,
	}
}

// BinaryContentFromBlock attempts to extract BinaryContent from a ContentBlock.
// Returns nil if the block does not contain base64-encoded binary data.
func BinaryContentFromBlock(block ContentBlock, mimeType string) *BinaryContent {
	if block.Type != "image" && block.Type != "resource" {
		return nil
	}
	if block.Text == "" {
		return nil
	}

	data, err := base64.StdEncoding.DecodeString(block.Text)
	if err != nil {
		return nil
	}

	if mimeType == "" {
		mimeType = DetectContentType(data)
	}

	return &BinaryContent{
		Data:     data,
		MIMEType: mimeType,
		Base64:   block.Text,
	}
}

// ValidateBinarySize checks if a binary payload is within size limits.
// Returns an error if the size exceeds the configured maximum.
func ValidateBinarySize(size int) error {
	maxSize := getMaxBinarySize()
	if size > maxSize {
		return fmt.Errorf("mcp: binary content size %d exceeds maximum %d bytes", size, maxSize)
	}
	return nil
}
