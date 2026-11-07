package sfntglyf

import (
	"context"
	"encoding/binary"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"
)

type geometryTestBBox struct {
	xMin, yMin, xMax, yMax int16
}

func TestValidateOutlineGeometryMinimalEmptyAndConcurrentReads(t *testing.T) {
	outline := decodeGeometryTestOutline(t, outlineTestTables(t, [][]byte{nil, nil}, outlineTestMaxp{}))
	geometry, receipt, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if geometry == nil || receipt.Profile != GeometryReceiptProfileV1 || receipt.GeometryHash == "" ||
		receipt.GeometryHashProfile != GeometryHashProfileV1 || receipt.DescriptorHash != GeometryDescriptorHash() ||
		receipt.SourceCanonicalBinaryBytes != outline.receipt.CanonicalBinaryBytes ||
		receipt.GlyphCount != 2 || receipt.GlyphsWithContours != 0 || receipt.ExactOpCount != 0 ||
		receipt.HeadXMin != 0 || receipt.HeadYMin != 0 || receipt.HeadXMax != 0 || receipt.HeadYMax != 0 ||
		receipt.MinLSB != 0 || receipt.MinRSB != 0 || receipt.XExtent != 0 ||
		receipt.GeometryStatus != GeometryValidatedStatusV1 || receipt.CommandStatus != GeometryCommandStatusV1 ||
		receipt.CapabilityStatus != GeometryCapabilityStatusV1 || receipt.HeadLSBStatus != GeometryHeadLSBUnsetV1 {
		t.Fatalf("receipt=%+v", receipt)
	}
	if geometry.Receipt() != receipt || geometry.GlyphCount() != 2 {
		t.Fatal("geometry accessors drift")
	}
	for glyphID := uint32(0); glyphID < 2; glyphID++ {
		info, ok := geometry.GlyphInfo(glyphID)
		if !ok || info.Kind != OutlineGlyphEmptyRange || info.HasContours || info.EffectiveMetricSource != glyphID {
			t.Fatalf("gid=%d info=%+v ok=%t", glyphID, info, ok)
		}
	}
	if _, ok := geometry.GlyphInfo(2); ok {
		t.Fatal("out-of-range geometry glyph accepted")
	}

	// Published scalar metadata does not alias the retained phase-one records.
	before, _ := geometry.GlyphInfo(0)
	outline.glyphs[0].kind = OutlineGlyphCompound
	after, _ := geometry.GlyphInfo(0)
	if after != before {
		t.Fatalf("source mutation leaked into geometry metadata: before=%+v after=%+v", before, after)
	}

	const readers = 32
	var wait sync.WaitGroup
	wait.Add(readers)
	for index := 0; index < readers; index++ {
		go func() {
			defer wait.Done()
			if geometry.Receipt() != receipt {
				t.Error("concurrent receipt drift")
			}
			if info, ok := geometry.GlyphInfo(1); !ok || info.Kind != OutlineGlyphEmptyRange {
				t.Errorf("concurrent info=%+v ok=%t", info, ok)
			}
		}()
	}
	wait.Wait()
}

func TestGeometryNoContourDeclarationsMustBeZero(t *testing.T) {
	t.Run("head bbox", func(t *testing.T) {
		tables := outlineTestTables(t, [][]byte{nil, nil}, outlineTestMaxp{})
		binary.BigEndian.PutUint16(tables["head"][40:42], 1)
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, tables), GeometryLimits{})
		if !errors.Is(err, ErrGeometryInvalid) || geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
	})
	t.Run("hhea extrema", func(t *testing.T) {
		tables := outlineTestTables(t, [][]byte{nil, nil}, outlineTestMaxp{})
		binary.BigEndian.PutUint16(tables["hhea"][12:14], 1)
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, tables), GeometryLimits{})
		if !errors.Is(err, ErrGeometryMetrics) || geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
	})
}

func TestValidateOutlineGeometrySimpleHeadAndHhea(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{
		{x: -2, y: -3, onCurve: true}, {x: 10, y: 7, onCurve: false},
	}, nil, false, 0)
	tables := outlineTestTables(t, [][]byte{nil, simple}, outlineTestMaxp{points: 2, contours: 1})
	setGeometryDeclarations(tables, geometryTestBBox{-2, -3, 10, 7}, 0, 488, 12)
	geometry, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, tables), GeometryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	info, ok := geometry.GlyphInfo(1)
	if !ok || info.Kind != OutlineGlyphSimple || !info.HasContours ||
		info.XMin != -2 || info.YMin != -3 || info.XMax != 10 || info.YMax != 7 ||
		info.AdvanceWidth != 500 || info.LeftSideBearing != 0 || info.EffectiveMetricSource != 1 {
		t.Fatalf("info=%+v ok=%t", info, ok)
	}
	if receipt.GlyphsWithContours != 1 || receipt.HeadXMin != -2 || receipt.HeadYMin != -3 ||
		receipt.HeadXMax != 10 || receipt.HeadYMax != 7 || receipt.MinLSB != 0 ||
		receipt.MinRSB != 488 || receipt.XExtent != 12 || receipt.ExactOpCount == 0 ||
		receipt.MaxObservedExactBits == 0 {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestGeometryExpansionScaledUnscaledAndDefaultOffsets(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 2, y: 4, onCurve: true}}, nil, false, 0)
	scaled := makeOutlineCompoundGlyph([]outlineTestComponent{{
		flags: componentArgsXY | componentScale | componentScaledOffset, glyphID: 1,
		arg1: 2, arg2: 2, matrix: []int16{0x2000},
	}}, nil, 0)
	unscaled := makeOutlineCompoundGlyph([]outlineTestComponent{{
		flags: componentArgsXY | componentScale | componentUnscaledOffset, glyphID: 1,
		arg1: 2, arg2: 2, matrix: []int16{0x2000},
	}}, nil, 0)
	defaulted := makeOutlineCompoundGlyph([]outlineTestComponent{{
		flags: componentArgsXY | componentScale, glyphID: 1,
		arg1: 2, arg2: 2, matrix: []int16{0x2000},
	}}, nil, 0)
	setGeometryGlyphBBox(scaled, geometryTestBBox{2, 3, 2, 3})
	setGeometryGlyphBBox(unscaled, geometryTestBBox{3, 4, 3, 4})
	setGeometryGlyphBBox(defaulted, geometryTestBBox{3, 4, 3, 4})
	tables := outlineTestTables(t, [][]byte{nil, simple, scaled, unscaled, defaulted}, outlineTestMaxp{
		points: 1, contours: 1, compositePoints: 1, compositeContours: 1, componentElements: 1, depth: 1,
	})
	setGeometryDeclarations(tables, geometryTestBBox{2, 3, 3, 4}, 0, 500, 0)
	outline := decodeGeometryTestOutline(t, tables)
	if _, _, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{}); err != nil {
		t.Fatal(err)
	}
	wants := map[uint32][2]*big.Rat{
		2: {big.NewRat(2, 1), big.NewRat(3, 1)},
		3: {big.NewRat(3, 1), big.NewRat(4, 1)},
		4: {big.NewRat(3, 1), big.NewRat(4, 1)},
	}
	for glyphID, want := range wants {
		expanded := expandGeometryForTest(t, outline, glyphID)
		if len(expanded.points) != 1 {
			t.Fatalf("gid=%d points=%d", glyphID, len(expanded.points))
		}
		assertGeometryRat(t, expanded.points[0].x, want[0])
		assertGeometryRat(t, expanded.points[0].y, want[1])
	}
}

func TestGeometryExpansionMatrixDiskOrderAndFractionalPointAttachment(t *testing.T) {
	t.Run("off diagonal disk order", func(t *testing.T) {
		simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 2, y: 3, onCurve: true}}, nil, false, 0)
		compound := makeOutlineCompoundGlyph([]outlineTestComponent{{
			flags: componentArgsXY | componentTwoByTwo, glyphID: 1,
			matrix: []int16{0, 0x4000, 0x2000, 0},
		}}, nil, 0)
		setGeometryGlyphBBox(compound, geometryTestBBox{1, 2, 2, 2})
		tables := outlineTestTables(t, [][]byte{nil, simple, compound}, outlineTestMaxp{
			points: 1, contours: 1, compositePoints: 1, compositeContours: 1, componentElements: 1, depth: 1,
		})
		setGeometryDeclarations(tables, geometryTestBBox{1, 2, 2, 3}, 0, 499, 1)
		outline := decodeGeometryTestOutline(t, tables)
		if _, _, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{}); err != nil {
			t.Fatal(err)
		}
		point := expandGeometryForTest(t, outline, 2).points[0]
		assertGeometryRat(t, point.x, big.NewRat(3, 2))
		assertGeometryRat(t, point.y, big.NewRat(2, 1))
	})

	t.Run("fractional real point attachment", func(t *testing.T) {
		simple := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{
			{x: 0, y: 0, onCurve: true}, {x: 3, y: 0, onCurve: false},
		}, nil, false, 0)
		compound := makeOutlineCompoundGlyph([]outlineTestComponent{
			{flags: componentArgsXY, glyphID: 1},
			{flags: componentScale, glyphID: 1, arg1: 1, arg2: 1, matrix: []int16{0x2000}},
		}, nil, 0)
		setGeometryGlyphBBox(compound, geometryTestBBox{0, 0, 3, 0})
		tables := outlineTestTables(t, [][]byte{nil, simple, compound}, outlineTestMaxp{
			points: 2, contours: 1, compositePoints: 4, compositeContours: 2, componentElements: 2, depth: 1,
		})
		setGeometryDeclarations(tables, geometryTestBBox{0, 0, 3, 0}, 0, 497, 3)
		outline := decodeGeometryTestOutline(t, tables)
		if _, _, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{}); err != nil {
			t.Fatal(err)
		}
		points := expandGeometryForTest(t, outline, 2).points
		if len(points) != 4 {
			t.Fatalf("points=%d", len(points))
		}
		wants := []*big.Rat{big.NewRat(0, 1), big.NewRat(3, 1), big.NewRat(3, 2), big.NewRat(3, 1)}
		for index, want := range wants {
			assertGeometryRat(t, points[index].x, want)
			assertGeometryRat(t, points[index].y, big.NewRat(0, 1))
		}
	})
}

func TestGeometryNestedTransformExceeds64BitNumerator(t *testing.T) {
	bodies := [][]byte{nil, makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 1, y: 0, onCurve: true}}, nil, false, 0)}
	for depth := 1; depth <= 5; depth++ {
		body := makeOutlineCompoundGlyph([]outlineTestComponent{{
			flags: componentArgsXY | componentScale, glyphID: uint16(depth), matrix: []int16{0x7fff},
		}}, nil, 0)
		setGeometryGlyphBBox(body, geometryTestBBox{int16((1 << depth) - 1), 0, int16(1 << depth), 0})
		bodies = append(bodies, body)
	}
	tables := outlineTestTables(t, bodies, outlineTestMaxp{
		points: 1, contours: 1, compositePoints: 1, compositeContours: 1, componentElements: 1, depth: 5,
	})
	setGeometryDeclarations(tables, geometryTestBBox{1, 0, 32, 0}, 0, 499, 1)
	outline := decodeGeometryTestOutline(t, tables)
	_, receipt, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.MaxObservedExactBits <= 64 || receipt.MaxComponentDepth != 5 {
		t.Fatalf("receipt=%+v", receipt)
	}
	point := expandGeometryForTest(t, outline, 6).points[0]
	wantNumerator := new(big.Int).Exp(big.NewInt(0x7fff), big.NewInt(5), nil)
	wantDenominator := new(big.Int).Lsh(big.NewInt(1), 70)
	assertGeometryRat(t, point.x, new(big.Rat).SetFrac(wantNumerator, wantDenominator))
}

func TestGeometryReflectionSkewAndSingularMatrices(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 2, y: 3, onCurve: true}}, nil, false, 0)
	reflection := makeOutlineCompoundGlyph([]outlineTestComponent{{
		flags: componentArgsXY | componentXYScale, glyphID: 1, matrix: []int16{-0x4000, 0x4000},
	}}, nil, 0)
	skew := makeOutlineCompoundGlyph([]outlineTestComponent{{
		flags: componentArgsXY | componentTwoByTwo, glyphID: 1,
		matrix: []int16{0x4000, 0x2000, -0x2000, 0x4000},
	}}, nil, 0)
	singular := makeOutlineCompoundGlyph([]outlineTestComponent{{
		flags: componentArgsXY | componentTwoByTwo, glyphID: 1,
		matrix: []int16{0x4000, 0x4000, 0x4000, 0x4000},
	}}, nil, 0)
	setGeometryGlyphBBox(reflection, geometryTestBBox{-2, 3, -2, 3})
	setGeometryGlyphBBox(skew, geometryTestBBox{0, 4, 1, 4})
	setGeometryGlyphBBox(singular, geometryTestBBox{5, 5, 5, 5})
	tables := outlineTestTables(t, [][]byte{nil, simple, reflection, skew, singular}, outlineTestMaxp{
		points: 1, contours: 1, compositePoints: 1, compositeContours: 1, componentElements: 1, depth: 1,
	})
	setGeometryDeclarations(tables, geometryTestBBox{-2, 3, 5, 5}, 0, 499, 1)
	outline := decodeGeometryTestOutline(t, tables)
	if _, _, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{}); err != nil {
		t.Fatal(err)
	}
	wants := map[uint32][2]*big.Rat{
		2: {big.NewRat(-2, 1), big.NewRat(3, 1)},
		3: {big.NewRat(1, 2), big.NewRat(4, 1)},
		4: {big.NewRat(5, 1), big.NewRat(5, 1)},
	}
	for glyphID, want := range wants {
		point := expandGeometryForTest(t, outline, glyphID).points[0]
		assertGeometryRat(t, point.x, want[0])
		assertGeometryRat(t, point.y, want[1])
	}
}

func TestGeometryEmptyXYChildAndCompoundBBoxMismatch(t *testing.T) {
	t.Run("empty child is valid for XY", func(t *testing.T) {
		emptySimple := makeOutlineSimpleGlyph(nil, nil, nil, false, 0)
		compound := makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 1}}, nil, 0)
		tables := outlineTestTables(t, [][]byte{nil, emptySimple, compound}, outlineTestMaxp{componentElements: 1, depth: 1})
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, tables), GeometryLimits{})
		if err != nil {
			t.Fatal(err)
		}
		info, _ := geometry.GlyphInfo(2)
		if info.HasContours || info.XMin != 0 || info.YMin != 0 || info.XMax != 0 || info.YMax != 0 || receipt.GlyphsWithContours != 0 {
			t.Fatalf("info=%+v receipt=%+v", info, receipt)
		}
	})

	t.Run("compound header mismatch", func(t *testing.T) {
		simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 1, y: 1, onCurve: true}}, nil, false, 0)
		compound := makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 1}}, nil, 0)
		// Header remains all-zero although exact transformed control geometry is (1,1).
		tables := outlineTestTables(t, [][]byte{nil, simple, compound}, outlineTestMaxp{
			points: 1, contours: 1, compositePoints: 1, compositeContours: 1, componentElements: 1, depth: 1,
		})
		setGeometryDeclarations(tables, geometryTestBBox{1, 1, 1, 1}, 0, 500, 0)
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, tables), GeometryLimits{})
		if !errors.Is(err, ErrGeometryInvalid) || geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
	})
}

func TestGeometryZeroControlExpansionInstancesArePreflighted(t *testing.T) {
	emptySimple := makeOutlineSimpleGlyph(nil, nil, nil, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY, glyphID: 1},
		{flags: componentArgsXY, glyphID: 1},
	}, nil, 0)
	tables := outlineTestTables(t, [][]byte{nil, emptySimple, compound}, outlineTestMaxp{componentElements: 2, depth: 1})
	outline := decodeGeometryTestOutline(t, tables)
	_, receipt, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ExpandedGlyphInstances != 5 || receipt.MaxExpandedGlyphInstancesPerRoot != 3 || receipt.TransformPointVisits != 0 {
		t.Fatalf("receipt=%+v", receipt)
	}
	geometry, failedReceipt, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{MaxExpandedGlyphInstances: 4})
	if !errors.Is(err, ErrResourceLimit) || geometry != nil || failedReceipt != (GeometryReceipt{}) {
		t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, failedReceipt, err)
	}
}

func TestGeometryWorkBudgetFailsAtEarliestKnownStage(t *testing.T) {
	minimums := func(t *testing.T, outline *OutlineFont, topology geometryTopologyStats) (uint64, uint64) {
		t.Helper()
		canonicalLower, ok := geometryCanonicalLowerBound(outline, GeometryHeadLSBUnsetV1)
		if !ok {
			t.Fatal("canonical lower-bound overflow")
		}
		exactLower, ok := checkedMultiply(outline.receipt.ExpandedPointInstances, 2)
		componentLower, componentOK := checkedMultiply(outline.receipt.ExpandedEdgeVisits, 4)
		if ok && componentOK {
			exactLower, ok = checkedAdd(exactLower, componentLower)
		}
		fixed, fixedOK := geometryWorkBase(outline, topology, 0, 0)
		exactWeight, weightOK := checkedMultiply(exactLower, 9)
		if !ok || !fixedOK || !weightOK {
			t.Fatal("work lower-bound overflow")
		}
		minimum, ok := checkedAdd(fixed, exactWeight)
		if ok {
			minimum, ok = checkedAdd(minimum, canonicalLower)
		}
		if !ok {
			t.Fatal("work lower-bound sum overflow")
		}
		return minimum, canonicalLower
	}
	resourcePath := func(t *testing.T, err error) string {
		t.Helper()
		var typed *Error
		if !errors.As(err, &typed) || !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("error=%v, want typed resource limit", err)
		}
		return typed.Path
	}

	t.Run("source graph rebind", func(t *testing.T) {
		outline := decodeGeometryTestOutline(t, outlineTestTables(t, [][]byte{nil, nil}, outlineTestMaxp{}))
		withSourceRebind, ok := geometrySourceRebindWorkLowerBound(outline)
		if !ok || outline.receipt.CanonicalBinaryBytes == 0 || withSourceRebind <= outline.receipt.CanonicalBinaryBytes {
			t.Fatalf("source admission=%d source canonical bytes=%d ok=%t", withSourceRebind, outline.receipt.CanonicalBinaryBytes, ok)
		}
		withoutSourceRebind := withSourceRebind - outline.receipt.CanonicalBinaryBytes
		budget := withSourceRebind - 1
		if budget < withoutSourceRebind {
			t.Fatalf("R gap collapsed: without=%d budget=%d with=%d", withoutSourceRebind, budget, withSourceRebind)
		}
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{
			MaxWorkUnits: budget,
		})
		if geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
		if path := resourcePath(t, err); path != "geometry/resources/source_rebind_work_lower_bound" {
			t.Fatalf("path=%q", path)
		}
	})

	t.Run("topology transform visits", func(t *testing.T) {
		simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 3, y: 0, onCurve: true}}, nil, false, 0)
		compound := makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY, glyphID: 1}}, nil, 0)
		setGeometryGlyphBBox(compound, geometryTestBBox{3, 0, 3, 0})
		tables := outlineTestTables(t, [][]byte{nil, simple, compound}, outlineTestMaxp{
			points: 1, contours: 1, compositePoints: 1, compositeContours: 1, componentElements: 1, depth: 1,
		})
		setGeometryDeclarations(tables, geometryTestBBox{3, 0, 3, 0}, 0, 500, 0)
		outline := decodeGeometryTestOutline(t, tables)
		limits := DefaultGeometryLimits()
		topology, err := computeGeometryTopology(context.Background(), outline, limits)
		if err != nil {
			t.Fatal(err)
		}
		topologyMinimum, _ := minimums(t, outline, topology)
		preflight := topology
		preflight.transformPointVisits = 0
		preflightMinimum, _ := minimums(t, outline, preflight)
		if topology.transformPointVisits == 0 || topologyMinimum <= preflightMinimum || topologyMinimum-1 < preflightMinimum {
			t.Fatalf("invalid topology work gap: T=%d preflight=%d topology=%d", topology.transformPointVisits, preflightMinimum, topologyMinimum)
		}
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{MaxWorkUnits: topologyMinimum - 1})
		if geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
		if path := resourcePath(t, err); path != "geometry/resources/work_topology_lower_bound" {
			t.Fatalf("path=%q", path)
		}
	})

	t.Run("incremental exact and canonical", func(t *testing.T) {
		simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 3, y: 0, onCurve: true}}, nil, false, 0)
		tables := outlineTestTables(t, [][]byte{nil, simple}, outlineTestMaxp{points: 1, contours: 1})
		setGeometryDeclarations(tables, geometryTestBBox{3, 0, 3, 0}, 0, 500, 0)
		outline := decodeGeometryTestOutline(t, tables)
		topology, err := computeGeometryTopology(context.Background(), outline, DefaultGeometryLimits())
		if err != nil {
			t.Fatal(err)
		}
		topologyMinimum, canonicalLower := minimums(t, outline, topology)
		_, baseline, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
		if err != nil {
			t.Fatal(err)
		}

		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{MaxWorkUnits: topologyMinimum})
		if geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("exact budget geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
		if path := resourcePath(t, err); !strings.HasSuffix(path, "/work/exact_operations") && !strings.HasSuffix(path, "/work/exact_limbs") {
			t.Fatalf("exact budget path=%q", path)
		}

		fixed, ok := geometryWorkBase(outline, topology, 0, 0)
		if !ok {
			t.Fatal("fixed work overflow")
		}
		hashBudget, ok := outlineWeightedSum(fixed,
			[2]uint64{baseline.ExactOpCount, 8},
			[2]uint64{baseline.ExactLimbUnits, 1},
			[2]uint64{canonicalLower, 1},
		)
		if !ok || hashBudget >= baseline.WorkUnits || canonicalLower >= baseline.CanonicalBinaryBytes {
			t.Fatalf("invalid hash work gap: budget=%d receipt=%+v canonicalLower=%d", hashBudget, baseline, canonicalLower)
		}
		geometry, receipt, err = ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{MaxWorkUnits: hashBudget})
		if geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("hash budget geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
		if path := resourcePath(t, err); !strings.HasPrefix(path, "geometry/hash/") || !strings.HasSuffix(path, "/work/canonical_bytes") {
			t.Fatalf("hash budget path=%q", path)
		}
	})
}

func TestGeometryDeepZeroControlDAGPreflight(t *testing.T) {
	const depth = 20
	bodies := [][]byte{nil, makeOutlineSimpleGlyph(nil, nil, nil, false, 0)}
	var rootEdges, allRootEdges uint64
	for level := 0; level < depth; level++ {
		child := uint16(len(bodies) - 1)
		bodies = append(bodies, makeOutlineCompoundGlyph([]outlineTestComponent{
			{flags: componentArgsXY, glyphID: child},
			{flags: componentArgsXY, glyphID: child},
		}, nil, 0))
		rootEdges = 2 + 2*rootEdges
		allRootEdges += rootEdges
	}
	tables := outlineTestTables(t, bodies, outlineTestMaxp{componentElements: 2, depth: depth})
	font := parseOutlineTestFont(t, tables)
	outline, outlineReceipt, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{
		MaxExpandedEdgesPerGlyph: rootEdges,
		MaxExpandedEdgeVisits:    allRootEdges,
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedInstances, ok := checkedAdd(uint64(len(bodies)), outlineReceipt.ExpandedEdgeVisits)
	if !ok || outlineReceipt.ExpandedEdgeVisits != allRootEdges {
		t.Fatalf("phase-one edges=%d want=%d", outlineReceipt.ExpandedEdgeVisits, allRootEdges)
	}
	limits := DefaultGeometryLimits()
	limits.MaxExpandedGlyphInstances = expectedInstances
	topology, err := computeGeometryTopology(context.Background(), outline, limits)
	if err != nil {
		t.Fatal(err)
	}
	if topology.expandedGlyphInstances != expectedInstances || topology.maxExpandedGlyphsPerRoot != rootEdges+1 || topology.transformPointVisits != 0 {
		t.Fatalf("topology=%+v expected I=%d max-root=%d", topology, expectedInstances, rootEdges+1)
	}
	limits.MaxExpandedGlyphInstances = expectedInstances - 1
	if _, err := computeGeometryTopology(context.Background(), outline, limits); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("topology N-1 error=%v", err)
	}

	geometry, receipt, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{MaxExpandedGlyphInstances: expectedInstances - 1})
	var typed *Error
	if geometry != nil || receipt != (GeometryReceipt{}) || !errors.As(err, &typed) || typed.Path != "geometry/resources/expanded_glyph_lower_bound" {
		t.Fatalf("I budget geometry=%v receipt=%+v err=%v", geometry, receipt, err)
	}
	geometry, receipt, err = ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{MaxWorkUnits: 1})
	typed = nil
	if geometry != nil || receipt != (GeometryReceipt{}) || !errors.As(err, &typed) || typed.Path != "geometry/resources/source_rebind_work_lower_bound" {
		t.Fatalf("work budget geometry=%v receipt=%+v err=%v", geometry, receipt, err)
	}
}

func TestGeometryUseMyMetricsLastWinsAndNested(t *testing.T) {
	first := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 0, y: 0, onCurve: true}}, nil, false, 0)
	last := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 10, y: 0, onCurve: true}}, nil, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY | componentUseMyMetrics, glyphID: 1},
		{flags: componentArgsXY | componentUseMyMetrics, glyphID: 2},
	}, nil, 0)
	setGeometryGlyphBBox(compound, geometryTestBBox{0, 0, 10, 0})
	nested := makeOutlineCompoundGlyph([]outlineTestComponent{{flags: componentArgsXY | componentUseMyMetrics, glyphID: 3}}, nil, 0)
	setGeometryGlyphBBox(nested, geometryTestBBox{0, 0, 10, 0})
	tables := outlineTestTables(t, [][]byte{nil, first, last, compound, nested}, outlineTestMaxp{
		points: 1, contours: 1, compositePoints: 2, compositeContours: 2, componentElements: 2, depth: 2,
	})
	setGeometryLongMetrics(tables, []HorizontalMetric{
		{AdvanceWidth: 500, LeftSideBearing: 0},
		{AdvanceWidth: 400, LeftSideBearing: 0},
		{AdvanceWidth: 600, LeftSideBearing: 10},
		{AdvanceWidth: 600, LeftSideBearing: 10},
		{AdvanceWidth: 600, LeftSideBearing: 10},
	})
	setGeometryDeclarations(tables, geometryTestBBox{0, 0, 10, 0}, 0, 400, 20)
	geometry, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, tables), GeometryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	compoundInfo, _ := geometry.GlyphInfo(3)
	nestedInfo, _ := geometry.GlyphInfo(4)
	if compoundInfo.EffectiveMetricSource != 2 || nestedInfo.EffectiveMetricSource != 2 || receipt.MetricRedirectGlyphCount != 2 {
		t.Fatalf("compound=%+v nested=%+v receipt=%+v", compoundInfo, nestedInfo, receipt)
	}
}

func TestGeometryUseMyMetricsRejectsNonIdentityAndHmtxMismatch(t *testing.T) {
	tests := []struct {
		name       string
		matrixFlag uint16
		matrix     []int16
		ownMetric  HorizontalMetric
	}{
		{name: "nonidentity matrix", matrixFlag: componentScale, matrix: []int16{0x2000}, ownMetric: HorizontalMetric{AdvanceWidth: 400, LeftSideBearing: 0}},
		{name: "hmtx mismatch", ownMetric: HorizontalMetric{AdvanceWidth: 401, LeftSideBearing: 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 0, y: 0, onCurve: true}}, nil, false, 0)
			compound := makeOutlineCompoundGlyph([]outlineTestComponent{{
				flags: componentArgsXY | componentUseMyMetrics | test.matrixFlag, glyphID: 1, matrix: test.matrix,
			}}, nil, 0)
			tables := outlineTestTables(t, [][]byte{nil, simple, compound}, outlineTestMaxp{
				points: 1, contours: 1, compositePoints: 1, compositeContours: 1, componentElements: 1, depth: 1,
			})
			setGeometryLongMetrics(tables, []HorizontalMetric{
				{AdvanceWidth: 500}, {AdvanceWidth: 400}, test.ownMetric,
			})
			geometry, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, tables), GeometryLimits{})
			if !errors.Is(err, ErrGeometryMetrics) || geometry != nil || receipt != (GeometryReceipt{}) {
				t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
			}
		})
	}
}

func TestGeometryHeadBitOneIncludesBlankGlyphs(t *testing.T) {
	makeTables := func(blankLSB int16) map[string][]byte {
		simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 5, y: 0, onCurve: true}}, nil, false, 0)
		tables := outlineTestTables(t, [][]byte{nil, simple}, outlineTestMaxp{points: 1, contours: 1})
		setGeometryLongMetrics(tables, []HorizontalMetric{
			{AdvanceWidth: 500, LeftSideBearing: blankLSB},
			{AdvanceWidth: 500, LeftSideBearing: 5},
		})
		binary.BigEndian.PutUint16(tables["head"][16:18], 0x0002)
		setGeometryDeclarations(tables, geometryTestBBox{5, 0, 5, 0}, 5, 495, 5)
		return tables
	}
	t.Run("verified", func(t *testing.T) {
		_, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, makeTables(0)), GeometryLimits{})
		if err != nil || receipt.HeadLSBStatus != GeometryHeadLSBSetV1 {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
	})
	t.Run("blank mismatch", func(t *testing.T) {
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, makeTables(1)), GeometryLimits{})
		if !errors.Is(err, ErrGeometryMetrics) || geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
	})
	t.Run("zero contour simple mismatch", func(t *testing.T) {
		zeroContour := makeOutlineSimpleGlyph(nil, nil, nil, false, 0)
		contour := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 5, y: 0, onCurve: true}}, nil, false, 0)
		tables := outlineTestTables(t, [][]byte{nil, zeroContour, contour}, outlineTestMaxp{points: 1, contours: 1})
		setGeometryLongMetrics(tables, []HorizontalMetric{{AdvanceWidth: 500}, {AdvanceWidth: 500, LeftSideBearing: 1}, {AdvanceWidth: 500, LeftSideBearing: 5}})
		binary.BigEndian.PutUint16(tables["head"][16:18], 0x0002)
		setGeometryDeclarations(tables, geometryTestBBox{5, 0, 5, 0}, 5, 495, 5)
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, tables), GeometryLimits{})
		if !errors.Is(err, ErrGeometryMetrics) || geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
	})
}

func TestGeometryHeadAndHheaExactMismatch(t *testing.T) {
	makeValid := func() map[string][]byte {
		simple := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{{x: 0, y: 0, onCurve: true}, {x: 10, y: 5, onCurve: true}}, nil, false, 0)
		tables := outlineTestTables(t, [][]byte{nil, simple}, outlineTestMaxp{points: 2, contours: 1})
		setGeometryDeclarations(tables, geometryTestBBox{0, 0, 10, 5}, 0, 490, 10)
		return tables
	}
	tests := []struct {
		name  string
		cause error
		edit  func(map[string][]byte)
	}{
		{"head plus one", ErrGeometryInvalid, func(tables map[string][]byte) { binary.BigEndian.PutUint16(tables["head"][40:42], 11) }},
		{"head minus one", ErrGeometryInvalid, func(tables map[string][]byte) { binary.BigEndian.PutUint16(tables["head"][40:42], 9) }},
		{"hhea min rsb plus one", ErrGeometryMetrics, func(tables map[string][]byte) { binary.BigEndian.PutUint16(tables["hhea"][14:16], 491) }},
		{"hhea extent minus one", ErrGeometryMetrics, func(tables map[string][]byte) { binary.BigEndian.PutUint16(tables["hhea"][16:18], 9) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tables := makeValid()
			test.edit(tables)
			geometry, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, tables), GeometryLimits{})
			if !errors.Is(err, test.cause) || geometry != nil || receipt != (GeometryReceipt{}) {
				t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
			}
		})
	}
}

func TestGeometryHheaNarrowsOnlyFinalExtrema(t *testing.T) {
	first := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 0, y: 0, onCurve: true}}, nil, false, 0)
	second := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: 0, y: 0, onCurve: true}}, nil, false, 0)
	tables := outlineTestTables(t, [][]byte{nil, first, second}, outlineTestMaxp{points: 1, contours: 1})
	setGeometryLongMetrics(tables, []HorizontalMetric{
		{AdvanceWidth: 500},
		{AdvanceWidth: 40_000}, // Individual RSB is outside int16, but is not the minimum.
		{AdvanceWidth: 100},
	})
	setGeometryDeclarations(tables, geometryTestBBox{}, 0, 100, 0)
	_, receipt, err := ValidateOutlineGeometry(context.Background(), decodeGeometryTestOutline(t, tables), GeometryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.MinRSB != 100 {
		t.Fatalf("minRSB=%d", receipt.MinRSB)
	}
}

func TestGeometryResourceLimitsExactBoundaries(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{
		{x: 0, y: 0, onCurve: true}, {x: 3, y: 0, onCurve: false},
	}, nil, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY, glyphID: 1},
		{flags: componentScale, glyphID: 1, arg1: 1, arg2: 1, matrix: []int16{0x2000}},
	}, nil, 0)
	setGeometryGlyphBBox(compound, geometryTestBBox{0, 0, 3, 0})
	tables := outlineTestTables(t, [][]byte{nil, simple, compound}, outlineTestMaxp{
		points: 2, contours: 1, compositePoints: 4, compositeContours: 2, componentElements: 2, depth: 1,
	})
	setGeometryDeclarations(tables, geometryTestBBox{0, 0, 3, 0}, 0, 497, 3)
	outline := decodeGeometryTestOutline(t, tables)
	_, baseline, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	recomputedWork, ok := outlineWeightedSum(0,
		[2]uint64{baseline.SourceCanonicalBinaryBytes, 1},
		[2]uint64{uint64(baseline.GlyphCount), 64},
		[2]uint64{baseline.ExpandedPointInstances, 16},
		[2]uint64{baseline.ExpandedContourInstances, 8},
		[2]uint64{baseline.ExpandedEdgeVisits, 32},
		[2]uint64{baseline.ExpandedGlyphInstances, 16},
		[2]uint64{baseline.TransformPointVisits, 12},
		[2]uint64{baseline.ExactOpCount, 8},
		[2]uint64{baseline.ExactLimbUnits, 1},
		[2]uint64{baseline.CanonicalBinaryBytes, 1},
	)
	if !ok || recomputedWork != baseline.WorkUnits {
		t.Fatalf("receipt-only work=%d ok=%t receipt=%d", recomputedWork, ok, baseline.WorkUnits)
	}
	tests := []struct {
		name string
		at   uint64
		set  func(*GeometryLimits, uint64)
	}{
		{"expanded points", baseline.ExpandedPointInstances, func(v *GeometryLimits, n uint64) { v.MaxExpandedPointInstances = n }},
		{"expanded contours", baseline.ExpandedContourInstances, func(v *GeometryLimits, n uint64) { v.MaxExpandedContourInstances = n }},
		{"expanded edges", baseline.ExpandedEdgeVisits, func(v *GeometryLimits, n uint64) { v.MaxExpandedEdgeVisits = n }},
		{"expanded glyphs", baseline.ExpandedGlyphInstances, func(v *GeometryLimits, n uint64) { v.MaxExpandedGlyphInstances = n }},
		{"transform visits", baseline.TransformPointVisits, func(v *GeometryLimits, n uint64) { v.MaxTransformPointVisits = n }},
		{"exact operations", baseline.ExactOpCount, func(v *GeometryLimits, n uint64) { v.MaxExactOps = n }},
		{"exact limbs", baseline.ExactLimbUnits, func(v *GeometryLimits, n uint64) { v.MaxExactLimbUnits = n }},
		{"canonical bytes", baseline.CanonicalBinaryBytes, func(v *GeometryLimits, n uint64) { v.MaxCanonicalBinaryBytes = n }},
		{"work", baseline.WorkUnits, func(v *GeometryLimits, n uint64) { v.MaxWorkUnits = n }},
		{"working", baseline.WorkingBytes, func(v *GeometryLimits, n uint64) { v.MaxWorkingBytes = n }},
		{"retained", baseline.RetainedBytes, func(v *GeometryLimits, n uint64) { v.MaxRetainedBytes = n }},
	}
	for _, test := range tests {
		if test.at <= 1 {
			t.Fatalf("test %q has unusable boundary %d", test.name, test.at)
		}
		t.Run(test.name, func(t *testing.T) {
			at := GeometryLimits{}
			test.set(&at, test.at)
			if _, _, err := ValidateOutlineGeometry(context.Background(), outline, at); err != nil {
				t.Fatalf("N=%d rejected: %v", test.at, err)
			}
			below := GeometryLimits{}
			test.set(&below, test.at-1)
			geometry, receipt, err := ValidateOutlineGeometry(context.Background(), outline, below)
			if !errors.Is(err, ErrResourceLimit) || geometry != nil || receipt != (GeometryReceipt{}) {
				t.Fatalf("N-1=%d geometry=%v receipt=%+v err=%v", test.at-1, geometry, receipt, err)
			}
		})
	}
	if baseline.MaxObservedExactBits <= 1 {
		t.Fatalf("exact bit boundary=%d", baseline.MaxObservedExactBits)
	}
	t.Run("exact bits", func(t *testing.T) {
		at := GeometryLimits{MaxExactBits: baseline.MaxObservedExactBits}
		if _, _, err := ValidateOutlineGeometry(context.Background(), outline, at); err != nil {
			t.Fatalf("N=%d rejected: %v", baseline.MaxObservedExactBits, err)
		}
		below := GeometryLimits{MaxExactBits: baseline.MaxObservedExactBits - 1}
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), outline, below)
		if !errors.Is(err, ErrResourceLimit) || geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
	})
}

func TestGeometryFailureAlwaysReturnsZeroReceipt(t *testing.T) {
	outline := decodeGeometryTestOutline(t, outlineTestTables(t, [][]byte{nil, nil}, outlineTestMaxp{}))
	tests := []struct {
		name   string
		ctx    context.Context
		source *OutlineFont
		limits GeometryLimits
		cause  error
	}{
		{"nil context", nil, outline, GeometryLimits{}, ErrContextInvalid},
		{"nil source", context.Background(), nil, GeometryLimits{}, ErrGeometryInputInvalid},
		{"invalid limits", context.Background(), outline, GeometryLimits{MaxExactBits: HardMaxGeometryExactBits + 1}, ErrLimitsInvalid},
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests = append(tests, struct {
		name   string
		ctx    context.Context
		source *OutlineFont
		limits GeometryLimits
		cause  error
	}{"canceled", canceled, outline, GeometryLimits{}, ErrCanceled})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			geometry, receipt, err := ValidateOutlineGeometry(test.ctx, test.source, test.limits)
			if !errors.Is(err, test.cause) || geometry != nil || receipt != (GeometryReceipt{}) {
				t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
			}
		})
	}

	t.Run("source graph mutation", func(t *testing.T) {
		mutated := *outline
		mutated.receipt.GraphHash = "sha256:mutated"
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), &mutated, GeometryLimits{})
		if !errors.Is(err, ErrGeometryInputInvalid) || geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
	})

	t.Run("source canonical byte binding mutation", func(t *testing.T) {
		mutated := *outline
		mutated.receipt.CanonicalBinaryBytes++
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), &mutated, GeometryLimits{})
		var typed *Error
		if !errors.Is(err, ErrGeometryInputInvalid) || !errors.As(err, &typed) || typed.Path != "geometry/source/graph_binding" ||
			geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
	})
}

func TestGeometryCancellationAcrossValidationStages(t *testing.T) {
	simple := makeOutlineSimpleGlyph([]uint16{1}, []outlineTestPoint{
		{x: 0, y: 0, onCurve: true}, {x: 3, y: 0, onCurve: false},
	}, nil, false, 0)
	compound := makeOutlineCompoundGlyph([]outlineTestComponent{
		{flags: componentArgsXY, glyphID: 1},
		{flags: componentScale, glyphID: 1, arg1: 1, arg2: 1, matrix: []int16{0x2000}},
	}, nil, 0)
	setGeometryGlyphBBox(compound, geometryTestBBox{0, 0, 3, 0})
	tables := outlineTestTables(t, [][]byte{nil, simple, compound}, outlineTestMaxp{
		points: 2, contours: 1, compositePoints: 4, compositeContours: 2, componentElements: 2, depth: 1,
	})
	setGeometryDeclarations(tables, geometryTestBBox{0, 0, 3, 0}, 0, 497, 3)
	outline := decodeGeometryTestOutline(t, tables)
	probe := &geometryCountingContext{}
	if _, _, err := ValidateOutlineGeometry(probe, outline, GeometryLimits{}); err != nil {
		t.Fatal(err)
	}
	if probe.calls < 10 {
		t.Fatalf("unexpectedly few cancellation checkpoints: %d", probe.calls)
	}
	wanted := map[string]bool{
		"outline/hash": false, "geometry/topology": false, "geometry/metrics": false,
		"geometry/expand": false, "geometry/bbox": false, "geometry/hash": false, "geometry/publish": false,
	}
	for cancelAt := 1; cancelAt <= probe.calls; cancelAt++ {
		ctx := &geometryCountingContext{cancelAt: cancelAt}
		geometry, receipt, err := ValidateOutlineGeometry(ctx, outline, GeometryLimits{})
		if !errors.Is(err, ErrCanceled) || geometry != nil || receipt != (GeometryReceipt{}) {
			t.Fatalf("cancelAt=%d calls=%d geometry=%v receipt=%+v err=%v", cancelAt, ctx.calls, geometry, receipt, err)
		}
		var typed *Error
		if !errors.As(err, &typed) {
			t.Fatalf("cancelAt=%d untyped error %v", cancelAt, err)
		}
		for prefix := range wanted {
			if strings.HasPrefix(typed.Path, prefix) {
				wanted[prefix] = true
			}
		}
	}
	for prefix, observed := range wanted {
		if !observed {
			t.Errorf("no cancellation checkpoint observed in %s", prefix)
		}
	}
}

func FuzzValidateOutlineGeometryFailureIsAtomic(f *testing.F) {
	tables := outlineTestTables(&fuzzTestingT{f: f}, [][]byte{nil, nil}, outlineTestMaxp{})
	f.Add(buildTestSFNT(&fuzzTestingT{f: f}, tables))
	f.Fuzz(func(t *testing.T, encoded []byte) {
		font, _, err := Parse(context.Background(), encoded, Limits{})
		if err != nil {
			return
		}
		outline, _, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
		if err != nil {
			return
		}
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
		if err != nil && (geometry != nil || receipt != (GeometryReceipt{})) {
			t.Fatalf("failure published geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
	})
}

func FuzzValidateOutlineGeometryStructuredSemantic(f *testing.F) {
	f.Add(int16(0), int16(0), int16(0x4000), int16(0), int16(0), int16(0x4000), int8(0), int8(0))
	f.Fuzz(func(t *testing.T, x, y, xscale, scale01, scale10, yscale int16, offsetX, offsetY int8) {
		simple := makeOutlineSimpleGlyph([]uint16{0}, []outlineTestPoint{{x: x, y: y, onCurve: true}}, nil, false, 0)
		compound := makeOutlineCompoundGlyph([]outlineTestComponent{{
			flags: componentArgsXY | componentTwoByTwo, glyphID: 1,
			arg1: int32(offsetX), arg2: int32(offsetY), matrix: []int16{xscale, scale01, scale10, yscale},
		}}, nil, 0)
		// Keep the phase-one structure and complete sfnt checksums valid while
		// deliberately varying exact geometry behind a deferred compound bbox.
		tables := outlineTestTables(t, [][]byte{nil, simple, compound}, outlineTestMaxp{
			points: 1, contours: 1, compositePoints: 1, compositeContours: 1, componentElements: 1, depth: 1,
		})
		setGeometryDeclarations(tables, geometryTestBBox{}, 0, 500, 0)
		font := parseOutlineTestFont(t, tables)
		outline, _, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
		if err != nil {
			t.Fatalf("structured phase-one input rejected: %v", err)
		}
		geometry, receipt, err := ValidateOutlineGeometry(context.Background(), outline, GeometryLimits{})
		if err != nil && (geometry != nil || receipt != (GeometryReceipt{})) {
			t.Fatalf("failure published geometry=%v receipt=%+v err=%v", geometry, receipt, err)
		}
	})
}

func decodeGeometryTestOutline(t testing.TB, tables map[string][]byte) *OutlineFont {
	t.Helper()
	font := parseOutlineTestFont(t, tables)
	outline, _, err := DecodeOutlineFont(context.Background(), font, OutlineLimits{})
	if err != nil {
		t.Fatalf("DecodeOutlineFont: %v", err)
	}
	return outline
}

func setGeometryGlyphBBox(body []byte, bbox geometryTestBBox) {
	binary.BigEndian.PutUint16(body[2:4], uint16(bbox.xMin))
	binary.BigEndian.PutUint16(body[4:6], uint16(bbox.yMin))
	binary.BigEndian.PutUint16(body[6:8], uint16(bbox.xMax))
	binary.BigEndian.PutUint16(body[8:10], uint16(bbox.yMax))
}

func setGeometryDeclarations(tables map[string][]byte, bbox geometryTestBBox, minLSB, minRSB, xExtent int16) {
	setGeometryGlyphBBox(tables["head"][34:], bbox)
	binary.BigEndian.PutUint16(tables["hhea"][12:14], uint16(minLSB))
	binary.BigEndian.PutUint16(tables["hhea"][14:16], uint16(minRSB))
	binary.BigEndian.PutUint16(tables["hhea"][16:18], uint16(xExtent))
}

func setGeometryLongMetrics(tables map[string][]byte, metrics []HorizontalMetric) {
	hmtx := make([]byte, len(metrics)*4)
	var maximum uint16
	for index, metric := range metrics {
		binary.BigEndian.PutUint16(hmtx[index*4:index*4+2], metric.AdvanceWidth)
		binary.BigEndian.PutUint16(hmtx[index*4+2:index*4+4], uint16(metric.LeftSideBearing))
		if metric.AdvanceWidth > maximum {
			maximum = metric.AdvanceWidth
		}
	}
	tables["hmtx"] = hmtx
	binary.BigEndian.PutUint16(tables["hhea"][10:12], maximum)
	binary.BigEndian.PutUint16(tables["hhea"][34:36], uint16(len(metrics)))
}

func expandGeometryForTest(t testing.TB, outline *OutlineFont, glyphID uint32) exactGlyphGeometry {
	t.Helper()
	exact := newGeometryExactContext(context.Background(), HardMaxGeometryExactBits, 1<<32, 1<<34)
	geometry, err := expandExactGlyph(context.Background(), outline, glyphID, exact)
	if err != nil {
		t.Fatalf("expand gid %d: %v", glyphID, err)
	}
	return geometry
}

func assertGeometryRat(t testing.TB, got geometryDyadic, want *big.Rat) {
	t.Helper()
	// Independent oracle conversion: directly form numerator / 2^shift without
	// calling any production arithmetic, normalization or comparison helper.
	denominator := new(big.Int).Lsh(big.NewInt(1), uint(got.shift))
	actual := new(big.Rat).SetFrac(new(big.Int).Set(got.numerator), denominator)
	if actual.Cmp(want) != 0 {
		t.Fatalf("got=%s want=%s raw=%s/2^%d", actual.RatString(), want.RatString(), got.numerator, got.shift)
	}
}

type geometryCountingContext struct {
	calls    int
	cancelAt int
}

func (value *geometryCountingContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (value *geometryCountingContext) Done() <-chan struct{}       { return nil }
func (value *geometryCountingContext) Value(any) any               { return nil }
func (value *geometryCountingContext) Err() error {
	value.calls++
	if value.cancelAt > 0 && value.calls >= value.cancelAt {
		return context.Canceled
	}
	return nil
}
