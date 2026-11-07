package vectorraster

import (
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

const (
	ProfileSchemaVersion = 1
	ProfileV1            = "cutly.raster.vector-path-viewport-px-supersample8x8.v1"
	ReceiptSchemaVersion = 1
	ReceiptProfileV1     = "cutly.raster.vector-path-viewport-px-supersample8x8.receipt.v1"
	SourceHashProfileV1  = "cutly.hash.vector-source-canonical-json.v1"
)

// ProfileDescriptor is the complete discovery contract. Changing any pixel rule requires a new
// profile; Limits may reject work but cannot select a lower-quality path.
type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	InputProfile             string `json:"input_profile"`
	GeometryMapping          string `json:"geometry_mapping"`
	ViewportCoordinates      string `json:"viewport_coordinates"`
	Sampling                 string `json:"sampling"`
	PointInFill              string `json:"point_in_fill"`
	SubpathClosure           string `json:"subpath_closure"`
	FillRules                string `json:"fill_rules"`
	PaintProfile             string `json:"paint_profile"`
	CoverageQuantization     string `json:"coverage_quantization"`
	Composition              string `json:"composition"`
	KernelProfile            string `json:"kernel_profile"`
	SurfaceProfile           string `json:"surface_profile"`
	SourceHashProfile        string `json:"source_hash_profile"`
	SourceHashSemantics      string `json:"source_hash_semantics"`
	ResourceSemantics        string `json:"resource_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	OwnershipSemantics       string `json:"ownership_semantics"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
	DefaultLimits            Limits `json:"default_limits"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	InputProfile:             documentv2.VectorProfilePathViewportPXV2,
	GeometryMapping:          "Use exact rational xMidYMid meet: s=min(viewport_width/view_box_width,viewport_height/view_box_height), x=(viewport_width-s*view_box_width)/2+s*(source_x-view_box_x), y=(viewport_height-s*view_box_height)/2+s*(source_y-view_box_y). No float or host vector API is permitted.",
	ViewportCoordinates:      "The declared raster viewport is top-down square-pixel edge coordinates: x in [0,width] and y in [0,height]. Samples outside the viewport are clipped and uncovered letterbox pixels remain transparent.",
	Sampling:                 "Every output pixel uses exactly 64 equally weighted samples at (x+(2*i+1)/16,y+(2*j+1)/16) for i,j in [0,7]. There is no adaptive sampling, jitter or platform-dependent antialiasing.",
	PointInFill:              "Cast a ray from each sample toward +x. An edge crosses only when sample_y is in its lower-inclusive upper-exclusive y interval and crossing_x is strictly greater than sample_x; horizontal and degenerate edges never cross. Consequently an axis-aligned rectangle is top/left inclusive and bottom/right exclusive.",
	SubpathClosure:           "move_to starts a subpath. A still-open subpath is conceptually closed by one straight edge when the next move_to begins or the path ends; close adds that edge immediately. Conceptual edges do not alter canonical source bytes.",
	FillRules:                "nonzero accumulates signed directed crossings and is inside when the sum is nonzero; evenodd is inside after an odd number of crossings. Paths are fill-only, and source path array order is paint order.",
	PaintProfile:             color.ProfileRGBA16SRGBPaintV1,
	CoverageQuantization:     "For each path and pixel, multiply each premultiplied channel returned by the paint profile by the integer covered-sample count in [0,64], divide by 64, and round the complete rational once to nearest integer with exact half ties to even.",
	Composition:              "Composite each coverage-scaled path over the accumulated pixel in source path array order with the exact Porter-Duff source-over equation and single nearest-even quantization of cutly.compositor.cpu-kernel.v1.",
	KernelProfile:            cpucompositor.KernelProfileV1,
	SurfaceProfile:           cpucompositor.SurfaceProfileV1,
	SourceHashProfile:        SourceHashProfileV1,
	SourceHashSemantics:      "SHA-256 over the complete validated VectorSource serialized as canonical JSON: recursively lexicographic object keys, source array order, no insignificant whitespace, base-10 model integers and standard JSON string escaping; encode as lowercase hex with the sha256: prefix. Profile, viewBox, viewport, path IDs, commands, fill rules and paints are included. Conceptual closure edges are not inserted.",
	ResourceSemantics:        "Before output allocation, validate dimensions, pixel/path/command/conceptual-edge counts, canonical-source upper bound, exact-integer bit bounds, retained bytes, logical temporary working bytes and overflow-checked conservative work units. Working accounting includes canonicalization, pixel staging, one reusable coverage plane and exact geometry/event/crossing storage; it excludes caller storage and the separately bounded retained Surface.",
	CancellationSemantics:    "A non-nil context is checked before validation, throughout source scans, geometry construction, scan conversion, coverage/composition and hashing, and immediately before publication. Cancellation never returns a Surface or Receipt.",
	OwnershipSemantics:       "Rasterize borrows the VectorSource object graph until return and callers must not mutate it concurrently. A successful Surface owns an immutable copy of all pixels, and Receipt contains no caller-owned mutable storage.",
	FailureSemantics:         "Unsupported profiles, v1 features, strokes, cubic commands, malformed canonical rationals, invalid command state, resource overflow, exact-bit overflow, allocation failure and cancellation fail closed with a nil Surface and zero Receipt. There is no partial or lower-quality result.",
	ExcludedResponsibilities: "Document/timeline identity, asset lookup, clip timing, transforms, opacity, transitions, text shaping, strokes, curves, SVG/XML/CSS, external references, host DPI, output transfer conversion, encoding, persistence and publication are outside this package.",
	DefaultLimits:            DefaultLimits(),
}

func Descriptor() ProfileDescriptor { return descriptorV1 }

// Receipt binds the complete successful raster result and the resource facts used to admit it.
// It is JSON-serializable and contains no slices, pointers or maps.
type Receipt struct {
	SchemaVersion       int    `json:"schema_version"`
	Profile             string `json:"profile"`
	RasterProfile       string `json:"raster_profile"`
	SourceProfile       string `json:"source_profile"`
	SourceHashProfile   string `json:"source_hash_profile"`
	SourceSemanticHash  string `json:"source_semantic_hash"`
	PaintProfile        string `json:"paint_profile"`
	KernelProfile       string `json:"kernel_profile"`
	SurfaceProfile      string `json:"surface_profile"`
	WidthPixels         uint32 `json:"width_pixels"`
	HeightPixels        uint32 `json:"height_pixels"`
	PixelHash           string `json:"pixel_hash"`
	PathCount           uint64 `json:"path_count"`
	CommandCount        uint64 `json:"command_count"`
	ConceptualEdgeCount uint64 `json:"conceptual_edge_count"`
	EffectiveEdgeCount  uint64 `json:"effective_edge_count"`
	// SampleCount is the semantic number of fixed sample classifications: width*height*64*paths.
	// The interval scan converter may prove and aggregate contiguous samples without visiting each
	// one, but never changes this count or the resulting coverage.
	SampleCount          uint64 `json:"sample_count"`
	WorkUnits            uint64 `json:"work_units"`
	CanonicalSourceBytes uint64 `json:"canonical_source_bytes"`
	WorkingBytes         uint64 `json:"working_bytes"`
	RetainedBytes        uint64 `json:"retained_bytes"`
}
