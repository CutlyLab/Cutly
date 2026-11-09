package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/prepared"
)

const (
	RequestProfileV2  = "cutly.render.worker-request.document-v2.v2"
	ResultProfileV2   = "cutly.render.worker-result.document-v2.v2"
	RunnerProfileV2   = "cutly.render.local-process-worker.document-v2.v2"
	MaxRequestBytesV2 = 224 << 20
	// MaxPayloadBytesV2 is retained for the compatibility inline ResultV2 API.
	// Production worker transport uses an inherited descriptor and the larger,
	// independently host-admitted MaxStreamPayloadBytesV2 bound.
	MaxPayloadBytesV2             = 16 << 20
	MaxResultBytesV2              = 24 << 20
	MaxStreamPayloadBytesV2       = 8 << 30
	MaxStreamResultBytesV2        = 1 << 20
	FrameGroupCountV2       int64 = 64
	SampleBlockCountV2      int64 = 65536
)

// ChunkV2 is one exact native partition of the original plan, never a V1 task.
// Oversized partitions are rejected, not silently repartitioned.
type ChunkV2 struct {
	Domain              string              `json:"domain"`
	Range               videoedit.TickRange `json:"range"`
	IntermediateProfile string              `json:"intermediate_profile"`
}

type RequestV2 struct {
	SchemaVersion            int
	Profile                  string
	InvocationID             string
	Attempt                  uint32
	ProviderExecutableDigest string
	Environment              prepared.EnvironmentV2
	Chunk                    ChunkV2
}

// The independently canonical prepared record is transported as base64, preserving
// Document number lexemes without applying the integer-only envelope codec to it.
type requestWireV2 struct {
	SchemaVersion            int     `json:"schema_version"`
	Profile                  string  `json:"profile"`
	InvocationID             string  `json:"invocation_id"`
	Attempt                  uint32  `json:"attempt"`
	ProviderExecutableDigest string  `json:"provider_executable_digest"`
	Environment              string  `json:"environment"`
	Chunk                    ChunkV2 `json:"chunk"`
}

type ReceiptV2 struct {
	DocumentHash                 string  `json:"document_hash"`
	PlanFingerprint              string  `json:"plan_fingerprint"`
	ExecutionManifestFingerprint string  `json:"execution_manifest_fingerprint"`
	ProviderExecutableDigest     string  `json:"provider_executable_digest"`
	Chunk                        ChunkV2 `json:"chunk"`
	ContentHash                  string  `json:"content_hash"`
	SizeBytes                    int64   `json:"size_bytes"`
}

type ResultV2 struct {
	SchemaVersion int              `json:"schema_version"`
	Profile       string           `json:"profile"`
	InvocationID  string           `json:"invocation_id"`
	Attempt       uint32           `json:"attempt"`
	OK            bool             `json:"ok"`
	Receipt       *ReceiptV2       `json:"receipt,omitempty"`
	Payload       []byte           `json:"payload,omitempty"`
	FailureClass  api.FailureClass `json:"failure_class,omitempty"`
	Message       string           `json:"message,omitempty"`
}

func headerV2(version int, profile, invocation string, attempt uint32, expected string) error {
	if version != 2 || profile != expected || !idPattern.MatchString(invocation) || attempt == 0 || attempt > 16 {
		return fmt.Errorf("%w: v2 header", ErrProtocol)
	}
	return nil
}

func chunkSizeV2(r RequestV2) (int64, error) {
	p, m, c := r.Environment.Plan, r.Environment.Manifest, r.Chunk
	base, partition, profile := p.FrameRange, FrameGroupCountV2, api.VideoSurfaceProfileV2
	if m.Output.Width == 0 || m.Output.Height == 0 || m.Output.Width > 8192 || m.Output.Height > 8192 {
		return 0, ErrProtocol
	}
	unit := int64(m.Output.Width) * int64(m.Output.Height) * 8
	if c.Domain == "audio" {
		base, partition, profile, unit = p.SampleRange, SampleBlockCountV2, api.AudioBlockProfileV2, 4
	} else if c.Domain != "video" {
		return 0, ErrProtocol
	}
	if m.Output.Intermediate.VideoSurfaceProfile != api.VideoSurfaceProfileV2 || m.Output.Intermediate.AudioBlockProfile != api.AudioBlockProfileV2 || c.IntermediateProfile != profile || c.Range.Validate() != nil || c.Range.Count <= 0 || c.Range.First < base.First || base.Validate() != nil {
		return 0, fmt.Errorf("%w: v2 native chunk/profile", ErrProtocol)
	}
	offset := c.Range.First - base.First
	if offset >= base.Count || offset%partition != 0 || c.Range.Count != min(partition, base.Count-offset) || unit <= 0 || unit > MaxStreamPayloadBytesV2/c.Range.Count {
		return 0, fmt.Errorf("%w: v2 exact partition/size limit", ErrProtocol)
	}
	return unit * c.Range.Count, nil
}

func ValidateRequestV2(ctx context.Context, r RequestV2) error {
	if ctx == nil {
		return ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := headerV2(r.SchemaVersion, r.Profile, r.InvocationID, r.Attempt, RequestProfileV2); err != nil {
		return err
	}
	// Scalar foreign-provider rejection precedes document traversal or hashing.
	if !cachekey.CanonicalSHA256(r.ProviderExecutableDigest) || r.ProviderExecutableDigest != r.Environment.Manifest.ProviderBuild {
		return fmt.Errorf("%w: v2 provider binding", ErrProtocol)
	}
	if _, err := chunkSizeV2(r); err != nil {
		return err
	}
	_, err := prepared.VerifyEnvironmentV2(ctx, r.Environment)
	return err
}

func CanonicalRequestV2(ctx context.Context, r RequestV2) ([]byte, error) {
	if err := ValidateRequestV2(ctx, r); err != nil {
		return nil, err
	}
	env, err := prepared.CanonicalEnvironmentV2(ctx, r.Environment)
	if err != nil {
		return nil, err
	}
	raw, err := canonicaljson.Marshal(requestWireV2{r.SchemaVersion, r.Profile, r.InvocationID, r.Attempt, r.ProviderExecutableDigest, base64.StdEncoding.EncodeToString(env), r.Chunk})
	if err != nil || len(raw) > MaxRequestBytesV2 {
		return nil, fmt.Errorf("%w: v2 request limit/encoding", ErrProtocol)
	}
	return raw, nil
}

// DecodeRequestV2 requires an independently measured executable digest. It rejects
// a foreign header before decoding/verifying the original environment.
func DecodeRequestV2(ctx context.Context, raw []byte, measured string) (out RequestV2, err error) {
	defer func() {
		if recover() != nil {
			err = ErrProtocol
		}
		if err != nil {
			out = RequestV2{}
		}
	}()
	if ctx == nil {
		return out, ErrProtocol
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if len(raw) == 0 || len(raw) > MaxRequestBytesV2 || !cachekey.CanonicalSHA256(measured) {
		return out, ErrProtocol
	}
	canonical, e := canonicalize(raw, MaxRequestBytesV2)
	if e != nil || !bytes.Equal(canonical, raw) {
		return out, ErrProtocol
	}
	var w requestWireV2
	if err = wirejson.Decode(raw, &w); err != nil {
		return out, err
	}
	if err = headerV2(w.SchemaVersion, w.Profile, w.InvocationID, w.Attempt, RequestProfileV2); err != nil {
		return out, err
	}
	if w.ProviderExecutableDigest != measured {
		return out, fmt.Errorf("%w: v2 measured executable mismatch", ErrProtocol)
	}
	envRaw, e := base64.StdEncoding.Strict().DecodeString(w.Environment)
	if e != nil || base64.StdEncoding.EncodeToString(envRaw) != w.Environment {
		return out, ErrProtocol
	}
	env, e := prepared.DecodeEnvironmentV2(ctx, envRaw)
	if e != nil {
		return out, e
	}
	out = RequestV2{w.SchemaVersion, w.Profile, w.InvocationID, w.Attempt, w.ProviderExecutableDigest, env, w.Chunk}
	err = ValidateRequestV2(ctx, out)
	return out, err
}

func digestV2(raw []byte) string {
	h := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(h[:])
}
func receiptV2(r RequestV2, payload []byte) ReceiptV2 {
	return ReceiptV2{r.Environment.Plan.DocumentHash, r.Environment.Plan.Fingerprint, r.Environment.Manifest.Fingerprint, r.ProviderExecutableDigest, r.Chunk, digestV2(payload), int64(len(payload))}
}
func receiptMetadataV2(r RequestV2, contentHash string, size int64) ReceiptV2 {
	return ReceiptV2{r.Environment.Plan.DocumentHash, r.Environment.Plan.Fingerprint, r.Environment.Manifest.Fingerprint, r.ProviderExecutableDigest, r.Chunk, contentHash, size}
}

func validateReceiptV2(p *ReceiptV2) error {
	if p == nil {
		return ErrProtocol
	}
	for _, h := range []string{p.DocumentHash, p.PlanFingerprint, p.ExecutionManifestFingerprint, p.ProviderExecutableDigest, p.ContentHash} {
		if !cachekey.CanonicalSHA256(h) {
			return ErrProtocol
		}
	}
	if p.SizeBytes <= 0 || p.SizeBytes > MaxStreamPayloadBytesV2 || p.Chunk.Range.Validate() != nil || p.Chunk.Range.Count <= 0 {
		return ErrProtocol
	}
	if p.Chunk.Domain != "video" && p.Chunk.Domain != "audio" {
		return ErrProtocol
	}
	if p.Chunk.Domain == "video" && p.Chunk.IntermediateProfile != api.VideoSurfaceProfileV2 || p.Chunk.Domain == "audio" && p.Chunk.IntermediateProfile != api.AudioBlockProfileV2 {
		return ErrProtocol
	}
	return nil
}
func ValidateResultV2(r ResultV2) error {
	if err := headerV2(r.SchemaVersion, r.Profile, r.InvocationID, r.Attempt, ResultProfileV2); err != nil {
		return err
	}
	if !r.OK {
		if r.Receipt != nil || r.Payload != nil || !validFailureClass(r.FailureClass) || !validText(r.Message) {
			return ErrProtocol
		}
		return nil
	}
	if r.Receipt == nil || r.FailureClass != "" || r.Message != "" || len(r.Payload) == 0 || len(r.Payload) > MaxPayloadBytesV2 {
		return ErrProtocol
	}
	p := r.Receipt
	if validateReceiptV2(p) != nil || p.SizeBytes != int64(len(r.Payload)) || p.ContentHash != digestV2(r.Payload) {
		return ErrProtocol
	}
	return nil
}

const streamPayloadTransportV2 = "fd5"

type streamResultWireV2 struct {
	SchemaVersion    int              `json:"schema_version"`
	Profile          string           `json:"profile"`
	InvocationID     string           `json:"invocation_id"`
	Attempt          uint32           `json:"attempt"`
	OK               bool             `json:"ok"`
	Receipt          *ReceiptV2       `json:"receipt,omitempty"`
	PayloadTransport string           `json:"payload_transport,omitempty"`
	FailureClass     api.FailureClass `json:"failure_class,omitempty"`
	Message          string           `json:"message,omitempty"`
}

func validateStreamResultWireV2(r streamResultWireV2) error {
	if err := headerV2(r.SchemaVersion, r.Profile, r.InvocationID, r.Attempt, ResultProfileV2); err != nil {
		return err
	}
	if !r.OK {
		if r.Receipt != nil || r.PayloadTransport != "" || !validFailureClass(r.FailureClass) || !validText(r.Message) {
			return ErrProtocol
		}
		return nil
	}
	if r.PayloadTransport != streamPayloadTransportV2 || r.FailureClass != "" || r.Message != "" || validateReceiptV2(r.Receipt) != nil {
		return ErrProtocol
	}
	return nil
}

func canonicalStreamResultV2(r streamResultWireV2) ([]byte, error) {
	if err := validateStreamResultWireV2(r); err != nil {
		return nil, err
	}
	raw, err := canonicaljson.Marshal(r)
	if err != nil || len(raw) > MaxStreamResultBytesV2 {
		return nil, ErrProtocol
	}
	return raw, nil
}

func decodeStreamResultV2(raw []byte) (streamResultWireV2, error) {
	if len(raw) == 0 || len(raw) > MaxStreamResultBytesV2 {
		return streamResultWireV2{}, ErrProtocol
	}
	canonical, err := canonicalize(raw, MaxStreamResultBytesV2)
	if err != nil || !bytes.Equal(raw, canonical) {
		return streamResultWireV2{}, ErrProtocol
	}
	var out streamResultWireV2
	if err = wirejson.Decode(raw, &out); err != nil {
		return streamResultWireV2{}, err
	}
	if err = validateStreamResultWireV2(out); err != nil {
		return streamResultWireV2{}, err
	}
	return out, nil
}

type resultWireV2 struct {
	SchemaVersion int              `json:"schema_version"`
	Profile       string           `json:"profile"`
	InvocationID  string           `json:"invocation_id"`
	Attempt       uint32           `json:"attempt"`
	OK            bool             `json:"ok"`
	Receipt       *ReceiptV2       `json:"receipt,omitempty"`
	Payload       string           `json:"payload,omitempty"`
	FailureClass  api.FailureClass `json:"failure_class,omitempty"`
	Message       string           `json:"message,omitempty"`
}

func CanonicalResultV2(r ResultV2) ([]byte, error) {
	if err := ValidateResultV2(r); err != nil {
		return nil, err
	}
	raw, err := canonicaljson.Marshal(resultWireV2{r.SchemaVersion, r.Profile, r.InvocationID, r.Attempt, r.OK, r.Receipt, base64.StdEncoding.EncodeToString(r.Payload), r.FailureClass, r.Message})
	if err != nil || len(raw) > MaxResultBytesV2 {
		return nil, ErrProtocol
	}
	return raw, nil
}
func DecodeResultV2(raw []byte) (ResultV2, error) {
	if len(raw) == 0 || len(raw) > MaxResultBytesV2 {
		return ResultV2{}, ErrProtocol
	}
	canonical, err := canonicalize(raw, MaxResultBytesV2)
	if err != nil || !bytes.Equal(raw, canonical) {
		return ResultV2{}, ErrProtocol
	}
	var wire resultWireV2
	if err = wirejson.Decode(raw, &wire); err != nil {
		return ResultV2{}, err
	}
	var payload []byte
	if wire.Payload != "" {
		payload, err = base64.StdEncoding.Strict().DecodeString(wire.Payload)
		if err != nil || base64.StdEncoding.EncodeToString(payload) != wire.Payload {
			return ResultV2{}, ErrProtocol
		}
	}
	out := ResultV2{wire.SchemaVersion, wire.Profile, wire.InvocationID, wire.Attempt, wire.OK, wire.Receipt, payload, wire.FailureClass, wire.Message}
	if err = ValidateResultV2(out); err != nil {
		return ResultV2{}, err
	}
	return out, nil
}

// EqualReceiptV2 compares scalar native identities, not unsigned provenance.
func EqualReceiptV2(a, b ReceiptV2) bool { return a == b }
