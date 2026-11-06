package color

import "github.com/CutlyLab/Cutly/render/cpucompositor"

const (
	PaintProfileSchemaVersion = 1
	ProfileRGBA16SRGBPaintV1  = "cutly.color.rgba16-srgb-straight-paint-to-linear-bt709-premultiplied-rgba16.v1"

	PaintTransferTableSHA256V1 = "sha256:58bf6182e171e3836767f8aad3662784c625c72ce79d0c054f4fb5e742387804"
)

// PaintProfileDescriptor freezes the scalar Document-paint conversion. It deliberately does not
// describe image storage, Surface construction or document model types.
type PaintProfileDescriptor struct {
	SchemaVersion              int    `json:"schema_version"`
	Profile                    string `json:"profile"`
	InputSemantics             string `json:"input_semantics"`
	InputColorimetry           string `json:"input_colorimetry"`
	InputAlpha                 string `json:"input_alpha"`
	OutputSurfaceProfile       string `json:"output_surface_profile"`
	BreakpointSemantics        string `json:"breakpoint_semantics"`
	LinearSegment              string `json:"linear_segment"`
	NonlinearSegment           string `json:"nonlinear_segment"`
	TransferQuantization       string `json:"transfer_quantization"`
	Premultiplication          string `json:"premultiplication"`
	TransferTableSerialization string `json:"transfer_table_serialization"`
	TransferTableSHA256        string `json:"transfer_table_sha256"`
	OperationBounds            string `json:"operation_bounds"`
	FailureSemantics           string `json:"failure_semantics"`
	ExcludedResponsibilities   string `json:"excluded_responsibilities"`
}

// PaintDescriptor returns the complete immutable v1 paint conversion contract by value.
func PaintDescriptor() PaintProfileDescriptor {
	return PaintProfileDescriptor{
		SchemaVersion:              PaintProfileSchemaVersion,
		Profile:                    ProfileRGBA16SRGBPaintV1,
		InputSemantics:             "One scalar RGBA16Input paint with unpremultiplied RGB encoded across the complete uint16 sRGB code range. It is not an image, row buffer, Surface or document-model type.",
		InputColorimetry:           "RGB uses BT.709/sRGB primaries, D65 white, the exact sRGB transfer function and full range.",
		InputAlpha:                 "Alpha is an independent linear uint16 straight-alpha value and is copied exactly to output A. Hidden RGB under alpha zero is accepted but output is exact transparent black.",
		OutputSurfaceProfile:       cpucompositor.SurfaceProfileV1,
		BreakpointSemantics:        "Select the linear EOTF segment exactly when encoded_code/65535 <= 809/20000 by comparing encoded_code*20000 <= 809*65535 with integers; codes 0 through 2650 are linear and 2651 through 65535 are nonlinear.",
		LinearSegment:              "For a linear-segment code c, the final unpremultiplied linear uint16 value is the nearest-even integer to 25*c/323, derived exactly from (c/65535)/12.92*65535 with 12.92=323/25.",
		NonlinearSegment:           "For a nonlinear code c, evaluate 65535*(((c/65535)+11/200)/(211/200))^(12/5). Select the final uint16 directly with exact big-integer threshold comparisons after raising positive sides to the fifth power; no root approximation or intermediate linear quantization is used.",
		TransferQuantization:       "Round the complete exact EOTF result once to the nearest uint16; an exact half chooses the even integer. Float, pow, platform color APIs, dithering, clamps and host-dependent approximations are forbidden.",
		Premultiplication:          "After EOTF quantization, compute each RGB channel times the original linear alpha divided by 65535 and round the complete exact rational once to nearest-even. Alpha remains unchanged; alpha zero produces 0,0,0,0.",
		TransferTableSerialization: "For conformance only, evaluate all 65536 encoded codes in ascending order and serialize each resulting unpremultiplied linear uint16 as unsigned big-endian with no header. Runtime does not retain this table.",
		TransferTableSHA256:        PaintTransferTableSHA256V1,
		OperationBounds:            "Each scalar conversion performs at most three nonlinear conversions. Each nonlinear conversion uses one exact fixed-size preparation and exactly 16 binary threshold decisions over uint16 output; every persistent big.Int operand and result is non-negative and at most 290 bits. All big.Int state is function-local and no process-wide mutable lookup or cache exists.",
		FailureSemantics:           "Every RGBA16Input bit pattern is valid and conversion returns one complete Pixel by value. There is no partial result, allocation budget, I/O or external fallback.",
		ExcludedResponsibilities:   "Document schemas and validation, keyframe interpolation, image buffers, Surface allocation, compositing, rasterization, ICC/HDR/tone mapping, gamut conversion, resize, encoding, persistence and publication are outside this scalar paint profile.",
	}
}
