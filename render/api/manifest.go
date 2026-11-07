package renderapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

const ExecutionManifestSchemaVersion = 1

// Decoded evidence profiles freeze the byte layout used for repeatability checks before a render
// starts. Evidence may not select or describe its own interpretation after bytes have been
// produced.
const (
	DecodedVideoProfileRGBA8V1   = "cutly.decoded-video.rgba8.interleaved.top-down.tight.v1"
	DecodedAudioProfilePCM16LEV1 = "cutly.decoded-audio.pcm-s16le.interleaved.v1"
)

var (
	ErrExecutionManifestInvalid             = errors.New("render: execution manifest is invalid")
	ErrExecutionManifestFingerprintMismatch = errors.New("render: execution manifest fingerprint mismatch")
)

var capabilityIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*\.v[1-9][0-9]*$`)

type LocatorScheme string

const (
	LocatorCAS  LocatorScheme = "cas"
	LocatorFile LocatorScheme = "file"
)

// MaterializedResource freezes the exact bytes and controlled locator visible to a worker.
type MaterializedResource struct {
	AssetID          videoedit.ID        `json:"asset_id"`
	Kind             videoedit.AssetKind `json:"kind"`
	ContentHash      string              `json:"content_hash"`
	SizeBytes        int64               `json:"size_bytes"`
	LocatorScheme    LocatorScheme       `json:"locator_scheme"`
	Locator          string              `json:"locator"`
	ProbeFingerprint string              `json:"probe_fingerprint"`
}

type FontResource struct {
	Family      string `json:"family"`
	ContentHash string `json:"content_hash"`
	Locator     string `json:"locator"`
}

type PluginSelection struct {
	ID               string `json:"id"`
	Version          string `json:"version"`
	ContentHash      string `json:"content_hash"`
	BuildFingerprint string `json:"build_fingerprint,omitempty"`
}

type MediaRuntime struct {
	FFmpegBuild     string `json:"ffmpeg_build"`
	DecoderProfile  string `json:"decoder_profile"`
	EncoderProfile  string `json:"encoder_profile"`
	TextShaperBuild string `json:"text_shaper_build,omitempty"`
	ColorProfile    string `json:"color_profile"`
}

type OutputSpec struct {
	ReferenceProfile    string `json:"reference_profile"`
	Format              string `json:"format"`
	Width               int    `json:"width"`
	Height              int    `json:"height"`
	PixelFormat         string `json:"pixel_format"`
	ColorSpace          string `json:"color_space"`
	VideoCodec          string `json:"video_codec"`
	AudioCodec          string `json:"audio_codec"`
	SampleRate          int64  `json:"sample_rate"`
	Channels            int    `json:"channels"`
	DecodedVideoProfile string `json:"decoded_video_profile"`
	DecodedAudioProfile string `json:"decoded_audio_profile"`
}

// ExecutionManifest freezes environment-specific choices for exactly one semantic RenderPlan.
// Machine locators are intentionally here rather than in the semantic plan.
type ExecutionManifest struct {
	SchemaVersion      int                           `json:"schema_version"`
	PlanFingerprint    string                        `json:"plan_fingerprint"`
	SemanticProfile    string                        `json:"semantic_profile"`
	CompilerBuild      string                        `json:"compiler_build"`
	EvaluatorBuild     string                        `json:"evaluator_build"`
	Resources          []MaterializedResource        `json:"resources,omitempty"`
	Fonts              []FontResource                `json:"fonts,omitempty"`
	Plugins            []PluginSelection             `json:"plugins,omitempty"`
	Backend            videoedit.BackendCapabilities `json:"backend"`
	MediaRuntime       MediaRuntime                  `json:"media_runtime"`
	Output             OutputSpec                    `json:"output"`
	DeterministicSeed  int64                         `json:"deterministic_seed"`
	BrowserFingerprint string                        `json:"browser_fingerprint,omitempty"`
	Fingerprint        string                        `json:"fingerprint"`
}

// FreezeExecutionManifest validates, canonicalizes set/order fields, and fingerprints a draft.
func FreezeExecutionManifest(plan videoedit.RenderPlan, draft ExecutionManifest) (ExecutionManifest, error) {
	if err := verifySemanticPlan(plan); err != nil {
		return ExecutionManifest{}, err
	}
	draft = cloneExecutionManifest(draft)
	draft.SchemaVersion = ExecutionManifestSchemaVersion
	draft.PlanFingerprint = plan.Fingerprint
	draft.SemanticProfile = plan.SemanticProfile
	draft.Backend = videoedit.CanonicalBackendCapabilities(draft.Backend)
	draft.Fingerprint = ""
	canonicalizeExecutionManifest(&draft)
	if diagnostics := videoedit.CheckCapabilities(plan, draft.Backend); videoedit.HasErrorDiagnostics(diagnostics) {
		return ExecutionManifest{}, fmt.Errorf("%w: backend preflight: %s at %s", ErrExecutionManifestInvalid, diagnostics[0].Code, diagnostics[0].Path)
	}
	if err := validateManifestAgainstPlan(plan, draft); err != nil {
		return ExecutionManifest{}, err
	}
	if err := validateExecutionManifest(draft); err != nil {
		return ExecutionManifest{}, err
	}
	fingerprint, err := executionManifestFingerprint(draft)
	if err != nil {
		return ExecutionManifest{}, err
	}
	draft.Fingerprint = fingerprint
	return draft, nil
}

// DecodeExecutionManifest strictly decodes and verifies a frozen manifest.
func DecodeExecutionManifest(raw []byte) (ExecutionManifest, error) {
	var manifest ExecutionManifest
	if err := wirejson.Decode(raw, &manifest); err != nil {
		return ExecutionManifest{}, fmt.Errorf("%w: decode: %v", ErrExecutionManifestInvalid, err)
	}
	if err := validateExecutionManifest(manifest); err != nil {
		return ExecutionManifest{}, err
	}
	canonical := cloneExecutionManifest(manifest)
	canonical.Fingerprint = ""
	canonical.Backend = videoedit.CanonicalBackendCapabilities(canonical.Backend)
	canonicalizeExecutionManifest(&canonical)
	if !executionManifestCanonical(manifest, canonical) {
		return ExecutionManifest{}, fmt.Errorf("%w: set and resource fields must be sorted and unique", ErrExecutionManifestInvalid)
	}
	want, err := executionManifestFingerprint(canonical)
	if err != nil {
		return ExecutionManifest{}, err
	}
	if manifest.Fingerprint != want {
		return ExecutionManifest{}, fmt.Errorf("%w: got %q, want %q", ErrExecutionManifestFingerprintMismatch, manifest.Fingerprint, want)
	}
	return manifest, nil
}

func verifySemanticPlan(plan videoedit.RenderPlan) error {
	raw, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("%w: encode semantic plan: %v", ErrExecutionManifestInvalid, err)
	}
	if _, err := videoedit.DecodeRenderPlan(raw); err != nil {
		return fmt.Errorf("%w: semantic plan: %v", ErrExecutionManifestInvalid, err)
	}
	return nil
}

func validateExecutionManifest(manifest ExecutionManifest) error {
	if manifest.SchemaVersion != ExecutionManifestSchemaVersion {
		return fmt.Errorf("%w: schema version %d", ErrExecutionManifestInvalid, manifest.SchemaVersion)
	}
	for _, item := range []struct {
		name  string
		value string
	}{
		{name: "plan_fingerprint", value: manifest.PlanFingerprint},
		{name: "compiler_build", value: manifest.CompilerBuild},
		{name: "evaluator_build", value: manifest.EvaluatorBuild},
		{name: "backend.build_fingerprint", value: manifest.Backend.BuildFingerprint},
		{name: "media_runtime.ffmpeg_build", value: manifest.MediaRuntime.FFmpegBuild},
	} {
		if !validDigest(item.value) {
			return fmt.Errorf("%w: %s must be a sha256 digest", ErrExecutionManifestInvalid, item.name)
		}
	}
	if manifest.SemanticProfile != videoedit.RenderSemanticProfile {
		return fmt.Errorf("%w: semantic profile %q", ErrExecutionManifestInvalid, manifest.SemanticProfile)
	}
	if strings.TrimSpace(manifest.Backend.BackendID) == "" {
		return fmt.Errorf("%w: backend ID is required", ErrExecutionManifestInvalid)
	}
	if manifest.Backend.MaxWidth < 0 || manifest.Backend.MaxHeight < 0 {
		return fmt.Errorf("%w: backend dimension limits must not be negative", ErrExecutionManifestInvalid)
	}
	for index, kind := range manifest.Backend.TrackKinds {
		if kind != videoedit.TrackKindVideo && kind != videoedit.TrackKindAudio {
			return fmt.Errorf("%w: backend track kind %d is invalid", ErrExecutionManifestInvalid, index)
		}
	}
	for index, kind := range manifest.Backend.AssetKinds {
		if !validAssetKind(kind) {
			return fmt.Errorf("%w: backend asset kind %d is invalid", ErrExecutionManifestInvalid, index)
		}
	}
	for index, capability := range manifest.Backend.Features {
		if !capabilityIDPattern.MatchString(string(capability)) {
			return fmt.Errorf("%w: backend capability %d must be a versioned ID", ErrExecutionManifestInvalid, index)
		}
	}
	if strings.TrimSpace(manifest.MediaRuntime.DecoderProfile) == "" || strings.TrimSpace(manifest.MediaRuntime.EncoderProfile) == "" || strings.TrimSpace(manifest.MediaRuntime.ColorProfile) == "" {
		return fmt.Errorf("%w: decoder, encoder and color profiles are required", ErrExecutionManifestInvalid)
	}
	if manifest.MediaRuntime.TextShaperBuild != "" && !validDigest(manifest.MediaRuntime.TextShaperBuild) {
		return fmt.Errorf("%w: text shaper build must be a sha256 digest", ErrExecutionManifestInvalid)
	}
	if manifest.BrowserFingerprint != "" && !validDigest(manifest.BrowserFingerprint) {
		return fmt.Errorf("%w: browser fingerprint must be a sha256 digest", ErrExecutionManifestInvalid)
	}
	if manifest.Output.Width <= 0 || manifest.Output.Height <= 0 || manifest.Output.SampleRate <= 0 || manifest.Output.Channels <= 0 {
		return fmt.Errorf("%w: output dimensions, sample rate and channels must be positive", ErrExecutionManifestInvalid)
	}
	for _, value := range []string{manifest.Output.ReferenceProfile, manifest.Output.Format, manifest.Output.PixelFormat, manifest.Output.ColorSpace, manifest.Output.VideoCodec, manifest.Output.AudioCodec} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: output reference profile, format, pixel/color and codecs are required", ErrExecutionManifestInvalid)
		}
	}
	if manifest.Output.DecodedVideoProfile != DecodedVideoProfileRGBA8V1 ||
		manifest.Output.DecodedAudioProfile != DecodedAudioProfilePCM16LEV1 {
		return fmt.Errorf("%w: unsupported decoded evidence profiles %q / %q", ErrExecutionManifestInvalid, manifest.Output.DecodedVideoProfile, manifest.Output.DecodedAudioProfile)
	}
	for index, resource := range manifest.Resources {
		if resource.AssetID == "" || !validAssetKind(resource.Kind) || !validDigest(resource.ContentHash) || !validDigest(resource.ProbeFingerprint) || resource.SizeBytes <= 0 || strings.TrimSpace(resource.Locator) == "" {
			return fmt.Errorf("%w: resource %d is incomplete", ErrExecutionManifestInvalid, index)
		}
		if index > 0 && manifest.Resources[index-1].AssetID == resource.AssetID {
			return fmt.Errorf("%w: duplicate resource %q", ErrExecutionManifestInvalid, resource.AssetID)
		}
		if resource.LocatorScheme != LocatorCAS && resource.LocatorScheme != LocatorFile {
			return fmt.Errorf("%w: resource %d locator scheme %q", ErrExecutionManifestInvalid, index, resource.LocatorScheme)
		}
		if err := validateMaterializedLocator(resource.LocatorScheme, resource.Locator, resource.ContentHash); err != nil {
			return fmt.Errorf("%w: resource %d locator: %v", ErrExecutionManifestInvalid, index, err)
		}
	}
	for index, font := range manifest.Fonts {
		if strings.TrimSpace(font.Family) == "" || !validDigest(font.ContentHash) || strings.TrimSpace(font.Locator) == "" {
			return fmt.Errorf("%w: font %d is incomplete", ErrExecutionManifestInvalid, index)
		}
		if index > 0 && manifest.Fonts[index-1].Family == font.Family && manifest.Fonts[index-1].ContentHash == font.ContentHash {
			return fmt.Errorf("%w: duplicate font %q", ErrExecutionManifestInvalid, font.Family)
		}
		fontScheme := LocatorFile
		if strings.HasPrefix(font.Locator, "cas://") {
			fontScheme = LocatorCAS
		}
		if err := validateMaterializedLocator(fontScheme, font.Locator, font.ContentHash); err != nil {
			return fmt.Errorf("%w: font %d locator: %v", ErrExecutionManifestInvalid, index, err)
		}
	}
	for index, plugin := range manifest.Plugins {
		if strings.TrimSpace(plugin.ID) == "" || strings.TrimSpace(plugin.Version) == "" || !validDigest(plugin.ContentHash) {
			return fmt.Errorf("%w: plugin %d is incomplete", ErrExecutionManifestInvalid, index)
		}
		if plugin.BuildFingerprint != "" && !validDigest(plugin.BuildFingerprint) {
			return fmt.Errorf("%w: plugin %d build fingerprint", ErrExecutionManifestInvalid, index)
		}
		if index > 0 && manifest.Plugins[index-1].ID == plugin.ID && manifest.Plugins[index-1].Version == plugin.Version {
			return fmt.Errorf("%w: duplicate plugin %q@%s", ErrExecutionManifestInvalid, plugin.ID, plugin.Version)
		}
	}
	if manifest.Fingerprint != "" && !validDigest(manifest.Fingerprint) {
		return fmt.Errorf("%w: fingerprint must be a sha256 digest", ErrExecutionManifestInvalid)
	}
	return nil
}

func validateManifestAgainstPlan(plan videoedit.RenderPlan, manifest ExecutionManifest) error {
	if manifest.Output.Width != plan.Target.Width || manifest.Output.Height != plan.Target.Height ||
		manifest.Output.SampleRate != plan.Target.SampleClock.SamplesPerSecond || manifest.Output.Channels != plan.Target.Channels ||
		manifest.Output.PixelFormat != plan.Target.PixelFormat || manifest.Output.ColorSpace != plan.Target.ColorSpace {
		return fmt.Errorf("%w: output spec does not match semantic plan target", ErrExecutionManifestInvalid)
	}
	if len(manifest.Resources) != len(plan.Resources) {
		return fmt.Errorf("%w: materialized resource count %d does not match plan count %d", ErrExecutionManifestInvalid, len(manifest.Resources), len(plan.Resources))
	}
	for index, planned := range plan.Resources {
		materialized := manifest.Resources[index]
		if materialized.AssetID != planned.AssetID || materialized.Kind != planned.Kind {
			return fmt.Errorf("%w: resource %d does not match planned asset", ErrExecutionManifestInvalid, index)
		}
		if planned.ContentHash != "" && materialized.ContentHash != planned.ContentHash {
			return fmt.Errorf("%w: resource %q content hash changed during materialization", ErrExecutionManifestInvalid, planned.AssetID)
		}
	}
	return nil
}

func canonicalizeExecutionManifest(manifest *ExecutionManifest) {
	sort.Slice(manifest.Resources, func(i, j int) bool { return manifest.Resources[i].AssetID < manifest.Resources[j].AssetID })
	sort.Slice(manifest.Fonts, func(i, j int) bool {
		if manifest.Fonts[i].Family == manifest.Fonts[j].Family {
			return manifest.Fonts[i].ContentHash < manifest.Fonts[j].ContentHash
		}
		return manifest.Fonts[i].Family < manifest.Fonts[j].Family
	})
	sort.Slice(manifest.Plugins, func(i, j int) bool {
		if manifest.Plugins[i].ID == manifest.Plugins[j].ID {
			return manifest.Plugins[i].Version < manifest.Plugins[j].Version
		}
		return manifest.Plugins[i].ID < manifest.Plugins[j].ID
	})
}

func cloneExecutionManifest(manifest ExecutionManifest) ExecutionManifest {
	clone := manifest
	clone.Resources = append([]MaterializedResource(nil), manifest.Resources...)
	clone.Fonts = append([]FontResource(nil), manifest.Fonts...)
	clone.Plugins = append([]PluginSelection(nil), manifest.Plugins...)
	clone.Backend.TrackKinds = append([]videoedit.TrackKind(nil), manifest.Backend.TrackKinds...)
	clone.Backend.AssetKinds = append([]videoedit.AssetKind(nil), manifest.Backend.AssetKinds...)
	clone.Backend.Features = append([]videoedit.CapabilityID(nil), manifest.Backend.Features...)
	return clone
}

func executionManifestCanonical(got, canonical ExecutionManifest) bool {
	got.Fingerprint = ""
	left, leftErr := videoedit.CanonicalJSON(got)
	right, rightErr := videoedit.CanonicalJSON(canonical)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func executionManifestFingerprint(manifest ExecutionManifest) (string, error) {
	manifest.Fingerprint = ""
	canonical, err := videoedit.CanonicalJSON(manifest)
	if err != nil {
		return "", fmt.Errorf("%w: canonical JSON: %v", ErrExecutionManifestInvalid, err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	hexValue := strings.TrimPrefix(value, "sha256:")
	if hexValue != strings.ToLower(hexValue) {
		return false
	}
	_, err := hex.DecodeString(hexValue)
	return err == nil
}

func validAssetKind(kind videoedit.AssetKind) bool {
	switch kind {
	case videoedit.AssetKindVideo, videoedit.AssetKindAudio, videoedit.AssetKindImage, videoedit.AssetKindFont, videoedit.AssetKindData:
		return true
	default:
		return false
	}
}

func validateMaterializedLocator(scheme LocatorScheme, locator, contentHash string) error {
	if strings.ContainsRune(locator, 0) {
		return errors.New("NUL bytes are forbidden")
	}
	switch scheme {
	case LocatorCAS:
		want := "cas://sha256/" + strings.TrimPrefix(contentHash, "sha256:")
		if locator != want {
			return fmt.Errorf("CAS locator must equal content identity %q", want)
		}
	case LocatorFile:
		if !filepath.IsAbs(locator) || filepath.Clean(locator) != locator {
			return errors.New("file locator must be an absolute clean path")
		}
	default:
		return fmt.Errorf("unsupported scheme %q", scheme)
	}
	return nil
}
