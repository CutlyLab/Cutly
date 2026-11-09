package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	agentprotocol "github.com/CutlyLab/Cutly/agent/protocol"
	"github.com/CutlyLab/Cutly/agent/router"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/media/color"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/pngencode"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/worker"
)

const nativeFramePreviewProfileV2 = "cutly.preview-frame.document-v2.v2"

type nativeFramePreviewV2 struct {
	projectID          string
	outputRoot         string
	providerExecutable string
	host               worker.LaunchConfigV2
	environments       *prepared.Store
	runner             *worker.RunnerV2
}

type nativeFramePreviewPayloadV2 struct {
	Profile                      string `json:"profile"`
	PlanFingerprint              string `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
	CompositionFrameIndex        int64  `json:"composition_frame_index"`
}

func (preview *nativeFramePreviewV2) Route() router.Route {
	return router.Route{Tool: agentprotocol.ToolRender, Action: "frame", Handler: router.ActionFunc(preview.execute)}
}

func (preview *nativeFramePreviewV2) execute(ctx context.Context, request agentprotocol.Request) (out router.Outcome, returnErr error) {
	if preview == nil || preview.environments == nil || preview.runner == nil {
		return router.Outcome{}, errors.New("cutlyd: native frame preview is uninitialized")
	}
	if request.ProjectID != preview.projectID {
		return nativePreviewRejectV2("RENDER.V2.PROJECT_MISMATCH", "/project_id", "project is outside the configured native runtime"), nil
	}
	var payload nativeFramePreviewPayloadV2
	if err := wirejson.Decode(request.Payload, &payload); err != nil || payload.Profile != nativeFramePreviewProfileV2 || payload.PlanFingerprint == "" || payload.ExecutionManifestFingerprint == "" {
		return nativePreviewRejectV2("RENDER.V2.PAYLOAD_INVALID", "/payload", "a bound Document v2 frame preview request is required"), nil
	}
	environment, err := preview.environments.ResolveV2(ctx, payload.PlanFingerprint, payload.ExecutionManifestFingerprint)
	if err != nil {
		return out, err
	}
	verified, err := prepared.VerifyEnvironmentV2(ctx, environment)
	if err != nil {
		return out, err
	}
	environment, err = verified.Environment(ctx)
	if err != nil {
		return out, err
	}
	if err := worker.AuthorizeLaunchEnvironmentV2(ctx, preview.host, preview.providerExecutable, environment); err != nil {
		return out, err
	}
	contains, err := environment.Plan.FrameRange.Contains(payload.CompositionFrameIndex)
	if err != nil || !contains {
		return nativePreviewRejectV2("RENDER.V2.FRAME_OUT_OF_RANGE", "/payload/composition_frame_index", "frame is outside the prepared plan"), nil
	}
	base := environment.Plan.FrameRange
	groupFirst := base.First + ((payload.CompositionFrameIndex-base.First)/worker.FrameGroupCountV2)*worker.FrameGroupCountV2
	groupCount := worker.FrameGroupCountV2
	if remaining := base.First + base.Count - groupFirst; remaining < groupCount {
		groupCount = remaining
	}
	invocation, err := nativePreviewInvocationV2()
	if err != nil {
		return out, err
	}
	stream, _, err := preview.runner.RunStream(ctx, worker.RequestV2{
		SchemaVersion:            2,
		Profile:                  worker.RequestProfileV2,
		InvocationID:             invocation,
		Attempt:                  1,
		ProviderExecutableDigest: preview.runner.ExecutableDigest(),
		Environment:              environment,
		Chunk: worker.ChunkV2{
			Domain:              "video",
			Range:               videoedit.TickRange{First: groupFirst, Count: groupCount},
			IntermediateProfile: "cutly.surface.linear-bt709-premultiplied-rgba16.v1",
		},
	})
	if err != nil {
		return out, err
	}
	if stream == nil {
		return out, errors.New("cutlyd: native frame worker returned no result")
	}
	defer func() { returnErr = errors.Join(returnErr, stream.Close()) }()
	if !stream.OK || stream.Payload == nil {
		return out, fmt.Errorf("cutlyd: native frame worker rejected preview: %s", stream.Message)
	}
	frameBytes, err := nativePreviewFrameBytesV2(stream.Payload, environment, groupFirst, payload.CompositionFrameIndex)
	if err != nil {
		return out, err
	}
	surface, err := nativePreviewSurfaceV2(environment, frameBytes)
	if err != nil {
		return out, err
	}
	converted, err := color.ConvertSurfaceToRGBA8(ctx, surface, color.DefaultOutputLimits())
	if err != nil {
		return out, err
	}
	encoded, err := pngencode.Encode(ctx, converted, pngencode.DefaultLimits())
	if err != nil {
		return out, err
	}
	artifactURI, err := publishNativePreviewV2(preview.outputRoot, encoded.Artifact, encoded.ArtifactHash)
	if err != nil {
		return out, err
	}
	receipt, err := canonicaljson.Marshal(struct {
		Profile                      string `json:"profile"`
		PlanFingerprint              string `json:"plan_fingerprint"`
		ExecutionManifestFingerprint string `json:"execution_manifest_fingerprint"`
		CompositionFrameIndex        int64  `json:"composition_frame_index"`
		ContentHash                  string `json:"content_hash"`
		URI                          string `json:"uri"`
	}{nativeFramePreviewProfileV2, environment.Plan.Fingerprint, environment.Manifest.Fingerprint, payload.CompositionFrameIndex, encoded.ArtifactHash, artifactURI})
	if err != nil {
		return out, err
	}
	return router.Outcome{OK: true, State: "frame-ready-native-v2", Receipt: receipt, Diagnostics: []videoedit.Diagnostic{}}, nil
}

func nativePreviewRejectV2(code, path, message string) router.Outcome {
	return router.Outcome{State: "rejected", Receipt: json.RawMessage(`{}`), Diagnostics: []videoedit.Diagnostic{{Code: code, Severity: documentv2.SeverityError, Path: path, Message: message}}}
}

func nativePreviewInvocationV2() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", err
	}
	return "preview-" + hex.EncodeToString(value[:]), nil
}

func nativePreviewFrameBytesV2(payload io.ReadSeeker, environment prepared.EnvironmentV2, groupFirst, frameIndex int64) ([]byte, error) {
	width, height := int64(environment.Manifest.Output.Width), int64(environment.Manifest.Output.Height)
	if width <= 0 || height <= 0 || width > (1<<62)/height || width*height > (1<<62)/8 {
		return nil, errors.New("cutlyd: native frame dimensions overflow")
	}
	frameSize := width * height * 8
	maximumInt := int64(^uint(0) >> 1)
	if frameSize <= 0 || frameSize > maximumInt {
		return nil, errors.New("cutlyd: native frame exceeds platform allocation")
	}
	offset := frameIndex - groupFirst
	if offset < 0 || offset > (1<<63-1)/frameSize {
		return nil, errors.New("cutlyd: native frame offset overflow")
	}
	if _, err := payload.Seek(offset*frameSize, io.SeekStart); err != nil {
		return nil, err
	}
	result := make([]byte, int(frameSize))
	if _, err := io.ReadFull(payload, result); err != nil {
		return nil, err
	}
	return result, nil
}

func nativePreviewSurfaceV2(environment prepared.EnvironmentV2, raw []byte) (*cpucompositor.Surface, error) {
	width, height := int(environment.Manifest.Output.Width), int(environment.Manifest.Output.Height)
	if width <= 0 || height <= 0 || width > int(^uint(0)>>1)/height || len(raw) != width*height*8 {
		return nil, errors.New("cutlyd: native frame geometry differs from manifest")
	}
	pixels := make([]cpucompositor.Pixel, width*height)
	for index := range pixels {
		offset := index * 8
		pixels[index] = cpucompositor.Pixel{
			R: binary.BigEndian.Uint16(raw[offset : offset+2]),
			G: binary.BigEndian.Uint16(raw[offset+2 : offset+4]),
			B: binary.BigEndian.Uint16(raw[offset+4 : offset+6]),
			A: binary.BigEndian.Uint16(raw[offset+6 : offset+8]),
		}
	}
	return cpucompositor.NewSurfaceFromPixelsWithLimits(width, height, pixels, cpucompositor.Limits{MaxWidth: width, MaxHeight: height, MaxBytes: uint64(len(raw))})
}

func publishNativePreviewV2(outputRoot string, artifact *pngencode.Artifact, contentHash string) (result string, returnErr error) {
	if artifact == nil || !strings.HasPrefix(contentHash, "sha256:") || len(contentHash) != len("sha256:")+64 {
		return "", errors.New("cutlyd: invalid native preview artifact")
	}
	previewRoot := filepath.Join(outputRoot, ".cutly-preview-v2")
	if err := os.Mkdir(previewRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(previewRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.Join(errors.New("cutlyd: preview root is not a real directory"), err)
	}
	final := filepath.Join(previewRoot, strings.TrimPrefix(contentHash, "sha256:")+".png")
	temporary, err := os.CreateTemp(previewRoot, ".cutly-preview-")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	temporaryOpen := true
	defer func() {
		if temporaryOpen {
			returnErr = errors.Join(returnErr, temporary.Close())
		}
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, removeErr)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := artifact.WriteTo(temporary); err != nil {
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	temporaryOpen = false
	if err := os.Link(temporaryPath, final); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if err := verifyNativePreviewFileV2(final, contentHash, int64(artifact.Size())); err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "file", Path: final}).String(), nil
}

func verifyNativePreviewFileV2(path, contentHash string, size int64) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != size {
		return errors.Join(errors.New("cutlyd: invalid published preview artifact"), err)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	if "sha256:"+hex.EncodeToString(digest.Sum(nil)) != contentHash {
		return errors.New("cutlyd: published preview artifact hash differs")
	}
	return nil
}
