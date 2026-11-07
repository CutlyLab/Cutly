package sfntglyf

import (
	"testing"
	"unsafe"
)

const layoutEdgeEvidenceActualBytes = uint64(unsafe.Sizeof(layoutEdgeEvidence{}))
const gsubLogicalLookupEdgeActualBytes = uint64(unsafe.Sizeof(gsubLogicalLookupEdge{}))

// Both directions are deliberate. Any packing drift makes one array length a
// negative constant and fails the test build before the runtime assertion.
var _ [layoutEdgeEvidenceRetainedBytes - layoutEdgeEvidenceActualBytes]struct{}
var _ [layoutEdgeEvidenceActualBytes - layoutEdgeEvidenceRetainedBytes]struct{}
var _ [gsubLogicalLookupEdgeRetainedBytes - gsubLogicalLookupEdgeActualBytes]struct{}
var _ [gsubLogicalLookupEdgeActualBytes - gsubLogicalLookupEdgeRetainedBytes]struct{}

func TestLayoutEdgeEvidenceRetainedSize(t *testing.T) {
	if got := uint64(unsafe.Sizeof(layoutEdgeEvidence{})); got != layoutEdgeEvidenceRetainedBytes {
		t.Fatalf("layoutEdgeEvidence size = %d, retained charge = %d", got, layoutEdgeEvidenceRetainedBytes)
	}
}

func TestGSUBLogicalLookupEdgeRetainedSize(t *testing.T) {
	if got := uint64(unsafe.Sizeof(gsubLogicalLookupEdge{})); got != gsubLogicalLookupEdgeRetainedBytes {
		t.Fatalf("gsubLogicalLookupEdge size = %d, retained charge = %d", got, gsubLogicalLookupEdgeRetainedBytes)
	}
}
