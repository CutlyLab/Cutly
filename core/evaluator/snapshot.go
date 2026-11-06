package evaluator

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

var ErrCanonicalDocumentLimit = errors.New("evaluator: canonical document exceeds compile limit")

type SnapshotLimits struct {
	MaxCanonicalDocumentBytes uint64
	MaxDocumentNodes          uint64
}

// SnapshotAsset is compact, immutable-by-value metadata from the same canonical Document v2
// snapshot used to compile Program. It deliberately exposes no source slices or property pointers.
type SnapshotAsset struct {
	ID                               documentv2.ID
	Index                            int
	Kind                             documentv2.AssetKind
	ContentHash                      string
	TextProfile                      string
	TextViewport                     documentv2.RasterViewport
	TextFontAssetID                  documentv2.ID
	TextFontAssetIndex               int
	TextFontContentHash              string
	TextFontProfile                  string
	TextRunInputProfile              string
	TextLayoutProfile                string
	TextOutlineProfile               string
	TextRasterProfile                string
	TextPaintProfile                 string
	TextCoverageHashProfile          string
	TextUnicodeVersion               string
	TextUnicodeValidationProfile     string
	TextUnicodeValidationHashProfile string
	TextUnicodeValidationTableHash   string
	TextSemanticHashProfile          string
	TextSemanticHash                 string
	TextLayoutInputHashProfile       string
	TextLayoutInputHash              string
	VectorProfile                    string
	VectorViewport                   documentv2.RasterViewport
	VectorSemanticHash               string
	CanonicalSourceBytes             uint64
	PathCount                        uint64
	CommandCount                     uint64
	ConceptualEdgeCount              uint64
}

type snapshotTextSemanticEnvelope struct {
	SchemaVersion                int                   `json:"schema_version"`
	Profile                      string                `json:"profile"`
	TextProfile                  string                `json:"text_profile"`
	EvaluatorProfile             string                `json:"evaluator_profile"`
	FontProfile                  string                `json:"font_profile"`
	RunInputProfile              string                `json:"run_input_profile"`
	LayoutProfile                string                `json:"layout_profile"`
	OutlineProfile               string                `json:"outline_profile"`
	RasterProfile                string                `json:"raster_profile"`
	PaintProfile                 string                `json:"paint_profile"`
	CoverageHashProfile          string                `json:"coverage_hash_profile"`
	LayoutInputHashProfile       string                `json:"layout_input_hash_profile"`
	UnicodeVersion               string                `json:"unicode_version"`
	UnicodeValidationProfile     string                `json:"unicode_validation_profile"`
	UnicodeValidationHashProfile string                `json:"unicode_validation_hash_profile"`
	UnicodeValidationTableHash   string                `json:"unicode_validation_table_hash"`
	FontContentHash              string                `json:"font_content_hash"`
	Source                       documentv2.TextSource `json:"source"`
}

type snapshotTextLayoutInputEnvelope struct {
	SchemaVersion                int                       `json:"schema_version"`
	Profile                      string                    `json:"profile"`
	TextProfile                  string                    `json:"text_profile"`
	EvaluatorProfile             string                    `json:"evaluator_profile"`
	FontProfile                  string                    `json:"font_profile"`
	RunInputProfile              string                    `json:"run_input_profile"`
	LayoutProfile                string                    `json:"layout_profile"`
	OutlineProfile               string                    `json:"outline_profile"`
	RasterProfile                string                    `json:"raster_profile"`
	CoverageHashProfile          string                    `json:"coverage_hash_profile"`
	UnicodeVersion               string                    `json:"unicode_version"`
	UnicodeValidationProfile     string                    `json:"unicode_validation_profile"`
	UnicodeValidationHashProfile string                    `json:"unicode_validation_hash_profile"`
	UnicodeValidationTableHash   string                    `json:"unicode_validation_table_hash"`
	FontContentHash              string                    `json:"font_content_hash"`
	Text                         string                    `json:"text"`
	FontSize                     documentv2.Rational       `json:"font_size"`
	LineHeight                   documentv2.Rational       `json:"line_height"`
	Viewport                     documentv2.RasterViewport `json:"viewport"`
	Language                     string                    `json:"language"`
	Script                       string                    `json:"script"`
	Direction                    documentv2.TextDirection  `json:"direction"`
	Alignment                    documentv2.TextAlignment  `json:"alignment"`
}

type SnapshotClip struct {
	ID       documentv2.ID
	Index    int
	AssetID  documentv2.ID
	Disabled bool
}

type SnapshotTrack struct {
	ID    documentv2.ID
	Index int
	Kind  documentv2.TrackKind
	Clips []SnapshotClip
}

type SnapshotTransition struct {
	ID             documentv2.ID
	Index          int
	Profile        string
	OutgoingClipID documentv2.ID
	IncomingClipID documentv2.ID
	PreDuration    documentv2.RationalTime
	PostDuration   documentv2.RationalTime
}

type SnapshotTimeline struct {
	ID          documentv2.ID
	Index       int
	Tracks      []SnapshotTrack
	Transitions []SnapshotTransition
}

// FrozenSnapshot is a compact read-only discovery index. Its fields are private and every slice
// accessor returns a defensive deep copy, so callers cannot mutate Program through aliases.
type FrozenSnapshot struct {
	documentHash  string
	canonicalSize uint64
	assets        []SnapshotAsset
	timelines     []SnapshotTimeline
}

func (value *FrozenSnapshot) DocumentHash() string {
	if value == nil {
		return ""
	}
	return value.documentHash
}

func (value *FrozenSnapshot) CanonicalBytes() uint64 {
	if value == nil {
		return 0
	}
	return value.canonicalSize
}

func (value *FrozenSnapshot) Assets() []SnapshotAsset {
	if value == nil {
		return nil
	}
	return append([]SnapshotAsset(nil), value.assets...)
}

func (value *FrozenSnapshot) Timelines() []SnapshotTimeline {
	if value == nil {
		return nil
	}
	result := make([]SnapshotTimeline, len(value.timelines))
	for timelineIndex, timeline := range value.timelines {
		result[timelineIndex] = timeline
		result[timelineIndex].Tracks = make([]SnapshotTrack, len(timeline.Tracks))
		for trackIndex, track := range timeline.Tracks {
			result[timelineIndex].Tracks[trackIndex] = track
			result[timelineIndex].Tracks[trackIndex].Clips = append([]SnapshotClip(nil), track.Clips...)
		}
		result[timelineIndex].Transitions = append([]SnapshotTransition(nil), timeline.Transitions...)
	}
	return result
}

// CompileSnapshot performs one canonical encode, one strict decode and one evaluator build, then
// publishes only Program plus compact defensive metadata. Limits are enforced by a non-allocating
// conservative graph/encoded-size walk before capability validation or canonical allocation.
// Failure and cancellation return neither a partial Program nor a partial snapshot.
func CompileSnapshot(
	ctx context.Context,
	document documentv2.Document,
	offered videoedit.BackendCapabilities,
	limits SnapshotLimits,
) (*Program, *FrozenSnapshot, []documentv2.Diagnostic) {
	if ctx == nil || limits.MaxCanonicalDocumentBytes == 0 || limits.MaxDocumentNodes == 0 {
		err := fmt.Errorf("%w: invalid compile context or byte limit", ErrCanonicalDocumentLimit)
		return nil, nil, []documentv2.Diagnostic{compileDiagnostic(err)}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, []documentv2.Diagnostic{compileDiagnostic(err)}
	}
	if err := preflightSnapshotDocument(ctx, document, limits); err != nil {
		return nil, nil, []documentv2.Diagnostic{compileDiagnostic(err)}
	}
	diagnostics := documentv2.PreflightCapabilities(document, offered)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, nil, diagnostics
	}
	canonical, err := documentv2.CanonicalJSON(document)
	if err != nil {
		return nil, nil, append(diagnostics, compileDiagnostic(err))
	}
	if uint64(len(canonical)) > limits.MaxCanonicalDocumentBytes {
		return nil, nil, append(diagnostics, compileDiagnostic(ErrCanonicalDocumentLimit))
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, append(diagnostics, compileDiagnostic(err))
	}
	frozen, err := documentv2.Decode(canonical)
	if err != nil {
		return nil, nil, append(diagnostics, compileDiagnostic(err))
	}
	hash := canonicalDocumentHash(canonical)
	snapshot, err := buildFrozenSnapshot(ctx, frozen, hash, uint64(len(canonical)))
	if err != nil {
		return nil, nil, append(diagnostics, compileDiagnostic(err))
	}
	program, diagnostics := compileFrozen(ctx, frozen, hash, diagnostics)
	if program == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		return nil, nil, diagnostics
	}
	return program, snapshot, diagnostics
}

func buildFrozenSnapshot(ctx context.Context, frozen documentv2.Document, hash string, canonicalSize uint64) (*FrozenSnapshot, error) {
	result := &FrozenSnapshot{documentHash: hash, canonicalSize: canonicalSize, assets: make([]SnapshotAsset, len(frozen.Assets)), timelines: make([]SnapshotTimeline, len(frozen.Timelines))}
	var visits uint64
	checkpoint := func() error {
		visits++
		if visits&255 == 0 {
			return ctx.Err()
		}
		return nil
	}
	assetIndexes := make(map[documentv2.ID]int, len(frozen.Assets))
	for index, asset := range frozen.Assets {
		assetIndexes[asset.ID] = index
	}
	var sourceBytes uint64
	for index, asset := range frozen.Assets {
		if err := checkpoint(); err != nil {
			return nil, err
		}
		entry := SnapshotAsset{ID: asset.ID, Index: index, Kind: asset.Kind, ContentHash: asset.ContentHash, TextFontAssetIndex: -1}
		if asset.Text != nil && asset.Text.Profile == documentv2.TextProfilePinnedFontViewportPXV2 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			encoded, err := videoedit.CanonicalJSON(*asset.Text)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if uint64(len(encoded)) > math.MaxUint64-sourceBytes || sourceBytes+uint64(len(encoded)) > canonicalSize {
				return nil, ErrCanonicalDocumentLimit
			}
			sourceBytes += uint64(len(encoded))
			entry.CanonicalSourceBytes = uint64(len(encoded))
			entry.TextProfile = asset.Text.Profile
			entry.TextFontAssetID = asset.Text.FontAssetID
			fontIndex, exists := assetIndexes[asset.Text.FontAssetID]
			if !exists || fontIndex < 0 || fontIndex >= len(frozen.Assets) || frozen.Assets[fontIndex].Kind != documentv2.AssetKindFont {
				return nil, errors.New("evaluator: validated text font binding is missing")
			}
			entry.TextFontAssetIndex = fontIndex
			entry.TextFontContentHash = frozen.Assets[fontIndex].ContentHash
			if asset.Text.Viewport == nil || asset.Text.LineHeight == nil {
				return nil, errors.New("evaluator: validated text v2 geometry is missing")
			}
			entry.TextViewport = *asset.Text.Viewport
			entry.TextFontProfile = documentv2.TextFontProfileStaticGlyfV1
			entry.TextRunInputProfile = documentv2.TextRunInputProfileV1
			entry.TextLayoutProfile = documentv2.TextLayoutProfileUnwrappedV1
			entry.TextOutlineProfile = documentv2.TextOutlineProfileQ26_6V1
			entry.TextRasterProfile = documentv2.TextRasterProfileSupersample8x8V1
			entry.TextPaintProfile = documentv2.TextPaintProfileRGBA16SRGBV1
			entry.TextCoverageHashProfile = documentv2.TextCoverageHashProfileV1
			entry.TextUnicodeVersion = documentv2.TextUnicodeDataVersionV1
			entry.TextUnicodeValidationProfile = documentv2.TextUnicodeValidationTableProfileV1
			entry.TextUnicodeValidationHashProfile = documentv2.TextUnicodeValidationTableHashProfileV1
			entry.TextUnicodeValidationTableHash = documentv2.TextUnicodeValidationTableHashV1
			entry.TextSemanticHashProfile = documentv2.TextSemanticHashProfileV1
			entry.TextLayoutInputHashProfile = documentv2.TextLayoutInputHashProfileV1
			semantic := snapshotTextSemanticEnvelope{
				SchemaVersion: 1, Profile: documentv2.TextSemanticHashProfileV1,
				TextProfile: asset.Text.Profile, EvaluatorProfile: documentv2.EvaluatorTextPinnedFontViewportPXV2,
				FontProfile: documentv2.TextFontProfileStaticGlyfV1, RunInputProfile: documentv2.TextRunInputProfileV1,
				LayoutProfile: documentv2.TextLayoutProfileUnwrappedV1, OutlineProfile: documentv2.TextOutlineProfileQ26_6V1,
				RasterProfile: documentv2.TextRasterProfileSupersample8x8V1, CoverageHashProfile: documentv2.TextCoverageHashProfileV1,
				PaintProfile:                 documentv2.TextPaintProfileRGBA16SRGBV1,
				LayoutInputHashProfile:       documentv2.TextLayoutInputHashProfileV1,
				UnicodeVersion:               documentv2.TextUnicodeDataVersionV1,
				UnicodeValidationProfile:     documentv2.TextUnicodeValidationTableProfileV1,
				UnicodeValidationHashProfile: documentv2.TextUnicodeValidationTableHashProfileV1,
				UnicodeValidationTableHash:   documentv2.TextUnicodeValidationTableHashV1,
				FontContentHash:              entry.TextFontContentHash, Source: *asset.Text,
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			semanticRaw, err := videoedit.CanonicalJSON(semantic)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			layoutInput := snapshotTextLayoutInputEnvelope{
				SchemaVersion: 1, Profile: documentv2.TextLayoutInputHashProfileV1,
				TextProfile: asset.Text.Profile, EvaluatorProfile: documentv2.EvaluatorTextPinnedFontViewportPXV2,
				FontProfile: documentv2.TextFontProfileStaticGlyfV1, RunInputProfile: documentv2.TextRunInputProfileV1,
				LayoutProfile: documentv2.TextLayoutProfileUnwrappedV1, OutlineProfile: documentv2.TextOutlineProfileQ26_6V1,
				RasterProfile: documentv2.TextRasterProfileSupersample8x8V1, CoverageHashProfile: documentv2.TextCoverageHashProfileV1,
				UnicodeVersion:               documentv2.TextUnicodeDataVersionV1,
				UnicodeValidationProfile:     documentv2.TextUnicodeValidationTableProfileV1,
				UnicodeValidationHashProfile: documentv2.TextUnicodeValidationTableHashProfileV1,
				UnicodeValidationTableHash:   documentv2.TextUnicodeValidationTableHashV1,
				FontContentHash:              entry.TextFontContentHash, Text: asset.Text.Text, FontSize: asset.Text.FontSize,
				LineHeight: *asset.Text.LineHeight, Viewport: *asset.Text.Viewport, Language: asset.Text.Language,
				Script: asset.Text.Script, Direction: asset.Text.Direction, Alignment: asset.Text.Alignment,
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			layoutInputRaw, err := videoedit.CanonicalJSON(layoutInput)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			entry.TextSemanticHash = canonicalDocumentHash(semanticRaw)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			entry.TextLayoutInputHash = canonicalDocumentHash(layoutInputRaw)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if asset.Vector != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			encoded, err := videoedit.CanonicalJSON(*asset.Vector)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// Every VectorSource canonical object appears exactly once inside the complete
			// canonical Document. Their aggregate canonical bytes therefore cannot exceed the
			// already preflight-bounded document bytes; enforce that invariant without creating a
			// second post-allocation resource limit.
			if uint64(len(encoded)) > math.MaxUint64-sourceBytes || sourceBytes+uint64(len(encoded)) > canonicalSize {
				return nil, ErrCanonicalDocumentLimit
			}
			sourceBytes += uint64(len(encoded))
			entry.VectorProfile = asset.Vector.Profile
			if asset.Vector.Viewport != nil {
				entry.VectorViewport = *asset.Vector.Viewport
			}
			entry.VectorSemanticHash = canonicalDocumentHash(encoded)
			entry.CanonicalSourceBytes = uint64(len(encoded))
			entry.PathCount, entry.CommandCount, entry.ConceptualEdgeCount = snapshotVectorCounts(*asset.Vector)
		}
		result.assets[index] = entry
	}
	for timelineIndex, timeline := range frozen.Timelines {
		if err := checkpoint(); err != nil {
			return nil, err
		}
		entry := SnapshotTimeline{ID: timeline.ID, Index: timelineIndex, Tracks: make([]SnapshotTrack, len(timeline.Tracks)), Transitions: make([]SnapshotTransition, len(timeline.Transitions))}
		for trackIndex, track := range timeline.Tracks {
			if err := checkpoint(); err != nil {
				return nil, err
			}
			trackEntry := SnapshotTrack{ID: track.ID, Index: trackIndex, Kind: track.Kind, Clips: make([]SnapshotClip, len(track.Clips))}
			for clipIndex, clip := range track.Clips {
				if err := checkpoint(); err != nil {
					return nil, err
				}
				trackEntry.Clips[clipIndex] = SnapshotClip{ID: clip.ID, Index: clipIndex, AssetID: clip.AssetID, Disabled: clip.Disabled}
			}
			entry.Tracks[trackIndex] = trackEntry
		}
		for transitionIndex, transition := range timeline.Transitions {
			if err := checkpoint(); err != nil {
				return nil, err
			}
			entry.Transitions[transitionIndex] = SnapshotTransition{
				ID: transition.ID, Index: transitionIndex, Profile: transition.Profile,
				OutgoingClipID: transition.OutgoingClipID, IncomingClipID: transition.IncomingClipID,
				PreDuration: transition.PreDuration, PostDuration: transition.PostDuration,
			}
		}
		result.timelines[timelineIndex] = entry
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

type snapshotMeasure struct {
	ctx          context.Context
	visits       uint64
	nodes        uint64
	encodedUpper uint64
	nodeLimit    uint64
	byteLimit    uint64
}

func preflightSnapshotDocument(ctx context.Context, document documentv2.Document, limits SnapshotLimits) error {
	measure := snapshotMeasure{ctx: ctx, nodeLimit: limits.MaxDocumentNodes, byteLimit: limits.MaxCanonicalDocumentBytes}
	if err := measure.addNodes(1); err != nil {
		return err
	}
	if err := measure.walk(reflect.ValueOf(document)); err != nil {
		if canceled := ctx.Err(); canceled != nil {
			return canceled
		}
		return err
	}
	return ctx.Err()
}

func (measure *snapshotMeasure) addNodes(count uint64) error {
	if count > math.MaxUint64-measure.nodes || measure.nodes+count > measure.nodeLimit {
		return ErrCanonicalDocumentLimit
	}
	measure.nodes += count
	return nil
}

func (measure *snapshotMeasure) addEncoded(count uint64) error {
	if count > math.MaxUint64-measure.encodedUpper || measure.encodedUpper+count > measure.byteLimit {
		return ErrCanonicalDocumentLimit
	}
	measure.encodedUpper += count
	return nil
}

func (measure *snapshotMeasure) addEscaped(length int) error {
	if length < 0 || uint64(length) > (math.MaxUint64-2)/6 {
		return ErrCanonicalDocumentLimit
	}
	return measure.addEncoded(uint64(length)*6 + 2)
}

func (measure *snapshotMeasure) walk(value reflect.Value) error {
	measure.visits++
	if measure.visits&255 == 0 {
		if err := measure.ctx.Err(); err != nil {
			return err
		}
	}
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return measure.addEncoded(4)
		}
		if err := measure.addNodes(1); err != nil {
			return err
		}
		return measure.walk(value.Elem())
	case reflect.Struct:
		if err := measure.addEncoded(2); err != nil {
			return err
		}
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.PkgPath != "" {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")
			name := tag[0]
			if name == "-" {
				continue
			}
			if len(tag) > 1 && slicesContain(tag[1:], "omitempty") && snapshotJSONEmpty(value.Field(index)) {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if err := measure.addEscaped(len(name)); err != nil {
				return err
			}
			if err := measure.addEncoded(2); err != nil {
				return err
			}
			if err := measure.walk(value.Field(index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if value.IsNil() {
			return measure.addEncoded(4)
		}
		length := uint64(value.Len())
		if err := measure.addNodes(length); err != nil {
			return err
		}
		if length > (math.MaxUint64-2)/2 {
			return ErrCanonicalDocumentLimit
		}
		if err := measure.addEncoded(2 + length*2); err != nil {
			return err
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := measure.walk(iterator.Key()); err != nil {
				return err
			}
			if err := measure.walk(iterator.Value()); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return measure.addEncoded(4)
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return measure.addEscaped(value.Len())
		}
		length := uint64(value.Len())
		if err := measure.addNodes(length); err != nil {
			return err
		}
		if length > math.MaxUint64-2 {
			return ErrCanonicalDocumentLimit
		}
		if err := measure.addEncoded(2 + length); err != nil {
			return err
		}
		for index := 0; index < value.Len(); index++ {
			if err := measure.walk(value.Index(index)); err != nil {
				return err
			}
		}
		return nil
	case reflect.String:
		return measure.addEscaped(value.Len())
	case reflect.Bool:
		return measure.addEncoded(5)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return measure.addEncoded(24)
	default:
		return fmt.Errorf("%w: unsupported document kind %s", ErrCanonicalDocumentLimit, value.Kind())
	}
}

func snapshotJSONEmpty(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Interface, reflect.Pointer:
		return value.IsZero()
	default:
		return false
	}
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func snapshotVectorCounts(source documentv2.VectorSource) (paths, commands, edges uint64) {
	paths = uint64(len(source.Paths))
	for _, path := range source.Paths {
		commands += uint64(len(path.Commands))
		open := false
		for _, command := range path.Commands {
			switch command.Op {
			case documentv2.VectorMoveTo:
				if open {
					edges++
				}
				open = true
			case documentv2.VectorLineTo:
				edges++
			case documentv2.VectorClose:
				edges++
				open = false
			}
		}
		if open {
			edges++
		}
	}
	return paths, commands, edges
}
