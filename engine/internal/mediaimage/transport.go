package mediaimage

import (
	"context"
	"encoding/binary"
)

// DeriveForTransport prepares a bounded automatic-delivery copy of a large
// PNG, without changing canonical bytes. The pixel budget matches DeepSeek's
// documented pre-model image scaling; wide, low-area images are not shortened
// merely because their long edge exceeds a thumbnail size. Explicit original
// detail must bypass this helper at the calling adapter.
//
// Opaque images use JPEG quality 85; transparency remains PNG. Small,
// animated, invalid, over-bound, or insufficiently smaller results keep the
// original encoding. A non-empty result is caller-owned and must be cleared.
func DeriveForTransport(ctx context.Context, data []byte, mimeType string, maxBytes int) (RecoveryDerivative, error) {
	if err := recoveryContextError(ctx); err != nil {
		return RecoveryDerivative{}, err
	}
	if mimeType != "image/png" || len(data) < 256*1024 || len(data) > maxBytes {
		return RecoveryDerivative{}, nil
	}
	if _, reason := Inspect(data, mimeType); reason != "" || pngHasAnimation(data) {
		return RecoveryDerivative{}, nil
	}
	result, err := deriveRaster(ctx, data, mimeType, maxBytes, derivativeProfile{
		maxLongEdge: 8192,
		maxPixels:   1_690_000,
		jpegQuality: 85,
	})
	if err != nil {
		return RecoveryDerivative{}, err
	}
	if len(result.Data) > len(data)/2 {
		clear(result.Data)
		return RecoveryDerivative{}, nil
	}
	return result, nil
}

// Inspect has already validated the bounded PNG chunk structure.
func pngHasAnimation(data []byte) bool {
	for offset := 8; offset+12 <= len(data); {
		length := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		if string(data[offset+4:offset+8]) == "acTL" {
			return true
		}
		offset += 12 + length
	}
	return false
}
