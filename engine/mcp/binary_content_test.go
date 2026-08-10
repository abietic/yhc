package mcp

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

// =============================================================================
// EncodeBinaryContent Tests
// =============================================================================

func TestEncodeBinaryContent_Basic(t *testing.T) {
	data := []byte("hello world")
	bc, err := EncodeBinaryContent(data, "text/plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if bc.MIMEType != "text/plain" {
		t.Fatalf("expected MIME type %q, got %q", "text/plain", bc.MIMEType)
	}
	if string(bc.Data) != "hello world" {
		t.Fatalf("expected data preserved, got %q", string(bc.Data))
	}

	// Verify base64 encoding.
	decoded, err := base64.StdEncoding.DecodeString(bc.Base64)
	if err != nil {
		t.Fatalf("failed to decode base64: %v", err)
		return
	}
	if string(decoded) != "hello world" {
		t.Fatalf("decoded data mismatch: %q", string(decoded))
	}
}

func TestEncodeBinaryContent_AutoDetectPNG(t *testing.T) {
	// PNG magic bytes.
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	bc, err := EncodeBinaryContent(pngData, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if bc.MIMEType != "image/png" {
		t.Fatalf("expected auto-detected MIME type %q, got %q", "image/png", bc.MIMEType)
	}
}

func TestEncodeBinaryContent_AutoDetectJPEG(t *testing.T) {
	// JPEG magic bytes.
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	bc, err := EncodeBinaryContent(jpegData, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if bc.MIMEType != "image/jpeg" {
		t.Fatalf("expected auto-detected MIME type %q, got %q", "image/jpeg", bc.MIMEType)
	}
}

func TestEncodeBinaryContent_AutoDetectGIF(t *testing.T) {
	// GIF magic bytes.
	gifData := []byte{'G', 'I', 'F', '8', '9', 'a', 0x00, 0x00}
	bc, err := EncodeBinaryContent(gifData, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if bc.MIMEType != "image/gif" {
		t.Fatalf("expected auto-detected MIME type %q, got %q", "image/gif", bc.MIMEType)
	}
}

func TestEncodeBinaryContent_AutoDetectWebP(t *testing.T) {
	// WebP magic bytes: RIFF....WEBP
	webpData := []byte{'R', 'I', 'F', 'F', 0x00, 0x00, 0x00, 0x00, 'W', 'E', 'B', 'P', 0x00}
	bc, err := EncodeBinaryContent(webpData, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if bc.MIMEType != "image/webp" {
		t.Fatalf("expected auto-detected MIME type %q, got %q", "image/webp", bc.MIMEType)
	}
}

func TestEncodeBinaryContent_AutoDetectPDF(t *testing.T) {
	pdfData := []byte("%PDF-1.4 some content")
	bc, err := EncodeBinaryContent(pdfData, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if bc.MIMEType != "application/pdf" {
		t.Fatalf("expected auto-detected MIME type %q, got %q", "application/pdf", bc.MIMEType)
	}
}

func TestEncodeBinaryContent_ExceedsSizeLimit(t *testing.T) {
	_ = os.Setenv(envMaxBinarySize, "100")
	defer os.Unsetenv(envMaxBinarySize) //nolint:errcheck

	data := make([]byte, 200)
	_, err := EncodeBinaryContent(data, "application/octet-stream")
	if err == nil {
		t.Fatal("expected error for oversized content")
		return
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("expected 'exceeds maximum' error, got: %v", err)
	}
}

func TestEncodeBinaryContent_EmptyData(t *testing.T) {
	bc, err := EncodeBinaryContent([]byte{}, "application/octet-stream")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if bc.Base64 != "" {
		t.Fatalf("expected empty base64, got %q", bc.Base64)
	}
}

func TestEncodeBinaryContent_ExplicitMIMEType(t *testing.T) {
	// Even with PNG magic bytes, explicit MIME type should be used.
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	bc, err := EncodeBinaryContent(pngData, "application/custom")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if bc.MIMEType != "application/custom" {
		t.Fatalf("expected explicit MIME type, got %q", bc.MIMEType)
	}
}

// =============================================================================
// DecodeBinaryContent Tests
// =============================================================================

func TestDecodeBinaryContent_Basic(t *testing.T) {
	original := []byte("test binary data")
	encoded := base64.StdEncoding.EncodeToString(original)

	bc, err := DecodeBinaryContent(encoded, "application/octet-stream")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if string(bc.Data) != "test binary data" {
		t.Fatalf("expected decoded data %q, got %q", "test binary data", string(bc.Data))
	}
	if bc.MIMEType != "application/octet-stream" {
		t.Fatalf("expected MIME type %q, got %q", "application/octet-stream", bc.MIMEType)
	}
	if bc.Base64 != encoded {
		t.Fatal("expected Base64 field to preserve original encoding")
	}
}

func TestDecodeBinaryContent_AutoDetect(t *testing.T) {
	// Encode PNG magic bytes.
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	encoded := base64.StdEncoding.EncodeToString(pngData)

	bc, err := DecodeBinaryContent(encoded, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}
	if bc.MIMEType != "image/png" {
		t.Fatalf("expected auto-detected MIME %q, got %q", "image/png", bc.MIMEType)
	}
}

func TestDecodeBinaryContent_InvalidBase64(t *testing.T) {
	_, err := DecodeBinaryContent("not-valid-base64!!!", "text/plain")
	if err == nil {
		t.Fatal("expected error for invalid base64")
		return
	}
	if !strings.Contains(err.Error(), "failed to decode") {
		t.Fatalf("expected decode error, got: %v", err)
	}
}

func TestDecodeBinaryContent_ExceedsSizeLimit(t *testing.T) {
	_ = os.Setenv(envMaxBinarySize, "50")
	defer os.Unsetenv(envMaxBinarySize) //nolint:errcheck

	data := make([]byte, 100)
	encoded := base64.StdEncoding.EncodeToString(data)

	_, err := DecodeBinaryContent(encoded, "application/octet-stream")
	if err == nil {
		t.Fatal("expected error for oversized decoded content")
		return
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("expected 'exceeds maximum' error, got: %v", err)
	}
}

// =============================================================================
// DetectContentType Tests
// =============================================================================

func TestDetectContentType_EmptyData(t *testing.T) {
	result := DetectContentType([]byte{})
	if result != "application/octet-stream" {
		t.Fatalf("expected application/octet-stream for empty data, got %q", result)
	}
}

func TestDetectContentType_UnknownBytes(t *testing.T) {
	// Random data that doesn't match any known signature.
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	result := DetectContentType(data)
	// Should fall through to http.DetectContentType.
	if result == "" {
		t.Fatal("expected non-empty content type")
	}
}

func TestDetectContentType_ShortData(t *testing.T) {
	// Less than 4 bytes - should not panic.
	result := DetectContentType([]byte{0xFF, 0xD8})
	if result == "" {
		t.Fatal("expected non-empty content type for short data")
	}
}

// =============================================================================
// IsImageMIMEType Tests
// =============================================================================

func TestIsImageMIMEType(t *testing.T) {
	tests := []struct {
		mime    string
		isImage bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/gif", true},
		{"image/webp", true},
		{"image/svg+xml", true},
		{"text/plain", false},
		{"application/json", false},
		{"application/pdf", false},
		{"", false},
	}

	for _, tt := range tests {
		result := IsImageMIMEType(tt.mime)
		if result != tt.isImage {
			t.Errorf("IsImageMIMEType(%q) = %v, want %v", tt.mime, result, tt.isImage)
		}
	}
}

// =============================================================================
// ContentBlockFromBinary Tests
// =============================================================================

func TestContentBlockFromBinary_Image(t *testing.T) {
	bc := &BinaryContent{
		Data:     []byte("fake png"),
		MIMEType: "image/png",
		Base64:   base64.StdEncoding.EncodeToString([]byte("fake png")),
	}

	block := ContentBlockFromBinary(bc)
	if block.Type != "image" {
		t.Fatalf("expected type 'image', got %q", block.Type)
	}
	if block.Text != bc.Base64 {
		t.Fatal("expected base64 in text field")
	}
}

func TestContentBlockFromBinary_NonImage(t *testing.T) {
	bc := &BinaryContent{
		Data:     []byte("some data"),
		MIMEType: "application/pdf",
		Base64:   base64.StdEncoding.EncodeToString([]byte("some data")),
	}

	block := ContentBlockFromBinary(bc)
	if block.Type != "resource" {
		t.Fatalf("expected type 'resource', got %q", block.Type)
	}
}

// =============================================================================
// BinaryContentFromBlock Tests
// =============================================================================

func TestBinaryContentFromBlock_Image(t *testing.T) {
	data := []byte("test data")
	encoded := base64.StdEncoding.EncodeToString(data)

	block := ContentBlock{Type: "image", Text: encoded}
	bc := BinaryContentFromBlock(block, "image/png")

	if bc == nil {
		t.Fatal("expected non-nil BinaryContent")
		return
	}
	if string(bc.Data) != "test data" {
		t.Fatalf("expected decoded data, got %q", string(bc.Data))
	}
	if bc.MIMEType != "image/png" {
		t.Fatalf("expected MIME %q, got %q", "image/png", bc.MIMEType)
	}
}

func TestBinaryContentFromBlock_TextType(t *testing.T) {
	block := ContentBlock{Type: "text", Text: "not binary"}
	bc := BinaryContentFromBlock(block, "")

	if bc != nil {
		t.Fatal("expected nil for text-type block")
		return
	}
}

func TestBinaryContentFromBlock_EmptyText(t *testing.T) {
	block := ContentBlock{Type: "image", Text: ""}
	bc := BinaryContentFromBlock(block, "")

	if bc != nil {
		t.Fatal("expected nil for empty text")
		return
	}
}

func TestBinaryContentFromBlock_InvalidBase64(t *testing.T) {
	block := ContentBlock{Type: "image", Text: "not-valid!!!"}
	bc := BinaryContentFromBlock(block, "")

	if bc != nil {
		t.Fatal("expected nil for invalid base64")
		return
	}
}

// =============================================================================
// ValidateBinarySize Tests
// =============================================================================

func TestValidateBinarySize_WithinLimit(t *testing.T) {
	err := ValidateBinarySize(100)
	if err != nil {
		t.Fatalf("unexpected error for small size: %v", err)
		return
	}
}

func TestValidateBinarySize_ExceedsLimit(t *testing.T) {
	_ = os.Setenv(envMaxBinarySize, "50")
	defer os.Unsetenv(envMaxBinarySize) //nolint:errcheck

	err := ValidateBinarySize(100)
	if err == nil {
		t.Fatal("expected error for oversized content")
		return
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("expected size error, got: %v", err)
	}
}

func TestValidateBinarySize_DefaultLimit(t *testing.T) {
	_ = os.Unsetenv(envMaxBinarySize)

	// Default is 10MB, so 5MB should be fine.
	err := ValidateBinarySize(5 * 1024 * 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
		return
	}

	// 11MB should exceed.
	err = ValidateBinarySize(11 * 1024 * 1024)
	if err == nil {
		t.Fatal("expected error for >10MB content")
		return
	}
}

// =============================================================================
// getMaxBinarySize Tests
// =============================================================================

func TestGetMaxBinarySize_Default(t *testing.T) {
	_ = os.Unsetenv(envMaxBinarySize)

	size := getMaxBinarySize()
	if size != defaultMaxBinarySize {
		t.Fatalf("expected %d, got %d", defaultMaxBinarySize, size)
	}
}

func TestGetMaxBinarySize_EnvOverride(t *testing.T) {
	_ = os.Setenv(envMaxBinarySize, "5242880")
	defer os.Unsetenv(envMaxBinarySize) //nolint:errcheck

	size := getMaxBinarySize()
	if size != 5242880 {
		t.Fatalf("expected 5242880, got %d", size)
	}
}

func TestGetMaxBinarySize_InvalidEnv(t *testing.T) {
	_ = os.Setenv(envMaxBinarySize, "invalid")
	defer os.Unsetenv(envMaxBinarySize) //nolint:errcheck

	size := getMaxBinarySize()
	if size != defaultMaxBinarySize {
		t.Fatalf("expected default for invalid env, got %d", size)
	}
}

// =============================================================================
// Round-trip encode/decode Tests
// =============================================================================

func TestBinaryContent_RoundTrip(t *testing.T) {
	original := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	}

	// Encode.
	encoded, err := EncodeBinaryContent(original, "image/png")
	if err != nil {
		t.Fatalf("encode failed: %v", err)
		return
	}

	// Convert to content block.
	block := ContentBlockFromBinary(encoded)
	if block.Type != "image" {
		t.Fatalf("expected image type, got %q", block.Type)
	}

	// Decode back.
	decoded, err := DecodeBinaryContent(block.Text, "image/png")
	if err != nil {
		t.Fatalf("decode failed: %v", err)
		return
	}

	// Verify data integrity.
	if len(decoded.Data) != len(original) {
		t.Fatalf("data length mismatch: got %d, want %d", len(decoded.Data), len(original))
	}
	for i := range original {
		if decoded.Data[i] != original[i] {
			t.Fatalf("data mismatch at byte %d: got %02x, want %02x", i, decoded.Data[i], original[i])
		}
	}
}
