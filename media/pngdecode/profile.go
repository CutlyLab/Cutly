package pngdecode

import (
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
)

const (
	ProfileSchemaVersion = 1
	ProfileV1            = "cutly.decode.png-rgba8-explicit-srgb.v1"
	ContentHashProfileV1 = "cutly.hash.sha256.encoded-png-bytes.v1"

	DefaultMaxEncodedBytes    uint64 = 128 << 20
	DefaultMaxWorkingBytes    uint64 = 512 << 20
	HardMaxEncodedBytes       uint64 = 1 << 30
	HardMaxWorkingBytes       uint64 = 2 << 30
	workingFrameBytesPerPixel uint64 = 20 // decoded RGBA8 plus color scratch and immutable RGBA16 surface
	minimumWorkingBytes       uint64 = 30 // one pixel plus two (filter byte + RGBA8 row) decoder buffers
)

// Limits is an allocation policy, not a rendering semantic. MaxWorkingBytes accounts for the
// decoded RGBA8 frame, the two complete RGBA16 frames required by color.NormalizeRGBA8, and the
// decoder's current/previous RGBA8 scanline buffers. Bounded zlib bookkeeping is additional.
type Limits struct {
	MaxWidth        int    `json:"max_width"`
	MaxHeight       int    `json:"max_height"`
	MaxEncodedBytes uint64 `json:"max_encoded_bytes"`
	MaxWorkingBytes uint64 `json:"max_working_bytes"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxWidth: 8192, MaxHeight: 8192,
		MaxEncodedBytes: DefaultMaxEncodedBytes, MaxWorkingBytes: DefaultMaxWorkingBytes,
	}
}

// ProfileDescriptor freezes the successful-input subset and every pixel-affecting handoff. The
// package owns the five lossless PNG filters and uses only a zlib inflater after independently
// validating the complete chunk envelope.
type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	ContainerSubset          string `json:"container_subset"`
	ChunkSequence            string `json:"chunk_sequence"`
	ColorDeclaration         string `json:"color_declaration"`
	RenderingIntent          string `json:"rendering_intent"`
	PixelDecode              string `json:"pixel_decode"`
	NormalizationProfile     string `json:"normalization_profile"`
	OutputSurfaceProfile     string `json:"output_surface_profile"`
	ContentHashProfile       string `json:"content_hash_profile"`
	EncodedInputSemantics    string `json:"encoded_input_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	AllocationSemantics      string `json:"allocation_semantics"`
	DefaultLimits            Limits `json:"default_limits"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	ContainerSubset:          "PNG signature; one IHDR declaring positive dimensions, bit depth 8, truecolor-with-alpha color type 6, compression/filter method 0 and interlace method 0; one sRGB; one or more contiguous IDAT; and one terminal IEND. No other chunks are accepted.",
	ChunkSequence:            "Every chunk length is bounds checked, every IEEE CRC-32 is verified over type+data, IDAT chunks are contiguous, IEND has zero length, and no byte may follow IEND. Unknown critical and all ancillary chunks other than the required sRGB declaration fail closed.",
	ColorDeclaration:         "The required sRGB chunk is the sole color declaration. gAMA, cHRM, iCCP, sBIT, eXIf and every other ancillary chunk are rejected, including declarations that might appear equivalent. Decoding never consults a host ICC service.",
	RenderingIntent:          "The one-byte sRGB rendering intent must be 0, 1, 2 or 3 and is reported in Result. It does not alter the normative sRGB EOTF used to create the linear working surface.",
	PixelDecode:              "Treat all contiguous IDAT payloads as one closed zlib stream: it must inflate to exactly height*(1+width*4) scanline bytes, reach zlib EOF/checksum, and leave no compressed trailing byte. Apply PNG filters None, Sub, Up, Average and Paeth byte-for-byte with RGBA bytes-per-pixel=4, checking cancellation per row and 32 KiB filter segment. The result is exact top-down, tightly packed straight RGBA8 matching IHDR; palette, grayscale, 16-bit and interlaced paths are outside this profile.",
	NormalizationProfile:     color.ProfileRGBA8SRGBV1,
	OutputSurfaceProfile:     cpucompositor.SurfaceProfileV1,
	ContentHashProfile:       ContentHashProfileV1,
	EncodedInputSemantics:    "Decode borrows one complete encoded byte slice only for the call. The caller must keep it immutable until return; the same immutable slice may be read concurrently. Decode retains no alias, and Result binds the complete borrowed bytes with canonical SHA-256.",
	CancellationSemantics:    "Check cancellation before and throughout chunk/CRC validation, before lossless decode, through a context-aware bounded-read adapter, after decode, for every normalization row, before/after surface hashing, throughout encoded-byte hashing and immediately before returning Result. No partial Result or Surface is returned.",
	AllocationSemantics:      "Before decode require width*height*20 + 2*(1 + width*4) <= MaxWorkingBytes. This covers one decoded RGBA8 frame, one RGBA16 normalization scratch frame, one immutable RGBA16 Surface, and the decoder's current/previous filtered RGBA8 scanlines. Caller-owned encoded bytes and bounded zlib bookkeeping are excluded.",
	DefaultLimits:            DefaultLimits(),
	ExcludedResponsibilities: "Remote fetch, trust/asset authorization, animation/APNG, ICC, HDR, EXIF orientation, non-square pixel metadata, resize, crop, affine transform, frame selection, output conversion, encoding and muxing are outside this profile.",
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
