package worker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/font/sfntglyf"
	"github.com/CutlyLab/Cutly/internal/buildidentity"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	decode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/pngdecode"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/media/runtimebundle"
	"github.com/CutlyLab/Cutly/media/wavdecode"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/preparev2"
	"github.com/CutlyLab/Cutly/render/reconstructv2"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
)

const MaxLaunchConfigBytesV2 = 8 << 20
const LaunchConfigProfileV2 = "cutly.render.worker-host-launch.document-v2.v2"

// LaunchConfigV2 is HOST policy, not a request. Deliver only through an inherited
// private descriptor owned by the launching host uid. Never derive it from Document
// locators. Resources map exact original document+claim to host-selected file paths.
// This is process-group isolation, not a filesystem/network/OS resource sandbox.
type LaunchConfigV2 struct {
	Profile               string                    `json:"profile"`
	AllowLocalDevelopment bool                      `json:"allow_local_development"`
	CompilerBuild         string                    `json:"compiler_build"`
	EvaluatorBuild        string                    `json:"evaluator_build"`
	Backend               api.BackendCapabilitiesV2 `json:"backend"`
	MediaRuntime          api.MediaRuntime          `json:"media_runtime"`
	Width                 int                       `json:"width"`
	Height                int                       `json:"height"`
	Resources             []HostResourceV2          `json:"resources"`
	Video                 *HostVideoV2              `json:"video,omitempty"`
}
type HostResourceV2 struct {
	DocumentHash string                     `json:"document_hash"`
	Claim        api.MaterializedResourceV2 `json:"claim"`
	Path         string                     `json:"path"`
}
type HostVideoV2 struct {
	BundleRoot     string            `json:"bundle_root"`
	TrustedKeys    map[string]string `json:"trusted_keys"`
	CPUFingerprint string            `json:"cpu_fingerprint"`
	Scratch        string            `json:"scratch"`
}

// OpenLaunchConfigV2 opens a host-selected private launch descriptor without
// following the final symlink. The host must keep all ancestors host-owned.
func OpenLaunchConfigV2(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrRunner
	}
	f, err := openPrivateRegularV2(path)
	if err != nil {
		return nil, err
	}
	if err = checkLaunchDescriptorV2(f); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

// ReadLaunchConfigV2 uses ReadAt: independent offsets allow concurrent launches
// inheriting the same descriptor. No path is reopened, and metadata is rechecked.
func ReadLaunchConfigV2(file *os.File) (out LaunchConfigV2, err error) {
	defer func() {
		if recover() != nil {
			err = ErrRunner
		}
		if err != nil {
			out = LaunchConfigV2{}
		}
	}()
	if err = checkLaunchDescriptorV2(file); err != nil {
		return out, err
	}
	before, err := file.Stat()
	if err != nil {
		return out, err
	}
	raw := make([]byte, int(before.Size()))
	if _, err = io.ReadFull(io.NewSectionReader(file, 0, before.Size()), raw); err != nil {
		return out, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return out, ErrRunner
	}
	if err = wirejson.Decode(raw, &out); err != nil {
		return out, err
	}
	if out.Profile != LaunchConfigProfileV2 || out.Width <= 0 || out.Height <= 0 || out.Width > 8192 || out.Height > 8192 || out.Resources == nil || len(out.Resources) > 4096 {
		return out, ErrRunner
	}
	return out, nil
}

// NewLaunchReconstructV2 installs independent host policy and a measured, signed
// runtime bundle when video is enabled. Adapter executables NEVER come from stdin.
func NewLaunchReconstructV2(ctx context.Context, input LaunchConfigV2) (ReconstructV2, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	identity, err := measureBindingV2(ctx, self)
	if err != nil {
		return nil, err
	}
	return newLaunchReconstructV2(ctx, input, self, &identity)
}

// AuthorizeLaunchEnvironmentV2 checks independent host policy and actual retained
// resource inspections before enqueue. It measures the selected CHILD executable,
// never the daemon. Reconstruction is closed without rendering any chunk.
func AuthorizeLaunchEnvironmentV2(ctx context.Context, config LaunchConfigV2, providerExecutable string, env prepared.EnvironmentV2) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrRunner
		}
	}()
	measured, err := measureAuthorizedV2(ctx, providerExecutable)
	if err != nil {
		return err
	}
	if measured != env.Manifest.ProviderBuild {
		return ErrProtocol
	}
	reconstruct, err := newLaunchReconstructV2(ctx, config, providerExecutable, nil)
	if err != nil {
		return err
	}
	bundle, err := reconstruct(ctx, env)
	if bundle != nil {
		err = errors.Join(err, closeNativeV2(bundle))
	}
	return err
}

func newLaunchReconstructV2(ctx context.Context, input LaunchConfigV2, providerExecutable string, binding *buildidentity.ProviderExecutableIdentity) (result ReconstructV2, err error) {
	defer func() {
		if recover() != nil {
			err = ErrRunner
		}
		if err != nil {
			result = nil
		}
	}()
	if ctx == nil {
		return nil, ErrRunner
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	// Own all policy including nested backend/claim pointers before retaining it.
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > MaxLaunchConfigBytesV2 {
		return nil, ErrRunner
	}
	var config LaunchConfigV2
	if err = wirejson.Decode(raw, &config); err != nil {
		return nil, err
	}
	if config.Profile != LaunchConfigProfileV2 || config.Resources == nil || len(config.Resources) > 4096 {
		return nil, ErrRunner
	}
	source := &hostSourceV2{resources: config.Resources}
	seen := map[string]bool{}
	for _, r := range source.resources {
		if !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || r.Claim.SizeBytes <= 0 || r.Claim.SizeBytes > 1<<30 {
			return nil, ErrRunner
		}
		key := r.DocumentHash + "/" + string(r.Claim.AssetID)
		if seen[key] {
			return nil, ErrRunner
		}
		seen[key] = true
	}
	var video *reconstructv2.MixedVideoConfig
	if config.Video != nil {
		v := config.Video
		keys := make(map[string]ed25519.PublicKey, len(v.TrustedKeys))
		for id, k := range v.TrustedKeys {
			decoded, e := base64.StdEncoding.Strict().DecodeString(k)
			if e != nil || base64.StdEncoding.EncodeToString(decoded) != k {
				return nil, ErrRunner
			}
			keys[id] = ed25519.PublicKey(decoded)
		}
		resolver, e := runtimebundle.New(runtimebundle.Options{TrustedKeys: keys})
		if e != nil {
			return nil, e
		}
		resolved, e := resolver.Resolve(ctx, v.BundleRoot)
		if e != nil {
			return nil, e
		}
		prober, e := ffprobe.New(ctx, resolved.FFprobe.Path, ffprobe.DefaultBuildIdentity(resolved.FFprobe.ClosureFingerprint), ffprobe.Options{})
		if e != nil {
			return nil, e
		}
		decoder, e := decode.New(ctx, resolved.FFmpeg.Path, decode.DefaultBuildIdentity(resolved.FFmpeg.ClosureFingerprint, v.CPUFingerprint), decode.Options{})
		if e != nil {
			return nil, e
		}
		if _, e = verifyWorkingDirectory(v.Scratch); e != nil {
			return nil, e
		}
		source.prober = prober
		video = &reconstructv2.MixedVideoConfig{Probe: prober, Decoder: decoder, MediaRuntime: config.MediaRuntime, Scratch: v.Scratch, Scan: ffprobe.VideoScanOptions{MaxFrames: decode.DefaultMaxIndexedFrames, MaxOutputBytes: 64 << 20}, MaxDiskBytes: 2 << 30, MaxScanBytes: 256 << 20, MaxIndexedFrames: uint64(decode.DefaultMaxIndexedFrames)}
	}
	authorityConfig := preparev2.Config{ProviderExecutable: providerExecutable, AllowLocalDevelopment: config.AllowLocalDevelopment, CompilerBuild: config.CompilerBuild, EvaluatorBuild: config.EvaluatorBuild, Backend: config.Backend, MediaRuntime: config.MediaRuntime, VerifyResource: source.verify}
	var authority *preparev2.Authority
	if binding == nil {
		authority, err = preparev2.New(ctx, authorityConfig)
	} else {
		authority, err = preparev2.NewWithProviderBinding(ctx, authorityConfig, *binding)
	}
	if err != nil {
		return nil, err
	}
	native := reconstructv2.DefaultMixedConfig(reconstructv2.DefaultConfig(config.Width, config.Height, authority, source))
	native.Video = video
	factory, err := reconstructv2.NewMixed(ctx, native)
	if err != nil {
		return nil, err
	}
	return MixedReconstructV2(factory), nil
}

type hostSourceV2 struct {
	resources []HostResourceV2
	prober    *ffprobe.Adapter
}

func (s *hostSourceV2) find(ctx context.Context, hash string, claim api.MaterializedResourceV2) (HostResourceV2, error) {
	if err := ctx.Err(); err != nil {
		return HostResourceV2{}, err
	}
	for _, r := range s.resources {
		if hash == r.DocumentHash && reflect.DeepEqual(claim, r.Claim) {
			return r, nil
		}
	}
	return HostResourceV2{}, ErrProtocol
}
func (s *hostSourceV2) Authorize(ctx context.Context, hash string, claim api.MaterializedResourceV2) (resourceidentityv2.Binding, error) {
	r, err := s.find(ctx, hash, claim)
	if err != nil {
		return resourceidentityv2.Binding{}, err
	}
	return resourceidentityv2.Binding{AssetID: r.Claim.AssetID, Kind: r.Claim.Kind, ContentHash: r.Claim.ContentHash, SizeBytes: r.Claim.SizeBytes}, nil
}
func (s *hostSourceV2) Open(ctx context.Context, hash string, claim api.MaterializedResourceV2, b resourceidentityv2.Binding) (io.ReadCloser, error) {
	r, err := s.find(ctx, hash, claim)
	if err != nil {
		return nil, err
	}
	expected := resourceidentityv2.Binding{AssetID: r.Claim.AssetID, Kind: r.Claim.Kind, ContentHash: r.Claim.ContentHash, SizeBytes: r.Claim.SizeBytes}
	if b != expected {
		return nil, ErrProtocol
	}
	return openPrivateRegularV2(r.Path)
}
func (s *hostSourceV2) verify(ctx context.Context, hash string, claim api.MaterializedResourceV2) (out api.MaterializedResourceV2, err error) {
	r, err := s.find(ctx, hash, claim)
	if err != nil {
		return out, err
	}
	b, err := s.Authorize(ctx, hash, claim)
	if err != nil {
		return out, err
	}
	if claim.Kind == documentv2.AssetKindVideo {
		if s.prober == nil {
			return out, ErrUnsupported
		}
		manifest, _, e := s.prober.Probe(ctx, ffprobe.Request{AssetID: videoedit.ID(b.AssetID), Path: r.Path, ExpectedContentHash: b.ContentHash})
		if e != nil {
			return out, e
		}
		fp, e := probe.Fingerprint(manifest)
		if e != nil || claim.ProbeFingerprint == nil || fp != *claim.ProbeFingerprint || manifest.Resource.SizeBytes != b.SizeBytes {
			return out, ErrProtocol
		}
		return r.Claim, nil
	}
	f, err := openPrivateRegularV2(r.Path)
	if err != nil {
		return out, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, b.SizeBytes+1))
	err = errors.Join(readErr, f.Close())
	if err != nil {
		return out, err
	}
	if int64(len(data)) != b.SizeBytes || digestV2(data) != b.ContentHash {
		return out, ErrProtocol
	}
	var observed *resourceidentityv2.Inspected
	switch b.Kind {
	case documentv2.AssetKindImage:
		observed, err = resourceidentityv2.InspectPNG(ctx, b, data, pngdecode.DefaultLimits())
	case documentv2.AssetKindFont:
		observed, err = resourceidentityv2.InspectFont(ctx, b, data, sfntglyf.DefaultLimits())
	case documentv2.AssetKindAudio:
		observed, err = resourceidentityv2.InspectAudio(ctx, b, data, wavdecode.DefaultLimits())
	default:
		return out, ErrUnsupported
	}
	if err != nil {
		return out, err
	}
	if err = resourceidentityv2.Check(ctx, b, claim, observed); err != nil {
		return out, err
	}
	return r.Claim, nil
}

// CanonicalLaunchConfigV2 is a host convenience, never accepted on request stdin.
func CanonicalLaunchConfigV2(config LaunchConfigV2) ([]byte, error) {
	raw, err := json.Marshal(config)
	if err != nil || len(raw) > MaxLaunchConfigBytesV2 {
		return nil, ErrRunner
	}
	var owned LaunchConfigV2
	if err = wirejson.Decode(raw, &owned); err != nil {
		return nil, err
	}
	return bytes.Clone(raw), nil
}
