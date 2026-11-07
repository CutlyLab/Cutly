package sfntglyf

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"sort"
	"strings"
)

type layoutFoundationHashSink struct {
	ctx   context.Context
	hash  hash.Hash
	count uint64
	limit uint64
}

// layoutFoundationCanonicalEvidenceFromGDEF projects the parser's stable
// physical evidence into the foundation preimage. Node payloads are direct
// spans of the already verified, operation-local GDEF view. The caller must
// hash this value before that view goes out of scope; layoutFoundation never
// stores this projection or any of its payload slices.
func layoutFoundationCanonicalEvidenceFromGDEF(ctx context.Context, source layoutSourceBinding,
	tables [3]layoutTableBinding, gdef *gdefFoundation, gdefBytes []byte,
	resources layoutFoundationResourceStats) (layoutFoundationCanonicalEvidence, error) {
	result := layoutFoundationCanonicalEvidence{source: source, tables: tables, resources: resources, gdef: gdef}
	if gdef == nil {
		if tables[0].present || len(gdefBytes) != 0 || resources.nodes != 0 || resources.edges != 0 {
			return layoutFoundationCanonicalEvidence{}, layoutFoundationHashInvariant("layout_foundation/hash/project/absent")
		}
		return result, contextFailure(ctx, "layout_foundation/hash/project/absent")
	}
	if !tables[0].present || uint64(len(gdefBytes)) != uint64(tables[0].length) {
		return layoutFoundationCanonicalEvidence{}, layoutFoundationHashInvariant("layout_foundation/hash/project/table")
	}
	result.gdefPresent = true
	result.gdefMajorVersion = gdef.majorVersion
	result.gdefMinorVersion = gdef.minorVersion
	if uint64(len(gdef.nodes)) != resources.nodes || uint64(len(gdef.edges)) != resources.edges {
		return layoutFoundationCanonicalEvidence{}, layoutFoundationHashInvariant("layout_foundation/hash/project/counts")
	}
	if err := contextFailure(ctx, "layout_foundation/hash/project/nodes/allocate"); err != nil {
		return layoutFoundationCanonicalEvidence{}, err
	}
	result.nodes = make([]layoutFoundationCanonicalNode, len(gdef.nodes))
	var directBytes uint64
	for index, node := range gdef.nodes {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout_foundation/hash/project/nodes"); err != nil {
				return layoutFoundationCanonicalEvidence{}, err
			}
		}
		if node.tableTag != layoutGDEFTag || node.start > uint64(^uint32(0)) || node.end <= node.start ||
			node.end > uint64(len(gdefBytes)) || node.end-node.start > uint64(^uint32(0)) {
			return layoutFoundationCanonicalEvidence{}, layoutFoundationHashInvariant("layout_foundation/hash/project/node")
		}
		spanLength := node.end - node.start
		var ok bool
		directBytes, ok = checkedAdd(directBytes, spanLength)
		if !ok {
			return layoutFoundationCanonicalEvidence{}, fontError(CodeResourceLimit, "layout_foundation/hash/project/direct_bytes", ErrResourceLimit)
		}
		start, end := int(node.start), int(node.end)
		result.nodes[index] = layoutFoundationCanonicalNode{
			key: layoutFoundationCanonicalNodeKey{
				tag: layoutFoundationTableTags[0], offset: uint32(node.start), kind: uint16(node.kind),
				param0: node.paramFirst, param1: node.paramSecond,
			},
			spanStart: uint32(node.start), spanLength: uint32(spanLength), payload: gdefBytes[start:end],
		}
	}
	if directBytes != resources.directBytes {
		return layoutFoundationCanonicalEvidence{}, layoutFoundationHashInvariant("layout_foundation/hash/project/direct_bytes")
	}
	if err := contextFailure(ctx, "layout_foundation/hash/project/edges/allocate"); err != nil {
		return layoutFoundationCanonicalEvidence{}, err
	}
	result.edges = make([]layoutFoundationCanonicalEdge, len(gdef.edges))
	for index, edge := range gdef.edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout_foundation/hash/project/edges"); err != nil {
				return layoutFoundationCanonicalEvidence{}, err
			}
		}
		if edge.tableTag != layoutGDEFTag {
			return layoutFoundationCanonicalEvidence{}, layoutFoundationHashInvariant("layout_foundation/hash/project/edge_tag")
		}
		parent, parentOK := layoutFoundationCanonicalKeyAtOffset(result.nodes, edge.parentStart)
		child, childOK := layoutFoundationCanonicalKeyAtOffset(result.nodes, edge.childStart)
		if !parentOK || !childOK {
			return layoutFoundationCanonicalEvidence{}, layoutFoundationHashInvariant("layout_foundation/hash/project/edge_target")
		}
		result.edges[index] = layoutFoundationCanonicalEdge{
			from: parent, role: uint16(edge.role), ordinal: edge.ordinal, rawOffset: edge.rawOffset, to: child,
		}
	}
	if err := contextFailure(ctx, "layout_foundation/hash/project/complete"); err != nil {
		return layoutFoundationCanonicalEvidence{}, err
	}
	return result, nil
}

func layoutFoundationCanonicalKeyAtOffset(nodes []layoutFoundationCanonicalNode, offset uint64) (
	layoutFoundationCanonicalNodeKey, bool) {
	if offset > uint64(^uint32(0)) {
		return layoutFoundationCanonicalNodeKey{}, false
	}
	// The decoder snapshots GDEF nodes in ascending physical start order.
	index := sort.Search(len(nodes), func(index int) bool {
		return uint64(nodes[index].key.offset) >= offset
	})
	if index >= len(nodes) || uint64(nodes[index].key.offset) != offset {
		return layoutFoundationCanonicalNodeKey{}, false
	}
	return nodes[index].key, true
}

func (value *layoutFoundationHashSink) raw(raw []byte, path string) error {
	next, ok := checkedAdd(value.count, uint64(len(raw)))
	if !ok || next > value.limit {
		return fontError(CodeResourceLimit, "layout_foundation/hash/canonical_bytes", ErrResourceLimit)
	}
	for offset := 0; offset < len(raw); offset += contextStrideBytes {
		if err := contextFailure(value.ctx, path); err != nil {
			return err
		}
		end := offset + contextStrideBytes
		if end > len(raw) {
			end = len(raw)
		}
		_, _ = value.hash.Write(raw[offset:end])
	}
	value.count = next
	return nil
}

func (value *layoutFoundationHashSink) str(text, path string) error {
	if err := value.u64(uint64(len(text)), path+"/length"); err != nil {
		return err
	}
	return value.raw([]byte(text), path+"/bytes")
}

func (value *layoutFoundationHashSink) tag(tag [4]byte, path string) error {
	return value.raw(tag[:], path)
}

func (value *layoutFoundationHashSink) u8(number uint8, path string) error {
	return value.raw([]byte{number}, path)
}

func (value *layoutFoundationHashSink) u16(number uint16, path string) error {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], number)
	return value.raw(encoded[:], path)
}

func (value *layoutFoundationHashSink) u32(number uint32, path string) error {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], number)
	return value.raw(encoded[:], path)
}

func (value *layoutFoundationHashSink) u64(number uint64, path string) error {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], number)
	return value.raw(encoded[:], path)
}

func hashLayoutFoundation(ctx context.Context, evidence layoutFoundationCanonicalEvidence,
	limit uint64) (result string, canonicalBytes uint64, err error) {
	defer func() {
		if recover() != nil {
			result, canonicalBytes = "", 0
			err = fontError(CodeAllocationFailed, "layout_foundation/hash/allocation", ErrAllocationFailed)
		}
	}()
	if err := contextFailure(ctx, "layout_foundation/hash/context"); err != nil {
		return "", 0, err
	}
	if limit == 0 {
		return "", 0, fontError(CodeResourceLimit, "layout_foundation/hash/canonical_bytes", ErrResourceLimit)
	}
	nodes, edges, err := normalizeLayoutFoundationCanonicalEvidence(ctx, evidence)
	if err != nil {
		return "", 0, err
	}
	sink := layoutFoundationHashSink{ctx: ctx, hash: sha256.New(), limit: limit}
	if err := writeLayoutFoundationHashHeader(&sink, evidence); err != nil {
		return "", 0, err
	}
	if err := sink.u64(uint64(len(nodes)), "layout_foundation/hash/nodes/count"); err != nil {
		return "", 0, err
	}
	for index, node := range nodes {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout_foundation/hash/normalize/nodes/validate"); err != nil {
				return "", 0, err
			}
		}
		path := fmt.Sprintf("layout_foundation/hash/nodes/%d", index)
		if err := writeLayoutFoundationNodeKey(&sink, node.key, path+"/key"); err != nil {
			return "", 0, err
		}
		if err := sink.u32(node.spanStart, path+"/span_start"); err != nil {
			return "", 0, err
		}
		if err := sink.u32(node.spanLength, path+"/span_length"); err != nil {
			return "", 0, err
		}
		if err := sink.u64(uint64(len(node.payload)), path+"/payload_length"); err != nil {
			return "", 0, err
		}
		if err := sink.raw(node.payload, path+"/payload"); err != nil {
			return "", 0, err
		}
	}
	if err := sink.u64(uint64(len(edges)), "layout_foundation/hash/edges/count"); err != nil {
		return "", 0, err
	}
	for index, edge := range edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout_foundation/hash/normalize/edges/validate"); err != nil {
				return "", 0, err
			}
		}
		path := fmt.Sprintf("layout_foundation/hash/edges/%d", index)
		if err := writeLayoutFoundationNodeKey(&sink, edge.from, path+"/from"); err != nil {
			return "", 0, err
		}
		if err := sink.u16(edge.role, path+"/role"); err != nil {
			return "", 0, err
		}
		if err := sink.u32(edge.ordinal, path+"/ordinal"); err != nil {
			return "", 0, err
		}
		if err := sink.u32(edge.rawOffset, path+"/raw_offset"); err != nil {
			return "", 0, err
		}
		if err := writeLayoutFoundationNodeKey(&sink, edge.to, path+"/to"); err != nil {
			return "", 0, err
		}
	}
	if err := writeLayoutFoundationResourceFooter(&sink, evidence.resources); err != nil {
		return "", 0, err
	}
	if err := contextFailure(ctx, "layout_foundation/hash/finalize"); err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(sink.hash.Sum(nil)), sink.count, nil
}

func writeLayoutFoundationHashHeader(sink *layoutFoundationHashSink,
	evidence layoutFoundationCanonicalEvidence) error {
	source := evidence.source
	writes := []func() error{
		func() error { return sink.str(layoutFoundationCanonicalHashV1, "layout_foundation/hash/hash_profile") },
		func() error { return sink.u32(layoutFoundationSchemaVersion, "layout_foundation/hash/schema") },
		func() error { return sink.str(layoutFoundationProfileV1, "layout_foundation/hash/profile") },
		func() error {
			return sink.str(layoutFoundationDescriptorIdentityV1, "layout_foundation/hash/descriptor_hash")
		},
		func() error { return sink.str(source.outlineSetProfile, "layout_foundation/hash/source/profile") },
		func() error {
			return sink.str(source.outlineSetDescriptorHashProfile, "layout_foundation/hash/source/descriptor_hash_profile")
		},
		func() error {
			return sink.str(source.outlineSetDescriptorHash, "layout_foundation/hash/source/descriptor_hash")
		},
		func() error {
			return sink.str(source.outlineSetHashProfile, "layout_foundation/hash/source/outline_set_hash_profile")
		},
		func() error { return sink.str(source.outlineSetHash, "layout_foundation/hash/source/outline_set_hash") },
		func() error {
			return sink.str(source.fontContentHashProfile, "layout_foundation/hash/source/content_hash_profile")
		},
		func() error { return sink.str(source.fontContentHash, "layout_foundation/hash/source/content_hash") },
		func() error { return sink.u32(source.glyphCount, "layout_foundation/hash/source/glyph_count") },
		func() error { return sink.u64(source.realPointInstances, "layout_foundation/hash/source/real_points") },
	}
	for _, write := range writes {
		if err := write(); err != nil {
			return err
		}
	}
	for index, table := range evidence.tables {
		path := fmt.Sprintf("layout_foundation/hash/tables/%d", index)
		if err := sink.tag(table.tag, path+"/tag"); err != nil {
			return err
		}
		present := uint8(0)
		if table.present {
			present = 1
		}
		if err := sink.u8(present, path+"/present"); err != nil {
			return err
		}
		if err := sink.u32(table.fileOffset, path+"/file_offset"); err != nil {
			return err
		}
		if err := sink.u32(table.length, path+"/length"); err != nil {
			return err
		}
		if err := sink.u32(table.checksum, path+"/checksum"); err != nil {
			return err
		}
		if err := sink.str(table.hashProfile, path+"/hash_profile"); err != nil {
			return err
		}
		if err := sink.str(table.contentHash, path+"/content_hash"); err != nil {
			return err
		}
	}
	present := uint8(0)
	if evidence.gdefPresent {
		present = 1
	}
	if err := sink.u8(present, "layout_foundation/hash/gdef/present"); err != nil {
		return err
	}
	if err := sink.u16(evidence.gdefMajorVersion, "layout_foundation/hash/gdef/major"); err != nil {
		return err
	}
	return sink.u16(evidence.gdefMinorVersion, "layout_foundation/hash/gdef/minor")
}

func writeLayoutFoundationNodeKey(sink *layoutFoundationHashSink,
	key layoutFoundationCanonicalNodeKey, path string) error {
	if err := sink.tag(key.tag, path+"/tag"); err != nil {
		return err
	}
	if err := sink.u32(key.offset, path+"/offset"); err != nil {
		return err
	}
	if err := sink.u16(key.kind, path+"/kind"); err != nil {
		return err
	}
	if err := sink.u64(key.param0, path+"/param0"); err != nil {
		return err
	}
	return sink.u64(key.param1, path+"/param1")
}

func writeLayoutFoundationResourceFooter(sink *layoutFoundationHashSink,
	stats layoutFoundationResourceStats) error {
	values := []uint64{
		stats.layoutTableBytes, stats.nodes, stats.edges, stats.aliasReferences, stats.directBytes,
		stats.coverageTables, stats.coverageFormat1Glyphs, stats.coverageRanges, stats.coveredGlyphs,
		stats.classDefTables, stats.classFormat1Values, stats.classRanges, stats.classAssignments,
		stats.deviceTables, stats.deviceDeltaValues, stats.deviceWords,
		stats.attachLists, stats.attachPointTables, stats.attachmentGlyphReferences, stats.attachmentPoints,
		stats.ligCaretLists, stats.ligGlyphTables, stats.ligatureGlyphReferences,
		stats.caretValueTables, stats.caretValueReferences, stats.caretPointReferences,
		stats.markGlyphSetsTables, stats.markGlyphSetReferences,
	}
	if err := sink.u32(stats.maxStructuralDepth, "layout_foundation/hash/resources/max_depth"); err != nil {
		return err
	}
	for index, value := range values {
		if err := sink.u64(value, fmt.Sprintf("layout_foundation/hash/resources/%d", index)); err != nil {
			return err
		}
	}
	return nil
}

func normalizeLayoutFoundationCanonicalEvidence(ctx context.Context, evidence layoutFoundationCanonicalEvidence) (
	[]layoutFoundationCanonicalNode, []layoutFoundationCanonicalEdge, error) {
	if err := validateLayoutFoundationCanonicalHeader(evidence); err != nil {
		return nil, nil, err
	}
	if err := contextFailure(ctx, "layout_foundation/hash/normalize/start"); err != nil {
		return nil, nil, err
	}
	if err := contextFailure(ctx, "layout_foundation/hash/normalize/nodes/allocate"); err != nil {
		return nil, nil, err
	}
	nodes := append([]layoutFoundationCanonicalNode(nil), evidence.nodes...)
	if err := contextFailure(ctx, "layout_foundation/hash/normalize/edges/allocate"); err != nil {
		return nil, nil, err
	}
	edges := append([]layoutFoundationCanonicalEdge(nil), evidence.edges...)
	if err := contextFailure(ctx, "layout_foundation/hash/normalize/copied"); err != nil {
		return nil, nil, err
	}
	if err := layoutCancelableSort(ctx, "layout_foundation/hash/normalize/nodes/sort", nodes, func(left, right int) bool {
		return compareLayoutFoundationNodeKey(nodes[left].key, nodes[right].key) < 0
	}); err != nil {
		return nil, nil, err
	}
	if err := contextFailure(ctx, "layout_foundation/hash/normalize/nodes_sorted"); err != nil {
		return nil, nil, err
	}
	for index, node := range nodes {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout_foundation/hash/normalize/nodes/validate"); err != nil {
				return nil, nil, err
			}
		}
		if index > 0 && compareLayoutFoundationNodeKey(nodes[index-1].key, node.key) == 0 {
			return nil, nil, layoutFoundationHashInvariant("layout_foundation/hash/nodes/duplicate")
		}
		if node.key.tag != layoutFoundationTableTags[0] || node.spanStart != node.key.offset || node.spanLength == 0 {
			return nil, nil, layoutFoundationHashInvariant("layout_foundation/hash/nodes/span")
		}
		end, ok := checkedAdd(uint64(node.spanStart), uint64(node.spanLength))
		if !ok || end > uint64(evidence.tables[0].length) {
			return nil, nil, layoutFoundationHashInvariant("layout_foundation/hash/nodes/bounds")
		}
		if index > 0 {
			previousEnd := uint64(nodes[index-1].spanStart) + uint64(nodes[index-1].spanLength)
			if uint64(node.spanStart) < previousEnd {
				return nil, nil, layoutFoundationHashInvariant("layout_foundation/hash/nodes/overlap")
			}
		}
	}
	if err := layoutCancelableSort(ctx, "layout_foundation/hash/normalize/edges/sort", edges,
		func(left, right int) bool { return compareLayoutFoundationEdge(edges[left], edges[right]) < 0 }); err != nil {
		return nil, nil, err
	}
	if err := contextFailure(ctx, "layout_foundation/hash/normalize/edges_sorted"); err != nil {
		return nil, nil, err
	}
	for index, edge := range edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout_foundation/hash/normalize/edges/validate"); err != nil {
				return nil, nil, err
			}
		}
		if index > 0 && compareLayoutFoundationEdge(edges[index-1], edge) == 0 {
			return nil, nil, layoutFoundationHashInvariant("layout_foundation/hash/edges/duplicate")
		}
		if edge.rawOffset == 0 || !layoutFoundationNodeKeyExists(nodes, edge.from) || !layoutFoundationNodeKeyExists(nodes, edge.to) {
			return nil, nil, layoutFoundationHashInvariant("layout_foundation/hash/edges/target")
		}
	}
	aliases, err := layoutFoundationCanonicalAliasCount(ctx, edges)
	if err != nil {
		return nil, nil, err
	}
	if uint64(len(nodes)) != evidence.resources.nodes || uint64(len(edges)) != evidence.resources.edges ||
		aliases != evidence.resources.aliasReferences {
		return nil, nil, layoutFoundationHashInvariant("layout_foundation/hash/resources/graph_binding")
	}
	return nodes, edges, nil
}

func validateLayoutFoundationCanonicalHeader(evidence layoutFoundationCanonicalEvidence) error {
	source := evidence.source
	if source.glyphCount == 0 || source.outlineSetProfile == "" || source.outlineSetDescriptorHashProfile == "" ||
		!validLayoutFoundationDigest(source.outlineSetDescriptorHash) || source.outlineSetHashProfile == "" ||
		!validLayoutFoundationDigest(source.outlineSetHash) || source.fontContentHashProfile == "" ||
		!validLayoutFoundationDigest(source.fontContentHash) {
		return layoutFoundationHashInvariant("layout_foundation/hash/source")
	}
	for index, table := range evidence.tables {
		if table.tag != layoutFoundationTableTags[index] {
			return layoutFoundationHashInvariant("layout_foundation/hash/tables/order")
		}
		if table.present {
			if table.hashProfile != TableHashProfileV1 || !validLayoutFoundationDigest(table.contentHash) {
				return layoutFoundationHashInvariant("layout_foundation/hash/tables/binding")
			}
		} else if table.fileOffset != 0 || table.length != 0 || table.checksum != 0 || table.hashProfile != "" || table.contentHash != "" {
			return layoutFoundationHashInvariant("layout_foundation/hash/tables/absent")
		}
	}
	if evidence.gdefPresent != evidence.tables[0].present {
		return layoutFoundationHashInvariant("layout_foundation/hash/gdef/presence")
	}
	if !evidence.gdefPresent {
		if evidence.gdefMajorVersion != 0 || evidence.gdefMinorVersion != 0 || len(evidence.nodes) != 0 || len(evidence.edges) != 0 {
			return layoutFoundationHashInvariant("layout_foundation/hash/gdef/absent")
		}
		return nil
	}
	if evidence.gdefMajorVersion != 1 ||
		(evidence.gdefMinorVersion != 0 && evidence.gdefMinorVersion != 2 && evidence.gdefMinorVersion != 3) {
		return layoutFoundationHashInvariant("layout_foundation/hash/gdef/version")
	}
	return nil
}

func layoutFoundationNodeKeyExists(nodes []layoutFoundationCanonicalNode, key layoutFoundationCanonicalNodeKey) bool {
	index := sort.Search(len(nodes), func(index int) bool {
		return compareLayoutFoundationNodeKey(nodes[index].key, key) >= 0
	})
	return index < len(nodes) && compareLayoutFoundationNodeKey(nodes[index].key, key) == 0
}

func layoutFoundationCanonicalAliasCount(ctx context.Context, edges []layoutFoundationCanonicalEdge) (uint64, error) {
	if len(edges) < 2 {
		return 0, contextFailure(ctx, "layout_foundation/hash/aliases")
	}
	if err := contextFailure(ctx, "layout_foundation/hash/aliases/start"); err != nil {
		return 0, err
	}
	targets := make([]layoutFoundationCanonicalNodeKey, len(edges))
	for index, edge := range edges {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout_foundation/hash/aliases/fill"); err != nil {
				return 0, err
			}
		}
		targets[index] = edge.to
	}
	if err := contextFailure(ctx, "layout_foundation/hash/aliases/copied"); err != nil {
		return 0, err
	}
	if err := layoutCancelableSort(ctx, "layout_foundation/hash/aliases/sort", targets, func(left, right int) bool {
		return compareLayoutFoundationNodeKey(targets[left], targets[right]) < 0
	}); err != nil {
		return 0, err
	}
	if err := contextFailure(ctx, "layout_foundation/hash/aliases/sorted"); err != nil {
		return 0, err
	}
	var aliases uint64
	for index := 1; index < len(targets); index++ {
		if index%layoutContextCheckStride == 0 {
			if err := contextFailure(ctx, "layout_foundation/hash/aliases/count"); err != nil {
				return 0, err
			}
		}
		if compareLayoutFoundationNodeKey(targets[index-1], targets[index]) == 0 {
			aliases++
		}
	}
	return aliases, nil
}

func compareLayoutFoundationNodeKey(left, right layoutFoundationCanonicalNodeKey) int {
	for index := range left.tag {
		if left.tag[index] < right.tag[index] {
			return -1
		}
		if left.tag[index] > right.tag[index] {
			return 1
		}
	}
	leftValues := [...]uint64{uint64(left.offset), uint64(left.kind), left.param0, left.param1}
	rightValues := [...]uint64{uint64(right.offset), uint64(right.kind), right.param0, right.param1}
	for index := range leftValues {
		if leftValues[index] < rightValues[index] {
			return -1
		}
		if leftValues[index] > rightValues[index] {
			return 1
		}
	}
	return 0
}

func compareLayoutFoundationEdge(left, right layoutFoundationCanonicalEdge) int {
	if comparison := compareLayoutFoundationNodeKey(left.from, right.from); comparison != 0 {
		return comparison
	}
	leftValues := [...]uint64{uint64(left.role), uint64(left.ordinal), uint64(left.rawOffset)}
	rightValues := [...]uint64{uint64(right.role), uint64(right.ordinal), uint64(right.rawOffset)}
	for index := range leftValues {
		if leftValues[index] < rightValues[index] {
			return -1
		}
		if leftValues[index] > rightValues[index] {
			return 1
		}
	}
	return compareLayoutFoundationNodeKey(left.to, right.to)
}

func validLayoutFoundationDigest(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || value[:len("sha256:")] != "sha256:" {
		return false
	}
	payload := value[len("sha256:"):]
	if payload != strings.ToLower(payload) {
		return false
	}
	_, err := hex.DecodeString(payload)
	return err == nil
}

func layoutFoundationHashInvariant(path string) error {
	return fontError(CodeInvariantFailed, path, ErrInvariantFailed)
}
