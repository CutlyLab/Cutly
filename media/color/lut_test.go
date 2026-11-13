package color

import (
	"math"
	"testing"
)

// The identity LUT must pass through values unchanged within floating-point tolerance.
func TestIdentityLUT(t *testing.T) {
	lut := IdentityLUT(33)
	inputs := [][3]float32{
		{0, 0, 0}, {0.5, 0.5, 0.5}, {1, 1, 1},
		{0.1, 0.7, 0.3}, {0.9, 0.05, 0.55}, {0.33, 0.67, 0.99},
	}
	for _, input := range inputs {
		r, g, b := lut.Apply(input[0], input[1], input[2])
		if math.Abs(float64(r-input[0])) > 0.02 || math.Abs(float64(g-input[1])) > 0.02 || math.Abs(float64(b-input[2])) > 0.02 {
			t.Fatalf("identity LUT: input %v output (%.4f,%.4f,%.4f)", input, r, g, b)
		}
	}
}

// A .cube file must round-trip through parse → apply.
func TestParseAndApplyCubeFile(t *testing.T) {
	// Build a simple warm-shift LUT: boost red, suppress blue
	cube := `# Test LUT
TITLE "warm_shift"
LUT_3D_SIZE 2
0.0 0.0 0.0
0.0 0.0 0.5
0.0 0.5 0.0
0.0 0.5 0.5
0.5 0.0 0.0
0.5 0.0 0.5
0.5 0.5 0.0
0.5 0.5 0.5
`
	lut, err := ParseLUT([]byte(cube))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if lut.Size != 2 || len(lut.Values) != 8 || lut.Title != "warm_shift" {
		t.Fatalf("lut = %+v", lut)
	}
	// The cube maps 0->0 and 1->0.5; midpoint should be ~0.25
	r, _, _ := lut.Apply(0.5, 0, 0)
	if math.Abs(float64(r)-0.25) > 0.01 {
		t.Fatalf("half-red = %.4f, want ~0.25", r)
	}
}

// Monotonicity: a LUT that maps [0,1] -> [0,0.5] must be monotonically increasing.
func TestLUTMonotonic(t *testing.T) {
	lut := IdentityLUT(17)
	prev := -1.0
	for i := 0; i <= 100; i++ {
		x := float32(i) / 100
		r, _, _ := lut.Apply(x, 0, 0)
		if float64(r) < prev-0.001 {
			t.Fatalf("LUT not monotonic at x=%.2f: r=%.4f < prev=%.4f", x, r, prev)
		}
		prev = float64(r)
	}
}

// Malformed LUT must fail closed.
func TestParseLUTRejectsMalformed(t *testing.T) {
	tests := map[string]string{
		"missing size": "TITLE \"x\"\n0 0 0",
		"wrong size":   "LUT_3D_SIZE 2\n0 0 0\n0 0 0\n0 0 0",
		"size too big": "LUT_3D_SIZE 200\n",
	}
	for name, data := range tests {
		if _, err := ParseLUT([]byte(data)); err == nil {
			t.Fatalf("%s: accepted malformed LUT", name)
		}
	}
}
