package mediaimage

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"
)

func TestP303RecoveryDerivativeIsDeterministicStrictlySmaller(t *testing.T) {
	source := recoveryTestPNG(t, 1024, 1024, false)
	original := append([]byte(nil), source...)

	first, err := DeriveForRecovery(
		context.Background(),
		source,
		"image/png",
		5*1024*1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DeriveForRecovery(
		context.Background(),
		source,
		"image/png",
		5*1024*1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(first.Data)
	defer clear(second.Data)
	if len(first.Data) == 0 ||
		len(first.Data) >= len(source) ||
		first.MIMEType != "image/jpeg" ||
		first.Width != 1024 ||
		first.Height != 1024 {
		t.Fatalf("derivative = %#v, source bytes = %d", first, len(source))
	}
	if !bytes.Equal(first.Data, second.Data) {
		t.Fatal("Recovery Profile v1 output is not deterministic")
	}
	if !bytes.Equal(source, original) {
		t.Fatal("recovery mutated canonical source bytes")
	}
	if _, reason := Inspect(first.Data, first.MIMEType); reason != "" {
		t.Fatalf("derived image failed strict inspection: %s", reason)
	}
}

func TestP303RecoveryDerivativePreservesAlphaAndBounds(t *testing.T) {
	source := recoveryTestPNG(t, 2400, 400, true)
	derived, err := DeriveForRecovery(
		context.Background(),
		source,
		"image/png",
		5*1024*1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(derived.Data)
	if len(derived.Data) == 0 ||
		derived.MIMEType != "image/png" ||
		derived.Width != 2048 ||
		derived.Height != 341 {
		t.Fatalf("bounded alpha derivative = %#v", derived)
	}
	if uint64(derived.Width)*uint64(derived.Height) > recoveryMaxPixels {
		t.Fatalf("derived pixels exceed profile: %#v", derived)
	}
}

func TestP303RecoveryDerivativeRejectsIneligibleAndCanceledWork(t *testing.T) {
	var gifBuffer bytes.Buffer
	if err := gif.Encode(
		&gifBuffer,
		image.NewPaletted(
			image.Rect(0, 0, 1, 1),
			color.Palette{color.Black},
		),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	derived, err := DeriveForRecovery(
		context.Background(),
		gifBuffer.Bytes(),
		"image/gif",
		5*1024*1024,
	)
	if err != nil || len(derived.Data) != 0 {
		t.Fatalf("ineligible static GIF result = %#v, %v", derived, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = DeriveForRecovery(
		ctx,
		recoveryTestPNG(t, 32, 32, false),
		"image/png",
		5*1024*1024,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func recoveryTestPNG(
	t *testing.T,
	width int,
	height int,
	alpha bool,
) []byte {
	t.Helper()
	source := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			value := uint8((x*31 + y*17) % 251)
			a := uint8(0xff)
			if alpha && (x+y)%7 == 0 {
				a = 0x80
			}
			source.SetNRGBA(x, y, color.NRGBA{
				R: value,
				G: value ^ 0x5a,
				B: value ^ 0xa5,
				A: a,
			})
		}
	}
	var encoded bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
