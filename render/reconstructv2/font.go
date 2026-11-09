package reconstructv2

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
	"github.com/CutlyLab/Cutly/render/textlayer"
)

// fontBuildLimits is trusted host policy, not a serialized receipt or document
// option. Every field must be explicit and positive; zero never invokes the
// native APIs' larger defaults. The initial adapter accepts only lower budgets
// than defaultFontBuildLimits, without widening any native structural profile.
// Core inspection/snapshot budgets are separate and must be admitted by the
// factory before invoking this adapter.
type fontBuildLimits struct {
	Outline    sfntglyf.OutlineLimits
	Geometry   sfntglyf.GeometryLimits
	OutlineSet sfntglyf.OutlineSetLimits
	Layout     sfntglyf.LayoutGraphLimits
	Provider   sfntglyf.ShapeProviderLimits
}

func defaultFontBuildLimits() fontBuildLimits {
	l := fontBuildLimits{
		Outline: sfntglyf.DefaultOutlineLimits(), Geometry: sfntglyf.DefaultGeometryLimits(),
		OutlineSet: sfntglyf.DefaultOutlineSetLimits(), Layout: sfntglyf.DefaultLayoutGraphLimits(),
		Provider: sfntglyf.DefaultShapeProviderLimits(),
	}
	const memory = 16 << 20
	const work = 1 << 28
	l.Outline.MaxWorkingBytes = memory
	l.Outline.MaxRetainedBytes = memory
	l.Outline.MaxWorkUnits = work
	l.Outline.MaxCanonicalBinaryBytes = 8 << 20
	l.Geometry.MaxWorkingBytes = memory
	l.Geometry.MaxRetainedBytes = memory
	l.Geometry.MaxWorkUnits = work
	l.Geometry.MaxCanonicalBinaryBytes = 8 << 20
	l.OutlineSet.MaxWorkingBytes = memory
	l.OutlineSet.MaxRetainedBytes = memory
	l.OutlineSet.MaxWorkUnits = work
	l.OutlineSet.MaxCanonicalBinaryBytes = 8 << 20
	l.Layout.MaxWorkingBytes = memory
	l.Layout.MaxRetainedBytes = memory
	l.Layout.MaxWorkUnits = work
	l.Layout.MaxValidationCanonicalBinaryBytes = 8 << 20
	l.Provider.MaxWorkingBytes = memory
	l.Provider.MaxRetainedBytes = memory
	l.Provider.MaxWorkUnits = work
	return l
}

// fontBuildEnvelope is allocation-free admission for aggregate factory budgets.
// Working=sum(W_outline,W_geometry,W_set,W_layout,W_provider)+W_layout;
// retained=sum(R_outline,R_geometry,R_set,R_layout,R_provider)+R_layout.
// The extra layout reservation covers the live graph replay during provider
// binding alongside the original graph. Adding both totals intentionally
// overcounts cumulative lineage and sequential workspace: conservative portable
// logical charges, NOT an allocator/GC/RSS ceiling. Core inspection and the input
// snapshot are charged separately; final provider lineage is already included in
// Provider.MaxRetainedBytes. There are no unreported zero/default normalizations.
func fontBuildEnvelope(l fontBuildLimits) (working, retained uint64, err error) {
	// Fixed nested structs of unsigned scalars only, with no caller graph or hooks.
	var admit func(reflect.Value, reflect.Value) bool
	admit = func(v, max reflect.Value) bool {
		if v.Kind() == reflect.Struct {
			for i := 0; i < v.NumField(); i++ {
				if !admit(v.Field(i), max.Field(i)) {
					return false
				}
			}
			return true
		}
		return v.Uint() > 0 && v.Uint() <= max.Uint()
	}
	if !admit(reflect.ValueOf(l), reflect.ValueOf(defaultFontBuildLimits())) {
		return 0, 0, ErrLimit
	}
	add := func(total *uint64, n uint64) bool {
		if *total > math.MaxUint64-n {
			return false
		}
		*total += n
		return true
	}
	for _, n := range []uint64{l.Outline.MaxWorkingBytes, l.Geometry.MaxWorkingBytes, l.OutlineSet.MaxWorkingBytes, l.Layout.MaxWorkingBytes, l.Provider.MaxWorkingBytes, l.Layout.MaxWorkingBytes} {
		if !add(&working, n) {
			return 0, 0, ErrLimit
		}
	}
	for _, n := range []uint64{l.Outline.MaxRetainedBytes, l.Geometry.MaxRetainedBytes, l.OutlineSet.MaxRetainedBytes, l.Layout.MaxRetainedBytes, l.Provider.MaxRetainedBytes, l.Layout.MaxRetainedBytes} {
		if !add(&retained, n) {
			return 0, 0, ErrLimit
		}
	}
	if working > math.MaxUint64-retained {
		return 0, 0, ErrLimit
	}
	return working, retained, nil
}

func fontDocumentHashValid(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	for _, c := range s[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// buildFontBinding consumes only an ACTUAL native core inspection matching the
// independently authorized expected binding. It never reparses caller bytes or
// accepts copied wire observations. Every later native stage consumes the exact
// previous live object, and each receipt edge is cross-checked before publication.
// documentHash must be the original verified Document hash authorized by the host.
func buildFontBinding(ctx context.Context, documentHash string, expected resourceidentityv2.Binding, actual *resourceidentityv2.Inspected, l fontBuildLimits) (out textlayer.FontBinding, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			out = textlayer.FontBinding{}
		}
	}()
	if ctx == nil {
		return out, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if _, _, err = fontBuildEnvelope(l); err != nil {
		return out, err
	}
	if !fontDocumentHashValid(documentHash) || expected.Kind != documentv2.AssetKindFont {
		return out, ErrResource
	}
	observed, e := actual.Binding()
	if e != nil || observed != expected {
		return out, errors.Join(ErrResource, e)
	}
	receipt, e := actual.Receipt()
	if e != nil {
		return out, errors.Join(ErrResource, e)
	}
	if e = resourceidentityv2.Matches(ctx, expected, receipt, actual); e != nil {
		return out, errors.Join(ErrResource, e)
	}
	font, e := actual.Font()
	if e != nil {
		return out, errors.Join(ErrResource, e)
	}
	core := font.Receipt()
	if core.OpaqueLayoutTables == nil {
		core.OpaqueLayoutTables = []string{}
	}
	if receipt.Font == nil || !reflect.DeepEqual(core, *receipt.Font) || core.ContentHash != expected.ContentHash || core.EncodedBytes != uint64(expected.SizeBytes) {
		return out, ErrResource
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	outlines, or, e := sfntglyf.DecodeOutlineFont(ctx, font, l.Outline)
	if e != nil {
		return out, fontBuildError("outline", e)
	}
	if outlines == nil || or.UpstreamContentHash != expected.ContentHash || or.UpstreamContentProfile != core.ContentHashProfile || or.GlyphCount != core.NumGlyphs || or.UpstreamRetainedBytes != core.RetainedBytes {
		return out, ErrResource
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	geometry, gr, e := sfntglyf.ValidateOutlineGeometry(ctx, outlines, l.Geometry)
	if e != nil {
		return out, fontBuildError("geometry", e)
	}
	if geometry == nil || gr.UpstreamContentHash != expected.ContentHash || gr.SourceGraphHash != or.GraphHash {
		return out, ErrResource
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	set, sr, e := sfntglyf.MaterializeOutlineSet(ctx, geometry, l.OutlineSet)
	if e != nil {
		return out, fontBuildError("outline set", e)
	}
	if set == nil || sr.UpstreamContentHash != expected.ContentHash || sr.SourceGeometryHash != gr.GeometryHash || sr.SourceGraphHash != or.GraphHash {
		return out, ErrResource
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	graph, lr, e := sfntglyf.BuildLayoutGraph(ctx, set, l.Layout)
	if e != nil {
		return out, fontBuildError("layout graph", e)
	}
	if graph == nil || lr.UpstreamContentHash != expected.ContentHash || lr.SourceOutlineSetHash != sr.OutlineSetHash {
		return out, ErrResource
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	provider, pr, e := sfntglyf.BindShapeProvider(ctx, graph, l.Provider)
	if e != nil {
		return out, fontBuildError("shape provider", e)
	}
	if provider == nil || pr != provider.Receipt() || pr.FontContentHash != expected.ContentHash || pr.FontContentHashProfile != core.ContentHashProfile || pr.EncodedBytes != uint64(expected.SizeBytes) || pr.GlyphCount != core.NumGlyphs || pr.UnitsPerEm != core.UnitsPerEm || pr.LayoutGraphHash != lr.GraphHash || pr.OutlineSetHash != sr.OutlineSetHash {
		return out, ErrResource
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return textlayer.FontBinding{DocumentHash: documentHash, FontAssetID: expected.AssetID, Provider: provider}, nil
}
func fontBuildError(stage string, err error) error {
	return fmt.Errorf("%w: font %s: %w", ErrResource, stage, err)
}
