package reconstructv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"strings"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/media/wavdecode"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/preparev2"
	"github.com/CutlyLab/Cutly/render/referencevisual"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/wavsource"
)

var ErrConfig = errors.New("reconstructv2: invalid host configuration")

const ConfigProfileV1 = "cutly.reconstruct.document-v2.native-static-wav-config.v1"

// Limits are explicit portable logical admission charges, not an RSS guarantee. Initial
// reconstruction permits budgets no larger than DefaultLimits; zero never means unlimited.
// Working and retained reservations deliberately sum sequential native-stage maxima.
type Limits struct {
	MaxResources          uint64 `json:"max_resources"`
	MaxResourceBytes      uint64 `json:"max_resource_bytes"`
	MaxTotalResourceBytes uint64 `json:"max_total_resource_bytes"`
	MaxWorkingBytes       uint64 `json:"max_working_bytes"`
	MaxRetainedBytes      uint64 `json:"max_retained_bytes"`
}

func DefaultLimits() Limits {
	return Limits{MaxResources: 4096, MaxResourceBytes: 1 << 30, MaxTotalResourceBytes: 2 << 30, MaxWorkingBytes: 64 << 30, MaxRetainedBytes: 16 << 30}
}

// Config is trusted, immutable host policy, never populated from a serialized environment.
// Every native limit must be explicit, positive and no larger than its initial native default.
// The font lineage build profile is fixed to defaultFontBuildLimits, not caller-injectable.
// Resources and Authority must remain concurrency-safe for the Factory's lifetime.
type Config struct {
	Authority      *preparev2.Authority
	Resources      ResourceSource
	Visual         referencevisual.Config
	Audio          audiograph.Limits
	WAV            wavsource.Config
	PCM            pcm16le.Limits
	PNGInspection  pngdecode.Limits
	FontInspection sfntglyf.Limits
	WAVInspection  wavdecode.Limits
	Limits         Limits
}

// DefaultConfig supplies explicit finite initial-profile budgets; callers may narrow them.
func DefaultConfig(width, height int, authority *preparev2.Authority, resources ResourceSource) Config {
	return Config{Authority: authority, Resources: resources, Visual: referencevisual.DefaultConfigV2(width, height), Audio: audiograph.DefaultLimits(), WAV: wavsource.DefaultConfig(), PCM: pcm16le.DefaultLimits(), PNGInspection: pngdecode.DefaultLimits(), FontInspection: sfntglyf.DefaultLimits(), WAVInspection: wavdecode.DefaultLimits(), Limits: DefaultLimits()}
}

// Factory owns configuration values; no mutable native-config pointer is exposed.
// It does not grant execution authority from a serialized claim.
type Factory struct {
	config     Config
	configHash string
}

type admissionCharge struct{ resourceBytes, workingBytes, retainedBytes uint64 }

// admittedEnvironment is private no-IO admission evidence, not a live native program.
// Authorization and retained-byte inspection must still follow before reconstruction.
type admittedEnvironment struct {
	verified    prepared.VerifiedEnvironmentV2
	environment prepared.EnvironmentV2
	scopes      reconstructionScopes
	charge      admissionCharge
	configHash  string
}

// New validates configuration using resource-free gap preflights. The dummy graph is never
// retained or used as runtime identity. Neither ResourceSource nor Authority resource callbacks
// are invoked; Authority must already have been constructed by trusted host code.
func New(ctx context.Context, config Config) (result *Factory, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = ErrPanic
		}
		if err != nil {
			result = nil
		}
	}()
	if ctx == nil {
		return nil, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if config.Authority == nil || nilResourceSource(config.Resources) || config.Visual.TextConfig == nil {
		return nil, ErrConfig
	}
	config = ownFactoryConfig(config)
	if config.Visual.Width <= 0 || config.Visual.Height <= 0 || config.Visual.Width > 8192 || config.Visual.Height > 8192 {
		return nil, ErrConfig
	}
	defaults := referencevisual.DefaultConfigV2(config.Visual.Width, config.Visual.Height)
	if !positiveWithin(config.Visual, defaults) || !positiveWithin(config.Audio, audiograph.DefaultLimits()) || !positiveWithin(config.WAV, wavsource.DefaultConfig()) || !positiveWithin(config.PCM, pcm16le.DefaultLimits()) || !positiveWithin(config.PNGInspection, pngdecode.DefaultLimits()) || !positiveWithin(config.FontInspection, sfntglyf.DefaultLimits()) || !positiveWithin(config.WAVInspection, wavdecode.DefaultLimits()) || !positiveWithin(config.Limits, DefaultLimits()) {
		return nil, ErrConfig
	}
	if config.PCM.MaxExactBits < config.Audio.MaxExactBits || config.PCM.MaxOutputBytes < pcm16le.BytesPerFrame || config.Limits.MaxResourceBytes > config.Limits.MaxTotalResourceBytes || config.PNGInspection.MaxEncodedBytes < 8 || config.PNGInspection.MaxWorkingBytes < 30 || config.FontInspection.MaxEncodedBytes < 12 || config.WAVInspection.MaxEncodedBytes < 44 || config.WAV.Limits.WAV.MaxEncodedBytes < 44 {
		return nil, ErrConfig
	}
	document, visual, audio := preflightGap()
	if _, d, e := referencevisual.PreflightDomainV2(ctx, document, visual, config.Visual); e != nil || documentv2.HasErrorDiagnostics(d) {
		return nil, ErrConfig
	}
	if _, d := audiograph.PreflightDomain(ctx, document, audio, fixedAudioOffered(), config.Audio); documentv2.HasErrorDiagnostics(d) {
		return nil, ErrConfig
	}
	// The source compiler receives no bindings and no audio clips, so this validates its
	// complete native configuration without decoding bytes or invoking a provider.
	if _, e := wavsource.Compile(ctx, document, nil, config.WAV); e != nil {
		return nil, ErrConfig
	}
	if _, e := textlayer.Compile(ctx, document, nil, *config.Visual.TextConfig); e != nil {
		return nil, ErrConfig
	}
	factory := &Factory{config: config}
	if _, err = factory.aggregate(ctx, prepared.EnvironmentV2{}); err != nil {
		return nil, err
	}
	identity := struct {
		Profile       string                 `json:"profile"`
		Visual        referencevisual.Config `json:"visual"`
		Audio         audiograph.Limits      `json:"audio"`
		WAV           wavsource.Config       `json:"wav"`
		PCM           pcm16le.Limits         `json:"pcm"`
		PNG           pngdecode.Limits       `json:"png"`
		Font          sfntglyf.Limits        `json:"font"`
		WAVInspection wavdecode.Limits       `json:"wav_inspection"`
		FontBuild     fontBuildLimits        `json:"font_build"`
		Limits        Limits                 `json:"limits"`
	}{ConfigProfileV1, config.Visual, config.Audio, config.WAV, config.PCM, config.PNGInspection, config.FontInspection, config.WAVInspection, defaultFontBuildLimits(), config.Limits}
	encoded, e := videoedit.CanonicalJSON(identity)
	if e != nil {
		return nil, ErrConfig
	}
	sum := sha256.Sum256(encoded)
	factory.configHash = "sha256:" + hex.EncodeToString(sum[:])
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return factory, nil
}

func ownFactoryConfig(config Config) Config {
	if config.Visual.TextConfig != nil {
		value := *config.Visual.TextConfig
		config.Visual.TextConfig = &value
	}
	return config
}
func nilResourceSource(source ResourceSource) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice:
		return value.IsNil()
	}
	return false
}

// Only fixed config structs are walked, not request graphs; strings are checked by native APIs.
func positiveWithin(value, maximum any) bool {
	return positiveWithinValue(reflect.ValueOf(value), reflect.ValueOf(maximum))
}
func positiveWithinValue(value, maximum reflect.Value) bool {
	switch value.Kind() {
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if !positiveWithinValue(value.Field(i), maximum.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Pointer:
		return !value.IsNil() && !maximum.IsNil() && positiveWithinValue(value.Elem(), maximum.Elem())
	case reflect.Int, reflect.Int64, reflect.Int32:
		return value.Int() > 0 && value.Int() <= maximum.Int()
	case reflect.Uint, reflect.Uint64, reflect.Uint32, reflect.Uint16:
		return value.Uint() > 0 && value.Uint() <= maximum.Uint()
	case reflect.String:
		return true
	default:
		return false
	}
}
func preflightGap() (documentv2.Document, evaluator.DomainScope, evaluator.DomainScope) {
	document := documentv2.Document{SchemaVersion: 2, ID: "reconstruct-config-admission", Timelines: []documentv2.Timeline{{ID: "gap"}}}
	visual := evaluator.DomainScope{Domain: evaluator.DomainVisual, Selection: documentv2.CapabilitySelection{TimelineID: "gap"}, RequestedRange: documentv2.TimeRange{Start: documentv2.RationalTime{Rate: 1}, Duration: documentv2.RationalTime{Value: 1, Rate: 1}}}
	audio := visual
	audio.Domain = evaluator.DomainAudio
	return document, visual, audio
}

// fixedAudioOffered declares only semantics implemented by this native graph/provider pair.
// Requirements are never copied into an offered declaration. Varispeed/routes/extensions excluded.
func fixedAudioOffered() videoedit.BackendCapabilities {
	return videoedit.BackendCapabilities{BackendID: "cutly.reconstruct.native-wav.v1", TrackKinds: []videoedit.TrackKind{"audio"}, AssetKinds: []videoedit.AssetKind{"audio"}, Features: []videoedit.CapabilityID{
		documentv2.CapabilityAudioGainV1, documentv2.CapabilityAudioPanV1, documentv2.CapabilityAudioChannelMatrixV1, documentv2.CapabilityKeyframesV1, documentv2.CapabilityInterpolationHoldV1, documentv2.CapabilityInterpolationLinearV1, documentv2.CapabilityInterpolationCubicV1,
	}}
}

func (factory *Factory) preflight(ctx context.Context, input prepared.EnvironmentV2) (result *admittedEnvironment, err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if err != nil {
			result = nil
		}
	}()
	if ctx == nil {
		return nil, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if factory == nil || factory.configHash == "" || factory.config.Authority == nil || nilResourceSource(factory.config.Resources) {
		return nil, ErrConfig
	}
	verified, err := prepared.VerifyEnvironmentV2(ctx, input)
	if err != nil {
		return nil, err
	}
	environment, err := verified.Environment(ctx)
	if err != nil {
		return nil, err
	}
	scopes, err := selectScopes(ctx, environment.Document, environment.Plan)
	if err != nil {
		return nil, err
	}
	if err = factory.config.Authority.CheckEnvironmentPins(ctx, environment.Manifest); err != nil {
		return nil, err
	}
	if err = factory.checkOutput(environment); err != nil {
		return nil, err
	}
	charge, err := factory.aggregate(ctx, environment)
	if err != nil {
		return nil, err
	}
	visual, d, err := referencevisual.PreflightDomainV2(ctx, environment.Document, scopes.visual, factory.config.Visual)
	if err != nil || documentv2.HasErrorDiagnostics(d) || visual == nil {
		return nil, errors.Join(ErrUnsupported, err)
	}
	if visual.DocumentHash() != environment.Plan.DocumentHash {
		return nil, ErrUnsupported
	}
	audio, d := audiograph.PreflightDomain(ctx, environment.Document, scopes.audio, fixedAudioOffered(), factory.config.Audio)
	if documentv2.HasErrorDiagnostics(d) || audio.DocumentHash != environment.Plan.DocumentHash {
		return nil, ErrUnsupported
	}
	if err = factory.admitSourceCounts(ctx, environment); err != nil {
		return nil, err
	}
	if err = factory.admitWAVDocument(ctx, environment); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return &admittedEnvironment{verified: verified, environment: environment, scopes: scopes, charge: charge, configHash: factory.configHash}, nil
}

func (factory *Factory) checkOutput(environment prepared.EnvironmentV2) error {
	output := environment.Manifest.Output
	plan := environment.Plan
	// Codec/format selection is not an execution permission; this profile is specifically the
	// native linear RGBA16 + stereo PCM16 handoff. Joint verification already binds every clock.
	if int(output.Width) != factory.config.Visual.Width || int(output.Height) != factory.config.Visual.Height || output.Channels != 2 || output.PixelFormat != "rgba16" || output.ColorSpace != "linear-srgb" || output.FrameClock != plan.Target.FrameClock || output.SampleClock != plan.Target.SampleClock || output.SampleRate != output.SampleClock.SamplesPerSecond || output.SampleRate > int64(factory.config.WAV.Limits.WAV.MaxSampleRate) || output.SampleRate > int64(factory.config.WAVInspection.MaxSampleRate) || plan.FrameRange.First < 0 || plan.SampleRange.First < 0 {
		return ErrUnsupported
	}
	if output.ReferenceProfile != renderapi.OutputReferenceProfileV2 || output.Intermediate.VideoSurfaceProfile != renderapi.VideoSurfaceProfileV2 || output.Intermediate.AudioBlockProfile != renderapi.AudioBlockProfileV2 || output.DecodedVideoProfile != renderapi.DecodedVideoProfileRGBA8V1 || output.DecodedAudioProfile != renderapi.DecodedAudioProfilePCM16LEV1 {
		return ErrUnsupported
	}
	return nil
}

// Source-local count limits are not all exercised by the compositor-only visual preflight.
// Check the complete selected closure before any resource verifier can run.
func (factory *Factory) admitSourceCounts(ctx context.Context, environment prepared.EnvironmentV2) error {
	config := factory.config.Visual
	var images, vectors, texts int
	imageAssets, vectorAssets, textAssets := map[documentv2.ID]bool{}, map[documentv2.ID]bool{}, map[documentv2.ID]bool{}
	for _, clip := range environment.Plan.Visual {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch clip.Binding.AssetKind {
		case documentv2.AssetKindImage:
			images++
			imageAssets[clip.Binding.AssetID] = true
		case documentv2.AssetKindVector:
			vectors++
			vectorAssets[clip.Binding.AssetID] = true
		case documentv2.AssetKindText:
			texts++
			textAssets[clip.Binding.AssetID] = true
		default:
			return ErrUnsupported
		}
	}
	fonts := 0
	for _, resource := range environment.Manifest.Resources {
		if resource.Kind == documentv2.AssetKindFont {
			fonts++
		}
	}
	if images > config.NativeLimits.MaxClipScopes || len(imageAssets) > config.NativeLimits.MaxBindings || vectors > config.ExpressionLimits.MaxClipScopes || len(vectorAssets) > config.ExpressionLimits.MaxExpressionAssets || texts > config.TextConfig.Limits.MaxClipScopes || len(textAssets) > config.TextConfig.Limits.MaxTextAssets || fonts > config.TextConfig.Limits.MaxFontAssets || fonts > config.TextConfig.Limits.MaxBindings {
		return ErrLimit
	}
	tracks := len(environment.Document.Timelines[0].Tracks)
	if tracks > config.NativeLimits.MaxTracksPerTimeline || tracks > config.ExpressionLimits.MaxTracksPerTimeline || tracks > config.TextConfig.Limits.MaxTracksPerTimeline {
		return ErrLimit
	}
	return ctx.Err()
}

func (factory *Factory) admitWAVDocument(ctx context.Context, environment prepared.EnvironmentV2) error {
	l := factory.config.WAV.Limits
	visual := factory.config.Visual
	// The compositor-only preflight does not build its source providers. Admit the original
	// graph under the narrowest source document policy as well, without decoding any resource.
	bytes := min(l.MaxCanonicalDocumentBytes, visual.NativeLimits.MaxCanonicalDocumentBytes, visual.ExpressionLimits.MaxCanonicalDocumentBytes, visual.TextConfig.Limits.MaxCanonicalDocumentBytes)
	nodes := min(l.MaxDocumentNodes, visual.NativeLimits.MaxDocumentNodes, visual.ExpressionLimits.MaxDocumentNodes, visual.TextConfig.Limits.MaxDocumentNodes)
	// The three raw source walkers count omitted fields too, unlike FreezeSnapshot. Reuse
	// the fixed expression source's identical conservative walker under their minimum policy.
	// Its vector rasterization is resource-free, and its discarded provider is covered by the
	// already-admitted native compile envelope. No alternate Document or identity is created.
	expression := expressionlayer.Config{Limits: visual.ExpressionLimits}
	expression.Limits.MaxCanonicalDocumentBytes = bytes
	expression.Limits.MaxDocumentNodes = nodes
	if _, err := expressionlayer.Compile(ctx, environment.Document, expression); err != nil {
		return errors.Join(ErrLimit, err)
	}
	// Text also caps its evaluator snapshot by its total compile workspace.
	bytes = min(bytes, visual.TextConfig.Limits.MaxCompileWorkingBytes)
	snapshot, d := evaluator.FreezeSnapshot(ctx, environment.Document, evaluator.SnapshotLimits{MaxCanonicalDocumentBytes: bytes, MaxDocumentNodes: uint64(nodes)})
	if snapshot == nil || documentv2.HasErrorDiagnostics(d) {
		return ErrLimit
	}
	if len(environment.Plan.Audio) > l.MaxClipScopes {
		return ErrLimit
	}
	count := 0
	for _, resource := range environment.Manifest.Resources {
		if resource.Kind == documentv2.AssetKindAudio {
			count++
		}
	}
	if count > l.MaxBindings || count > factory.config.Audio.MaxBindings || len(environment.Document.Timelines[0].Tracks) > l.MaxTracksPerTimeline {
		return ErrLimit
	}
	return factory.admitTextDocument(ctx, environment, snapshot)
}

// Text geometry depends on font bytes, but viewport storage and frozen text binding do not.
func (factory *Factory) admitTextDocument(ctx context.Context, environment prepared.EnvironmentV2, snapshot *evaluator.FrozenSnapshot) error {
	limits := factory.config.Visual.TextConfig.Limits
	selected := make(map[documentv2.ID]bool)
	for _, clip := range environment.Plan.Visual {
		if clip.Binding.AssetKind == documentv2.AssetKindText {
			selected[clip.Binding.AssetID] = true
		}
	}
	var surfaces, rasterWorkingLower uint64
	for _, asset := range environment.Document.Assets {
		if !selected[asset.ID] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if asset.Text == nil || asset.Text.Viewport == nil {
			return ErrUnsupported
		}
		viewport := *asset.Text.Viewport
		pixels := uint64(viewport.WidthPixels) * uint64(viewport.HeightPixels)
		layout, raster := limits.Assembly.Layout, limits.Raster.Raster
		if viewport.WidthPixels > layout.MaxWidth || viewport.HeightPixels > layout.MaxHeight || pixels > layout.MaxPixels || viewport.WidthPixels > raster.MaxWidth || viewport.HeightPixels > raster.MaxHeight || pixels > raster.MaxPixels || uint64(len(asset.Text.Text)) > layout.MaxTextBytes || pixels > math.MaxUint64/8 {
			return ErrLimit
		}
		// Match the native shape scan: LF separates lines and is not an input scalar.
		var scalars, lineScalars uint64
		for offset, scalar := range asset.Text.Text {
			if offset&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if scalar == '\n' {
				lineScalars = 0
				continue
			}
			scalars++
			lineScalars++
			if scalars > limits.Assembly.Shape.MaxScalars || lineScalars > limits.Assembly.Shape.MaxScalarsPerLine {
				return ErrLimit
			}
		}
		lines := uint64(strings.Count(asset.Text.Text, "\n")) + 1
		if uint64(len(asset.Text.Text)) > limits.Assembly.Shape.MaxTextBytes || lines > limits.Assembly.MaxLines || lines > limits.Assembly.Shape.MaxLines || lines > layout.MaxLines {
			return ErrLimit
		}
		// These are the fixed native textraster admission bounds, with zero glyph/geometry
		// terms. K is an inner preadmission upper, not the outer exact canonical charge.
		if pixels > (math.MaxUint64-(16<<10))/9 || uint64(viewport.WidthPixels) > (math.MaxUint64-8-(64<<10))/72 {
			return ErrLimit
		}
		canonical := 9*pixels + (16 << 10)
		rowWorking := 72*uint64(viewport.WidthPixels) + 8 + (64 << 10)
		if pixels > (math.MaxUint64-rowWorking)/9 || pixels > (math.MaxUint64-(8<<10))/8 || pixels > (math.MaxUint64-canonical)/80 {
			return ErrLimit
		}
		working, retained, work := 9*pixels+rowWorking, 8*pixels+(8<<10), 80*pixels+canonical
		if canonical > raster.MaxCanonicalBinaryBytes || work > raster.MaxWorkUnits || work > limits.Raster.MaxWorkUnits || working > raster.MaxWorkingBytes || working > limits.Raster.MaxWorkingBytes || retained > raster.MaxRetainedBytes || retained > limits.Raster.MaxRetainedBytes || retained > limits.Raster.MaxWorkingBytes {
			return ErrLimit
		}
		rasterWorkingLower = max(rasterWorkingLower, working)
		bytes := pixels * 8
		if surfaces > math.MaxUint64-bytes {
			return ErrLimit
		}
		surfaces += bytes
	}
	if surfaces > limits.MaxTotalStaticSurfaceBytes || snapshot.CanonicalBytes() > limits.MaxCompileWorkingBytes || surfaces > limits.MaxCompileWorkingBytes-snapshot.CanonicalBytes() {
		return ErrLimit
	}
	budget := limits.MaxCompileWorkingBytes - snapshot.CanonicalBytes() - surfaces
	if budget < 64<<10 || rasterWorkingLower > budget {
		return ErrLimit
	}
	frozen := limits.FrozenText
	frozen.MaxWorkingBytes = min(frozen.MaxWorkingBytes, budget)
	for index, asset := range environment.Document.Assets {
		if selected[asset.ID] {
			if _, _, err := snapshot.BindTextSource(ctx, asset.ID, index, frozen); err != nil {
				return errors.Join(ErrLimit, err)
			}
		}
	}
	return ctx.Err()
}

// aggregate reserves full native configured envelopes even for a gap, plus every encoded input
// snapshot and per-resource inspection/retained lineage. Summing sequential work is intentional.
// Audio exact rationals reserve two numerator/denominator pairs per stereo sample at MaxExactBits
// plus 256 bytes/sample of slice/value bookkeeping. Metadata charges 512 bytes/document node;
// 8 canonical copies cover environment/native snapshots and receipts. These are logical charges.
func (factory *Factory) aggregate(ctx context.Context, environment prepared.EnvironmentV2) (charge admissionCharge, err error) {
	config := factory.config
	add := func(total *uint64, n uint64) bool {
		if *total > math.MaxUint64-n {
			return false
		}
		*total += n
		return true
	}
	mul := func(a, b uint64) (uint64, bool) {
		if a != 0 && b > math.MaxUint64/a {
			return 0, false
		}
		return a * b, true
	}
	reserve := func(working, retained uint64) bool {
		return add(&charge.workingBytes, working) && add(&charge.retainedBytes, retained)
	}
	canonical := max(config.Visual.CompositorLimits.MaxCanonicalDocumentBytes, config.Audio.MaxCanonicalDocumentBytes, config.WAV.Limits.MaxCanonicalDocumentBytes)
	nodes := max(uint64(config.Visual.CompositorLimits.MaxDocumentNodes), uint64(config.Audio.MaxDocumentNodes), uint64(config.WAV.Limits.MaxDocumentNodes))
	canonicalBytes, ok := mul(canonical, 8)
	if !ok {
		return charge, ErrLimit
	}
	metadata, ok := mul(nodes, 512)
	if !ok {
		return charge, ErrLimit
	}
	if !reserve(canonicalBytes, canonicalBytes) || !reserve(metadata, metadata) || !reserve(config.Visual.PipelineLimits.MaxCompileWorkingBytes, config.Visual.PipelineLimits.MaxRetainedSourceBytes) || !reserve(config.Visual.PipelineLimits.MaxRuntimeWorkingBytes, 0) || !reserve(config.WAV.Limits.WAV.MaxWorkingBytes, config.WAV.Limits.MaxRetainedBytes) {
		return charge, ErrLimit
	}
	bytesPerSample, ok := mul((uint64(config.Audio.MaxExactBits)+7)/8, 4)
	if !ok || !add(&bytesPerSample, 256) {
		return charge, ErrLimit
	}
	block, ok := mul(uint64(config.Audio.MaxBlockSamples), bytesPerSample)
	if !ok || !reserve(block, block) {
		return charge, ErrLimit
	}
	if !reserve(config.PCM.MaxOutputBytes, config.PCM.MaxOutputBytes) {
		return charge, ErrLimit
	}
	if uint64(len(environment.Manifest.Resources)) > config.Limits.MaxResources {
		return charge, ErrLimit
	}
	var wavBytes, wavRetained, pngBytes uint64
	fontWorking, fontRetained, e := fontBuildEnvelope(defaultFontBuildLimits())
	if e != nil {
		return charge, e
	}
	for _, resource := range environment.Manifest.Resources {
		if err = ctx.Err(); err != nil {
			return charge, err
		}
		if resource.SizeBytes <= 0 || uint64(resource.SizeBytes) > config.Limits.MaxResourceBytes || uint64(resource.SizeBytes) > uint64(^uint(0)>>1) {
			return charge, ErrLimit
		}
		size := uint64(resource.SizeBytes)
		if !add(&charge.resourceBytes, size) || !reserve(size, size) {
			return charge, ErrLimit
		}
		switch resource.Kind {
		case documentv2.AssetKindImage:
			if size > config.PNGInspection.MaxEncodedBytes || size > config.Visual.NativeLimits.PNG.MaxEncodedBytes || !add(&pngBytes, size) || !reserve(config.PNGInspection.MaxWorkingBytes, config.PNGInspection.MaxWorkingBytes) {
				return charge, ErrLimit
			}
		case documentv2.AssetKindFont:
			if size > config.FontInspection.MaxEncodedBytes || !reserve(config.FontInspection.MaxWorkingBytes, config.FontInspection.MaxRetainedBytes) || !reserve(fontWorking, fontRetained) {
				return charge, ErrLimit
			}
		case documentv2.AssetKindAudio:
			if size > 44 && !add(&wavRetained, size-44) {
				return charge, ErrLimit
			}
			if size > config.WAVInspection.MaxEncodedBytes || size > config.WAV.Limits.WAV.MaxEncodedBytes || !add(&wavBytes, size) || !reserve(config.WAVInspection.MaxWorkingBytes, config.WAVInspection.MaxWorkingBytes) {
				return charge, ErrLimit
			}
		default:
			return charge, ErrUnsupported
		}
	}
	// Working is a simultaneous-live upper envelope, not transient-only workspace.
	// Add all retained maxima as well, intentionally double-charging stages already
	// counted in both columns, and covering discarded preflight vector surfaces.
	if !add(&charge.workingBytes, charge.retainedBytes) {
		return charge, ErrLimit
	}
	if pngBytes > config.Visual.NativeLimits.MaxTotalEncodedBytes || wavBytes > config.WAV.Limits.MaxTotalEncodedBytes || wavRetained > config.WAV.Limits.MaxRetainedBytes || charge.resourceBytes > config.Limits.MaxTotalResourceBytes || charge.workingBytes > config.Limits.MaxWorkingBytes || charge.retainedBytes > config.Limits.MaxRetainedBytes {
		return charge, ErrLimit
	}
	return charge, nil
}
