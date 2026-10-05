package agenticdeepseek

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/abietic/yhc/engine/internal/mediaimage"
)

const maxTransportImageBytes = 5 * 1024 * 1024

// Work only on the already-validated wire request, never on caller messages,
// durable media or callback inputs. Repeated history images share one local
// derivation within this attempt; no remote Files resources or cache exist.
func optimizeResponseImages(ctx context.Context, request *responseRequest) error {
	cache := make(map[string]string)
	for itemIndex := range request.Input {
		item := &request.Input[itemIndex]
		for _, parts := range [][]contentPart{item.Content, item.Output} {
			for partIndex := range parts {
				part := &parts[partIndex]
				if part.Type != "input_image" || part.Detail != "" && part.Detail != "auto" {
					continue
				}
				const prefix = "data:image/png;base64,"
				if !strings.HasPrefix(part.ImageURL, prefix) {
					continue
				}
				if cached, ok := cache[part.ImageURL]; ok {
					part.ImageURL = cached
					continue
				}
				encoded := strings.TrimPrefix(part.ImageURL, prefix)
				if len(encoded) > base64.StdEncoding.EncodedLen(maxTransportImageBytes) || len(encoded) < base64.StdEncoding.EncodedLen(256*1024) {
					continue
				}
				data, err := base64.StdEncoding.Strict().DecodeString(encoded)
				if err != nil {
					continue
				}
				derived, err := mediaimage.DeriveForTransport(ctx, data, "image/png", maxTransportImageBytes)
				clear(data)
				if err != nil {
					if ctx.Err() != nil {
						return &transportError{err: ctx.Err()}
					}
					return conversionError(itemIndex, partIndex, "image_transport_preparation_failed")
				}
				optimized := part.ImageURL
				if len(derived.Data) > 0 {
					optimized = "data:" + derived.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(derived.Data)
					clear(derived.Data)
				}
				cache[part.ImageURL] = optimized
				part.ImageURL = optimized
			}
		}
	}
	return nil
}
