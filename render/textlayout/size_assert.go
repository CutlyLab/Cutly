package textlayout

import "unsafe"

// Compile-time guards prevent logical budgets from under-accounting retained Go records on any
// supported GOARCH. The logical constants may be conservative, but may never be smaller.
const (
	_ = logicalPlacementBytes - uint64(unsafe.Sizeof(GlyphPlacement{}))
	_ = logicalCommandBytes - uint64(unsafe.Sizeof(layoutCommand{}))
	_ = logicalOutlineBytes - uint64(unsafe.Sizeof(layoutOutline{}))
	_ = logicalLayoutRecordBytes - uint64(unsafe.Sizeof(Layout{}))
	_ = logicalReferenceFlagBytes - uint64(unsafe.Sizeof(false))
)
