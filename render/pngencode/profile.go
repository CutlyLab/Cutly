package pngencode

import (
	"math"

	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/media/pngdecode"
)

const (
	ProfileSchemaVersion = 1
	ProfileV1            = "cutly.encode.png-rgba8-srgb-none-stored-deflate.v1"

	FilterProfileV1  = "cutly.png.filter.none-every-row.v1"
	DeflateProfileV1 = "cutly.zlib.deflate-stored-65535.v1"

	SRGBRenderingIntent     uint8  = 0
	StoredBlockMaxBytes            = 65535
	ArtifactWriteChunkBytes        = 64 << 10
	FixedArtifactBytes      uint64 = 70 // signature + IHDR + sRGB + IDAT envelope + IEND
	ZlibFixedBytes          uint64 = 6  // CMF/FLG + Adler-32
	MinimumArtifactBytes    uint64 = 86 // 1x1 RGBA plus filter and one stored block

	DefaultMaxWidth                = 8192
	DefaultMaxHeight               = 8192
	HardMaxDimension               = 65536
	DefaultMaxRawRGBABytes  uint64 = color.DefaultMaxOutputRGBA8Bytes
	HardMaxRawRGBABytes     uint64 = color.HardMaxOutputRGBA8Bytes
	DefaultMaxArtifactBytes uint64 = 320 << 20
	HardMaxIDATBytes        uint64 = math.MaxInt32
	HardMaxArtifactBytes           = HardMaxIDATBytes + FixedArtifactBytes
	MinimumWorkingBytes     uint64 = MinimumArtifactBytes + color.BytesPerOutputRGBA8Pixel
	DefaultMaxWorkingBytes  uint64 = DefaultMaxArtifactBytes + DefaultMaxWidth*color.BytesPerOutputRGBA8Pixel
	HardMaxWorkingBytes            = HardMaxArtifactBytes + HardMaxDimension*color.BytesPerOutputRGBA8Pixel
)

type Limits struct {
	MaxWidth         int    `json:"max_width"`
	MaxHeight        int    `json:"max_height"`
	MaxRawRGBABytes  uint64 `json:"max_raw_rgba_bytes"`
	MaxArtifactBytes uint64 `json:"max_artifact_bytes"`
	MaxWorkingBytes  uint64 `json:"max_working_bytes"`
}

func DefaultLimits() Limits {
	return Limits{
		MaxWidth: DefaultMaxWidth, MaxHeight: DefaultMaxHeight,
		MaxRawRGBABytes: DefaultMaxRawRGBABytes, MaxArtifactBytes: DefaultMaxArtifactBytes,
		MaxWorkingBytes: DefaultMaxWorkingBytes,
	}
}

type ProfileDescriptor struct {
	SchemaVersion            int    `json:"schema_version"`
	Profile                  string `json:"profile"`
	InputColorProfile        string `json:"input_color_profile"`
	InputPixelHashProfile    string `json:"input_pixel_hash_profile"`
	RoundTripDecodeProfile   string `json:"round_trip_decode_profile"`
	ArtifactHashProfile      string `json:"artifact_hash_profile"`
	ContainerSemantics       string `json:"container_semantics"`
	ColorSemantics           string `json:"color_semantics"`
	ScanlineSemantics        string `json:"scanline_semantics"`
	ZlibSemantics            string `json:"zlib_semantics"`
	DeflateSemantics         string `json:"deflate_semantics"`
	ChunkSemantics           string `json:"chunk_semantics"`
	HashSemantics            string `json:"hash_semantics"`
	CancellationSemantics    string `json:"cancellation_semantics"`
	AllocationSemantics      string `json:"allocation_semantics"`
	DefaultLimits            Limits `json:"default_limits"`
	FailureSemantics         string `json:"failure_semantics"`
	ExcludedResponsibilities string `json:"excluded_responsibilities"`
}

var descriptorV1 = ProfileDescriptor{
	SchemaVersion:            ProfileSchemaVersion,
	Profile:                  ProfileV1,
	InputColorProfile:        color.ProfileLinearRGBA16ToRGBA8SRGBV1,
	InputPixelHashProfile:    color.OutputRGBAHashProfileV1,
	RoundTripDecodeProfile:   pngdecode.ProfileV1,
	ArtifactHashProfile:      pngdecode.ContentHashProfileV1,
	ContainerSemantics:       "Emit the PNG signature, one IHDR, one sRGB, one IDAT and one IEND in exactly that order, with no other chunk, split IDAT, metadata, padding or trailing byte. Every chunk length is unsigned big-endian and every CRC is IEEE CRC-32 over type plus data.",
	ColorSemantics:           "IHDR declares positive dimensions, bit depth 8, truecolor-with-alpha color type 6, compression/filter/interlace methods 0. The sole sRGB chunk has rendering intent 0. Pixel bytes are the input top-down tightly packed straight-alpha full-range sRGB RGBA8 stream unchanged.",
	ScanlineSemantics:        "Prefix every row with filter byte 0 (None), then copy exactly width*4 input bytes. Rows are top-down with no padding and no trailing decompressed byte.",
	ZlibSemantics:            "The single IDAT payload is one zlib stream with CMF/FLG bytes 0x78,0x01 and a terminal big-endian Adler-32 over every decompressed filter byte and RGBA row byte.",
	DeflateSemantics:         "Encode the zlib payload only as byte-aligned DEFLATE stored blocks. Partition the complete scanline stream from the beginning into consecutive blocks of at most 65535 bytes; every non-final block is exactly 65535 bytes. Emit header byte 0x00 for non-final or 0x01 for final, then little-endian LEN and one's-complement NLEN, then unchanged block bytes. Empty images are impossible.",
	ChunkSemantics:           "IDAT is exactly zlib_header + stored-block envelopes/data + Adler-32 and must not exceed the PNG 2^31-1 chunk-data limit. IHDR, sRGB and IEND use their canonical fixed lengths.",
	HashSemantics:            "Before publication recompute SHA-256 over all raw RGBA8 bytes and require both the input receipt and immutable Frame hash to match. ArtifactHash is SHA-256 over every encoded PNG byte. Geometry and profiles are bound separately in Result.",
	CancellationSemantics:    "Validate context before work; check cancellation before allocation, once per row, at every stored-block boundary, while checksumming the IDAT chunk and hashing artifact chunks in fixed 64 KiB segments, and immediately before publication. Failure returns no Result or partial Artifact.",
	AllocationSemantics:      "Preflight dimensions, width*height*4 raw bytes, scanline bytes, stored-block count, IDAT bytes, PNG chunk limit, platform int, artifact bytes and exact artifact+width*4 working bytes before allocation. Encode owns exactly one complete artifact plus one width*4 row scratch and fixed hash/checksum state; it consumes the immutable Frame through defensive row copies and never allocates a second frame-sized RGBA or scanline buffer. Artifact.Bytes returns a full defensive copy and WriteTo uses fixed 64 KiB defensive scratch; both occur after Encode and are outside its working budget.",
	DefaultLimits:            DefaultLimits(),
	FailureSemantics:         "Any context, profile, metadata, geometry, raw hash, layout, size, allocation or writer mismatch fails closed. Encoding never repairs a receipt, truncates pixels or publishes a partial Artifact.",
	ExcludedResponsibilities: "Working-surface conversion, ICC/HDR/tone mapping, resize, crop, transform, dithering, alternate PNG color types, bit depths, filters, interlace, ancillary metadata, adaptive/compressed DEFLATE, APNG, filesystem publication, signing and scheduling are outside this profile.",
}

func Descriptor() ProfileDescriptor { return descriptorV1 }
