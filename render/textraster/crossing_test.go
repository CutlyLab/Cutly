package textraster

import (
	"math/big"
	"testing"
)

func TestQuadraticCrossingsUseExactRootsAndStrictRay(t *testing.T) {
	exact := exactContext{maxBits: 512}
	delta := make([]int64, 33)
	// y=4+24t(1-t) intersects y=8 twice. x=20+160t puts the exact
	// crossings between sample centers 4+8*k at thresholds 7 and 18.
	segment := rasterSegment{kind: segmentQuad,
		p0: rasterPoint{x: 20, y: 4}, p1: rasterPoint{x: 100, y: 16}, p2: rasterPoint{x: 180, y: 4}}
	if err := addQuadraticCrossings(exact, segment, 8, 32, delta, "test/quad"); err != nil {
		t.Fatal(err)
	}
	if delta[0] != 0 || delta[7] != -1 || delta[18] != 1 {
		t.Fatalf("unexpected exact-root deltas: d0=%d d7=%d d18=%d all=%v", delta[0], delta[7], delta[18], delta)
	}
	var winding int64
	for column := 0; column < 32; column++ {
		winding += delta[column]
		want := int64(0)
		if column >= 7 && column < 18 {
			want = -1
		}
		if winding != want {
			t.Fatalf("column %d winding=%d want=%d", column, winding, want)
		}
	}
	if winding+delta[32] != 0 {
		t.Fatal("crossing deltas did not close")
	}
}

func TestQuadraticFlatEndpointsAndInteriorTangent(t *testing.T) {
	exact := exactContext{maxBits: 512}
	tests := []struct {
		name    string
		segment rasterSegment
		want    int64
	}{
		{
			name: "flat_start_enters", want: 1,
			segment: rasterSegment{kind: segmentQuad,
				p0: rasterPoint{x: 100, y: 8}, p1: rasterPoint{x: 100, y: 8}, p2: rasterPoint{x: 100, y: 16}},
		},
		{
			name: "flat_end_leaves", want: -1,
			segment: rasterSegment{kind: segmentQuad,
				p0: rasterPoint{x: 100, y: 16}, p1: rasterPoint{x: 100, y: 8}, p2: rasterPoint{x: 100, y: 8}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delta := make([]int64, 33)
			if err := addQuadraticCrossings(exact, test.segment, 8, 32, delta, "test/endpoint"); err != nil {
				t.Fatal(err)
			}
			// x=100 is itself sample center k=12. Strict crossing_x>sample_x means
			// only k=0..11 are affected, never k=12.
			if delta[0] != test.want || delta[12] != -test.want || delta[13] != 0 {
				t.Fatalf("deltas=%v want [0]=%d [12]=%d", delta, test.want, -test.want)
			}
		})
	}
	interior := rasterSegment{kind: segmentQuad,
		p0: rasterPoint{x: 20, y: 8}, p1: rasterPoint{x: 100, y: -8}, p2: rasterPoint{x: 180, y: 8}}
	delta := make([]int64, 33)
	if err := addQuadraticCrossings(exact, interior, 0, 32, delta, "test/tangent"); err != nil {
		t.Fatal(err)
	}
	for index, value := range delta {
		if value != 0 {
			t.Fatalf("interior tangent emitted delta[%d]=%d", index, value)
		}
	}
}

func TestExactSquareRootExpressionEquality(t *testing.T) {
	exact := exactContext{maxBits: 512}
	for _, test := range []struct {
		u, v, d int64
		want    int
	}{{-6, 1, 36, 0}, {-7, 1, 36, -1}, {-5, 1, 36, 1}, {6, -1, 36, 0}} {
		got, err := signSqrtExpression(exact, big.NewInt(test.u), big.NewInt(test.v), big.NewInt(test.d), "test/sqrt")
		if err != nil || got != test.want {
			t.Fatalf("sign(%d+%d*sqrt(%d))=%d err=%v want=%d", test.u, test.v, test.d, got, err, test.want)
		}
	}
}
