package wavsource

import (
	"context"
	"fmt"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/media/wavdecode"
)

// Inspect produces the same native ProbeManifest used by Compile without a
// synthetic Document or live Provider. The expected content hash and size must
// come from an independently authorized resource binding. Bytes are borrowed
// only for this call and must remain immutable until it returns. No filesystem
// access, resource authorization or FFprobe execution occurs here.
//
// Identity and encoded-byte admission precede WAV decoding. Limits bound the
// decoder's allocations; caller-owned encoded bytes are additional. A successful
// observation does not retain the bytes for later reconstruction.
func Inspect(ctx context.Context, assetID documentv2.ID, expectedContentHash string, expectedSizeBytes int64, encoded []byte, limits wavdecode.Limits) (result probe.ProbeManifest, err error) {
	defer func() {
		if recover() != nil {
			result = probe.ProbeManifest{}
			err = fmt.Errorf("%w: inspection panic", ErrDecodeFailed)
		}
	}()
	if ctx == nil {
		return result, ErrContextInvalid
	}
	if err = contextFailure(ctx, "inspect"); err != nil {
		return result, err
	}
	maximum := limits.MaxEncodedBytes
	if maximum == 0 {
		maximum = wavdecode.DefaultMaxEncodedBytes
	}
	if maximum > wavdecode.HardMaxEncodedBytes || uint64(len(encoded)) > maximum {
		return result, ErrResourceLimit
	}
	if expectedSizeBytes <= 0 || int64(len(encoded)) != expectedSizeBytes || assetID == "" || len(assetID) > 4096 {
		return result, ErrBindingInvalid
	}
	if !validInspectionDigest(expectedContentHash) {
		return result, ErrBindingInvalid
	}
	observed, err := hashBytes(ctx, encoded, "inspect/content_hash")
	if err != nil {
		return result, err
	}
	if observed != expectedContentHash {
		return result, ErrContentMismatch
	}
	decoded, err := wavdecode.Decode(ctx, encoded, limits)
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	if decoded.EncodedContentHash != observed {
		return result, ErrContentMismatch
	}
	build, options, err := probeFingerprints()
	if err != nil {
		return result, err
	}
	manifest, err := makeProbeManifest(assetID, len(encoded), decoded, build, options)
	if err != nil {
		return result, err
	}
	if err = contextFailure(ctx, "inspect/publish"); err != nil {
		return result, err
	}
	return manifest, nil
}

func validInspectionDigest(value string) bool {
	if len(value) != 71 || value[:7] != "sha256:" {
		return false
	}
	for _, c := range value[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
