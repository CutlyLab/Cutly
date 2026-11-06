package color

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
)

// This exhausts every encoded-code/alpha pair. The EOTF values come from the independent
// fifth-power oracle in srgb_lut_test.go; the alpha oracle uses the closed form for division by
// odd denominator 255, where an exact half tie is impossible.
func TestNormalizeRGBA8EveryCodeAlphaPairMatchesExactOracle(t *testing.T) {
	const side = 256
	encoded := make([]byte, side*side*4)
	for alpha := 0; alpha < side; alpha++ {
		for code := 0; code < side; code++ {
			offset := (alpha*side + code) * 4
			encoded[offset] = byte(code)
			encoded[offset+1] = byte(255 - code)
			encoded[offset+2] = byte(code ^ 0x5a)
			encoded[offset+3] = byte(alpha)
		}
	}

	surface, err := NormalizeRGBA8(context.Background(), RGBA8Input{
		Width: side, Height: side, Stride: side * 4,
		Metadata: MetadataRGBA8SRGBV1(), Pixels: encoded,
	}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var canonical bytes.Buffer
	if err := surface.WriteCanonical(&canonical); err != nil {
		t.Fatal(err)
	}
	if canonical.Len() != side*side*8 {
		t.Fatalf("canonical byte length = %d, want %d", canonical.Len(), side*side*8)
	}

	var linear [side]uint16
	for code := range linear {
		linear[code] = exactSRGBOracle(uint64(code))
	}
	premultiply := func(value uint16, alpha int) uint16 {
		// value*alpha/255 cannot be exactly half-integral because 255 is odd.
		return uint16((uint64(value)*uint64(alpha) + 127) / 255)
	}
	bytes := canonical.Bytes()
	for alpha := 0; alpha < side; alpha++ {
		for code := 0; code < side; code++ {
			offset := (alpha*side + code) * 8
			gotR := binary.BigEndian.Uint16(bytes[offset:])
			gotG := binary.BigEndian.Uint16(bytes[offset+2:])
			gotB := binary.BigEndian.Uint16(bytes[offset+4:])
			gotA := binary.BigEndian.Uint16(bytes[offset+6:])
			wantR := premultiply(linear[code], alpha)
			wantG := premultiply(linear[255-code], alpha)
			wantB := premultiply(linear[code^0x5a], alpha)
			wantA := uint16(alpha * 257)
			if gotR != wantR || gotG != wantG || gotB != wantB || gotA != wantA {
				t.Fatalf("code=%d alpha=%d got RGBA=(%d,%d,%d,%d), want (%d,%d,%d,%d)",
					code, alpha, gotR, gotG, gotB, gotA, wantR, wantG, wantB, wantA)
			}
		}
	}
}
