package sfntglyf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func hashLayoutGraph(ctx context.Context, receipt LayoutGraphReceipt, limit uint64) (
	result string, canonicalBytes uint64, err error,
) {
	defer func() {
		if recover() != nil {
			result, canonicalBytes = "", 0
			err = fontError(CodeAllocationFailed, "layout_graph/hash/allocation", ErrAllocationFailed)
		}
	}()
	if err := contextFailure(ctx, "layout_graph/hash/context"); err != nil {
		return "", 0, err
	}
	if limit == 0 || receipt.SchemaVersion != LayoutGraphSchemaVersion ||
		receipt.Profile != LayoutGraphReceiptProfileV1 || receipt.DescriptorHash != layoutGraphDescriptorIdentityV1 ||
		receipt.GraphHashProfile != LayoutGraphCanonicalHashProfileV1 ||
		!validLayoutFoundationDigest(receipt.SourceOutlineSetHash) ||
		!validLayoutFoundationDigest(receipt.UpstreamContentHash) ||
		!validLayoutFoundationDigest(receipt.GDEFFoundationHash) ||
		!validLayoutFoundationDigest(receipt.GSUBFoundationHash) ||
		!validLayoutFoundationDigest(receipt.GPOSFoundationHash) {
		return "", 0, fontError(CodeLayoutGraphInvariant, "layout_graph/hash/input", ErrInvariantFailed)
	}
	sink := layoutFoundationHashSink{ctx: ctx, hash: sha256.New(), limit: limit}
	writes := []func() error{
		func() error { return sink.str(LayoutGraphCanonicalHashProfileV1, "layout_graph/hash/profile") },
		func() error { return sink.u32(LayoutGraphSchemaVersion, "layout_graph/hash/schema") },
		func() error { return sink.str(LayoutGraphProfileV1, "layout_graph/hash/graph_profile") },
		func() error { return sink.str(layoutGraphDescriptorIdentityV1, "layout_graph/hash/descriptor") },
		func() error { return sink.str(receipt.SourceProfile, "layout_graph/hash/source/profile") },
		func() error {
			return sink.str(receipt.SourceDescriptorHashProfile, "layout_graph/hash/source/descriptor_profile")
		},
		func() error { return sink.str(receipt.SourceDescriptorHash, "layout_graph/hash/source/descriptor") },
		func() error {
			return sink.str(receipt.SourceOutlineSetHashProfile, "layout_graph/hash/source/outline_hash_profile")
		},
		func() error { return sink.str(receipt.SourceOutlineSetHash, "layout_graph/hash/source/outline_hash") },
		func() error {
			return sink.str(receipt.UpstreamContentHashProfile, "layout_graph/hash/source/content_hash_profile")
		},
		func() error { return sink.str(receipt.UpstreamContentHash, "layout_graph/hash/source/content_hash") },
		func() error { return sink.u32(receipt.GlyphCount, "layout_graph/hash/source/glyph_count") },
		func() error {
			return sink.str(receipt.GDEFFoundationHashProfile, "layout_graph/hash/gdef/hash_profile")
		},
		func() error { return sink.str(receipt.GDEFFoundationHash, "layout_graph/hash/gdef/hash") },
		func() error {
			return sink.str(receipt.GSUBFoundationHashProfile, "layout_graph/hash/gsub/hash_profile")
		},
		func() error { return sink.str(receipt.GSUBFoundationHash, "layout_graph/hash/gsub/hash") },
		func() error {
			return sink.str(receipt.GPOSFoundationHashProfile, "layout_graph/hash/gpos/hash_profile")
		},
		func() error { return sink.str(receipt.GPOSFoundationHash, "layout_graph/hash/gpos/hash") },
		func() error { return sink.str(receipt.ValidationProfile, "layout_graph/hash/validation_profile") },
		func() error { return sink.str(receipt.CapabilityStatus, "layout_graph/hash/capability_status") },
		func() error { return sink.str(receipt.LookupExecutionStatus, "layout_graph/hash/execution_status") },
		func() error { return sink.str(receipt.VariableLayoutStatus, "layout_graph/hash/variable_status") },
		func() error {
			return sink.u64(receipt.ValidationCanonicalBinaryBytes, "layout_graph/hash/validation_canonical_bytes")
		},
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return "", 0, err
		}
	}
	for index, table := range receipt.Tables {
		path := fmt.Sprintf("layout_graph/hash/tables/%d", index)
		present := uint8(0)
		if table.Present {
			present = 1
		}
		for _, write := range []func() error{
			func() error { return sink.str(string(table.Tag), path+"/tag") },
			func() error { return sink.u8(present, path+"/present") },
			func() error { return sink.u32(table.FileOffset, path+"/file_offset") },
			func() error { return sink.u32(table.Length, path+"/length") },
			func() error { return sink.u32(table.Checksum, path+"/checksum") },
			func() error { return sink.str(table.HashProfile, path+"/hash_profile") },
			func() error { return sink.str(table.ContentHash, path+"/content_hash") },
			func() error { return sink.u16(table.MajorVersion, path+"/major") },
			func() error { return sink.u16(table.MinorVersion, path+"/minor") },
			func() error { return sink.u64(table.ScriptCount, path+"/scripts") },
			func() error { return sink.u64(table.FeatureCount, path+"/features") },
			func() error { return sink.u64(table.LookupCount, path+"/lookups") },
			func() error { return sink.u64(table.NodeCount, path+"/nodes") },
			func() error { return sink.u64(table.EdgeCount, path+"/edges") },
			func() error { return sink.u64(table.LogicalEdges, path+"/logical_edges") },
			func() error { return sink.u32(table.LogicalDepth, path+"/logical_depth") },
			func() error { return sink.u64(table.Expansion, path+"/logical_expansion") },
		} {
			if err := write(); err != nil {
				return "", 0, err
			}
		}
	}
	if err := contextFailure(ctx, "layout_graph/hash/finalize"); err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(sink.hash.Sum(nil)), sink.count, nil
}
