// Package reconstructv2 contains host-owned reconstruction boundaries. Resource
// sources are trusted host configuration, never deserialized request authority.
package reconstructv2

import (
	"context"
	"errors"
	"io"

	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
)

var (
	ErrResource = errors.New("reconstructv2: resource snapshot failed")
	ErrLimit    = errors.New("reconstructv2: resource admission exceeded")
	ErrContext  = errors.New("reconstructv2: nil context")
	ErrPanic    = errors.New("reconstructv2: resource operation panicked")
)

// ResourceSource is installed by trusted host code. Authorize must independently
// authorize the original Document and locator and return the expected identity;
// copying the claim without consulting host policy does not satisfy this contract.
// Open must confine the locator to authorized roots and honor cancellation. Both
// methods must be concurrency-safe. Readers must support cancellation themselves:
// the caller cannot interrupt a blocked synchronous Read or Close.
//
// The reconstruction factory must finish scope, host-pin and aggregate admission
// checks BEFORE calling this interface. No locator policy is provided by a receipt.
// Returned readers are always closed, including a non-nil reader returned with an
// error. The private snapshot is subsequently hashed/inspected before execution.
type ResourceSource interface {
	Authorize(context.Context, string, renderapi.MaterializedResourceV2) (resourceidentityv2.Binding, error)
	Open(context.Context, string, renderapi.MaterializedResourceV2, resourceidentityv2.Binding) (io.ReadCloser, error)
}

func ownClaim(value renderapi.MaterializedResourceV2) renderapi.MaterializedResourceV2 {
	if value.InspectionFingerprint != nil {
		x := *value.InspectionFingerprint
		value.InspectionFingerprint = &x
	}
	if value.ProbeFingerprint != nil {
		x := *value.ProbeFingerprint
		value.ProbeFingerprint = &x
	}
	return value
}

// snapshotResource is called only after joint environment verification and no-IO
// preflight. Its output is a private byte snapshot, NOT an actual inspection.
func snapshotResource(ctx context.Context, source ResourceSource, documentHash string, claim renderapi.MaterializedResourceV2, maximum uint64) (binding resourceidentityv2.Binding, encoded []byte, err error) {
	var reader io.ReadCloser
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if reader != nil {
			err = errors.Join(err, closeResource(reader))
		}
		if err != nil {
			binding, encoded = resourceidentityv2.Binding{}, nil
		}
	}()
	if ctx == nil {
		return binding, nil, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return binding, nil, err
	}
	if source == nil || documentHash == "" {
		return binding, nil, ErrResource
	}
	if claim.SizeBytes <= 0 || uint64(claim.SizeBytes) > maximum || uint64(claim.SizeBytes) > uint64(^uint(0)>>1) {
		return binding, nil, ErrLimit
	}
	claim = ownClaim(claim)
	expected := resourceidentityv2.Binding{AssetID: claim.AssetID, Kind: claim.Kind, ContentHash: claim.ContentHash, SizeBytes: claim.SizeBytes}
	binding, err = source.Authorize(ctx, documentHash, ownClaim(claim))
	if err != nil {
		return binding, nil, err
	}
	if err = ctx.Err(); err != nil {
		return binding, nil, err
	}
	if binding != expected {
		return binding, nil, ErrResource
	}
	reader, err = source.Open(ctx, documentHash, ownClaim(claim), binding)
	if err != nil {
		return binding, nil, err
	}
	if reader == nil {
		return binding, nil, ErrResource
	}
	if err = ctx.Err(); err != nil {
		return binding, nil, err
	}
	encoded = make([]byte, int(claim.SizeBytes))
	for offset := 0; offset < len(encoded); {
		if err = ctx.Err(); err != nil {
			return binding, nil, err
		}
		end := offset + min(32<<10, len(encoded)-offset)
		n, readErr := reader.Read(encoded[offset:end])
		if n < 0 || n > end-offset {
			return binding, nil, ErrResource
		}
		offset += n
		if readErr != nil {
			if readErr == io.EOF && offset == len(encoded) {
				break
			}
			return binding, nil, errors.Join(ErrResource, readErr)
		}
		if n == 0 {
			return binding, nil, io.ErrNoProgress
		}
	}
	if err = ctx.Err(); err != nil {
		return binding, nil, err
	}
	var extra [1]byte
	n, readErr := reader.Read(extra[:])
	if n != 0 || readErr != io.EOF {
		return binding, nil, ErrResource
	}
	if err = ctx.Err(); err != nil {
		return binding, nil, err
	}
	// Close before publication, then check cancellation caused by Close as well.
	closing := reader
	reader = nil
	if err = closeResource(closing); err != nil {
		return binding, nil, err
	}
	if err = ctx.Err(); err != nil {
		return binding, nil, err
	}
	return binding, encoded, nil
}

func closeResource(reader io.ReadCloser) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
	}()
	return reader.Close()
}
