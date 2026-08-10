package mediaimage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const (
	recoveryMaxLongEdge = 2048
	recoveryMaxPixels   = 4_194_304
	recoveryJPEGQuality = 85
)

// RecoveryDerivative is one attempt-local Recovery Profile v1 result. Data is
// caller-owned and must be cleared after the provider attempt.
type RecoveryDerivative struct {
	Data     []byte
	MIMEType string
	Width    int
	Height   int
}

// DeriveForRecovery applies Recovery Profile v1 to one already-admitted
// canonical raster. An empty result is a valid "keep canonical bytes" outcome.
func DeriveForRecovery(
	ctx context.Context,
	data []byte,
	mimeType string,
	maxBytes int,
) (RecoveryDerivative, error) {
	if err := recoveryContextError(ctx); err != nil {
		return RecoveryDerivative{}, err
	}
	if maxBytes <= 0 {
		return RecoveryDerivative{}, errors.New("media recovery invalid byte limit")
	}
	info, reason := Inspect(data, mimeType)
	if reason != "" {
		return RecoveryDerivative{}, errors.New(
			"media recovery canonical image failed strict inspection",
		)
	}
	switch info.MIMEType {
	case "image/jpeg", "image/png", "image/webp":
		// Supported below.
	default:
		return RecoveryDerivative{}, nil
	}

	if err := recoveryContextError(ctx); err != nil {
		return RecoveryDerivative{}, err
	}
	decoded, err := decodeRecoveryImage(data, info.MIMEType)
	if err != nil {
		return RecoveryDerivative{}, errors.New("media recovery decode failed")
	}

	targetWidth, targetHeight := recoveryDimensions(
		decoded.Bounds().Dx(),
		decoded.Bounds().Dy(),
	)
	resampled := decoded
	if targetWidth != decoded.Bounds().Dx() ||
		targetHeight != decoded.Bounds().Dy() {
		if err := recoveryContextError(ctx); err != nil {
			return RecoveryDerivative{}, err
		}
		target := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))
		xdraw.CatmullRom.Scale(
			target,
			target.Bounds(),
			decoded,
			decoded.Bounds(),
			xdraw.Over,
			nil,
		)
		resampled = target
	}

	if err := recoveryContextError(ctx); err != nil {
		return RecoveryDerivative{}, err
	}
	alpha, err := recoveryHasAlpha(ctx, resampled)
	if err != nil {
		return RecoveryDerivative{}, err
	}
	if err := recoveryContextError(ctx); err != nil {
		return RecoveryDerivative{}, err
	}

	var encoded bytes.Buffer
	outputMIME := "image/jpeg"
	if alpha {
		outputMIME = "image/png"
		encoder := png.Encoder{CompressionLevel: png.BestCompression}
		err = encoder.Encode(&encoded, resampled)
	} else {
		err = jpeg.Encode(
			&encoded,
			resampled,
			&jpeg.Options{Quality: recoveryJPEGQuality},
		)
	}
	if err != nil {
		return RecoveryDerivative{}, errors.New("media recovery encode failed")
	}
	if err := recoveryContextError(ctx); err != nil {
		clear(encoded.Bytes())
		return RecoveryDerivative{}, err
	}

	output := encoded.Bytes()
	if len(output) == 0 || len(output) > maxBytes || len(output) >= len(data) {
		clear(output)
		return RecoveryDerivative{}, nil
	}
	derivedInfo, reason := Inspect(output, outputMIME)
	if reason != "" ||
		derivedInfo.Width != targetWidth ||
		derivedInfo.Height != targetHeight ||
		derivedInfo.Width > recoveryMaxLongEdge ||
		derivedInfo.Height > recoveryMaxLongEdge ||
		uint64(derivedInfo.Width)*uint64(derivedInfo.Height) >
			recoveryMaxPixels {
		clear(output)
		return RecoveryDerivative{}, errors.New(
			"media recovery derivative failed strict inspection",
		)
	}
	return RecoveryDerivative{
		Data:     output,
		MIMEType: outputMIME,
		Width:    derivedInfo.Width,
		Height:   derivedInfo.Height,
	}, nil
}

func decodeRecoveryImage(data []byte, mimeType string) (image.Image, error) {
	reader := bytes.NewReader(data)
	switch mimeType {
	case "image/jpeg":
		return jpeg.Decode(reader)
	case "image/png":
		return png.Decode(reader)
	case "image/webp":
		return webp.Decode(reader)
	default:
		return nil, fmt.Errorf("unsupported recovery image type")
	}
}

func recoveryDimensions(width, height int) (int, int) {
	if width <= 0 || height <= 0 {
		return 1, 1
	}
	longEdge := max(width, height)
	if longEdge <= recoveryMaxLongEdge &&
		uint64(width)*uint64(height) <= recoveryMaxPixels {
		return width, height
	}
	scale := float64(recoveryMaxLongEdge) / float64(longEdge)
	targetWidth := max(1, int(float64(width)*scale))
	targetHeight := max(1, int(float64(height)*scale))
	for uint64(targetWidth)*uint64(targetHeight) > recoveryMaxPixels {
		if targetWidth >= targetHeight {
			targetWidth--
		} else {
			targetHeight--
		}
	}
	return targetWidth, targetHeight
}

func recoveryHasAlpha(ctx context.Context, source image.Image) (bool, error) {
	if opaque, ok := source.(interface{ Opaque() bool }); ok {
		return !opaque.Opaque(), nil
	}
	bounds := source.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if err := recoveryContextError(ctx); err != nil {
			return false, err
		}
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, alpha := source.At(x, y).RGBA()
			if alpha != 0xffff {
				return true, nil
			}
		}
	}
	return false, nil
}

func recoveryContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("media recovery canceled: %w", err)
	}
	return nil
}
