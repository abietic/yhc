package mediaimage

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image/png"
	"testing"
)

func TestTransportDerivativePreservesSourceAlphaAndPixelBudget(t *testing.T) {
	for _, alpha := range []bool{false, true} {
		source := recoveryTestPNG(t, 800, 400, alpha)
		original := append([]byte(nil), source...)
		derived, err := DeriveForTransport(t.Context(), source, "image/png", 5*1024*1024)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(derived.Data)
		if len(derived.Data) == 0 || len(derived.Data) > len(source)/2 || derived.Width != 800 || derived.Height != 400 {
			t.Fatalf("transport derivative shape: bytes=%d width=%d height=%d", len(derived.Data), derived.Width, derived.Height)
		}
		if !bytes.Equal(source, original) {
			t.Fatal("canonical bytes were changed")
		}
		if alpha {
			if derived.MIMEType != "image/png" {
				t.Fatal("alpha was flattened to JPEG")
			}
			decoded, err := png.Decode(bytes.NewReader(derived.Data))
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, a := decoded.At(0, 0).RGBA()
			if a != 0x8080 {
				t.Fatal("PNG alpha value changed")
			}
		} else if derived.MIMEType != "image/jpeg" {
			t.Fatal("opaque PNG was not compacted to JPEG")
		}
		if _, reason := Inspect(derived.Data, derived.MIMEType); reason != "" {
			t.Fatalf("invalid output: %s", reason)
		}
	}
	profile := derivativeProfile{maxLongEdge: 8192, maxPixels: 1_690_000, jpegQuality: 85}
	for _, tc := range []struct{ w, h, wantW, wantH int }{
		{2752, 1510, 1755, 962},
		{8192, 64, 8192, 64},
		{512, 512, 512, 512},
		{10000, 1, 8192, 1},
	} {
		w, h := derivativeDimensions(tc.w, tc.h, profile)
		if w != tc.wantW || h != tc.wantH {
			t.Fatalf("dimensions %dx%d -> %dx%d want %dx%d", tc.w, tc.h, w, h, tc.wantW, tc.wantH)
		}
	}
}

func TestTransportDerivativeKeepsSmallMalformedAndAnimatedPNG(t *testing.T) {
	source := recoveryTestPNG(t, 512, 512, false)
	animated := append([]byte(nil), source[:33]...)
	var chunk bytes.Buffer
	_ = binary.Write(&chunk, binary.BigEndian, uint32(8))
	chunk.WriteString("acTL")
	_ = binary.Write(&chunk, binary.BigEndian, uint32(2))
	_ = binary.Write(&chunk, binary.BigEndian, uint32(0))
	_ = binary.Write(&chunk, binary.BigEndian, crc32.ChecksumIEEE(chunk.Bytes()[4:]))
	animated = append(animated, chunk.Bytes()...)
	animated = append(animated, source[33:]...)
	if _, reason := Inspect(animated, "image/png"); reason != "" {
		t.Fatalf("animated fixture structure invalid: %s", reason)
	}
	for _, data := range [][]byte{
		recoveryTestPNG(t, 16, 16, false),
		append(append([]byte(nil), source...), []byte("trailing-private-payload")...),
		bytes.Repeat([]byte{0}, 300*1024),
		animated,
	} {
		derived, err := DeriveForTransport(t.Context(), data, "image/png", 5*1024*1024)
		if err != nil || len(derived.Data) != 0 {
			t.Fatal("ineligible image was silently reinterpreted")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := DeriveForTransport(ctx, source, "image/png", 5*1024*1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
}
