package reconstructv2

import (
	"context"
	"errors"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	videodecode "github.com/CutlyLab/Cutly/media/decode"
	"github.com/CutlyLab/Cutly/media/ffprobe"
	"github.com/CutlyLab/Cutly/media/probe"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// videoResourceConfig is host policy, never decoded from an environment. The
// caller must verify these runtime pins against the authorized environment and
// reserve disk for both the transfer snapshot and the decoder's retained copy.
type videoResourceConfig struct {
	probe        *ffprobe.Adapter
	decoder      *videodecode.Adapter
	scan         ffprobe.VideoScanOptions
	maximumBytes uint64
	scratch      string
}

type openedVideoResource struct {
	source   *videodecode.Source
	manifest probe.ProbeManifest
	scan     ffprobe.VideoScanResult
}

// openVideoResource re-probes the actual privately retained bytes before complete
// scanning and decoding. This profile requires exactly one video stream; it
// never guesses a stream from average frame rate or silently chooses another
// stream when an ambiguous container is encountered. It grants no authority by
// itself: aggregate admission and environment authorization precede this call.
func openVideoResource(ctx context.Context, resources ResourceSource, documentHash string, claim renderapi.MaterializedResourceV2, config videoResourceConfig) (result openedVideoResource, err error) {
	var snapshot *diskSnapshot
	var source *videodecode.Source
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if snapshot != nil {
			err = errors.Join(err, snapshot.Close())
		}
		if err != nil {
			if source != nil {
				err = errors.Join(err, source.Close())
			}
			result = openedVideoResource{}
		}
	}()
	if ctx == nil {
		return result, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if config.probe == nil || config.decoder == nil || config.decoder.Fingerprint() == "" || config.scan.MaxFrames <= 0 || config.scan.MaxOutputBytes <= 0 {
		return result, ErrConfig
	}
	if claim.Kind != documentv2.AssetKindVideo || claim.MaterializationProfile != renderapi.AVMaterializationProfileV2 || claim.ProbeFingerprint == nil || !bridgeSHA(*claim.ProbeFingerprint) || claim.InspectionFingerprint != nil {
		return result, ErrResource
	}
	claim = ownClaim(claim)
	binding, snapshot, err := snapshotResourceFile(ctx, resources, documentHash, claim, config.maximumBytes, config.scratch)
	if err != nil {
		return result, err
	}
	request := ffprobe.Request{AssetID: videoedit.ID(binding.AssetID), Path: snapshot.path, ExpectedContentHash: binding.ContentHash}
	manifest, _, err := config.probe.Probe(ctx, request)
	if err != nil {
		return result, err
	}
	fingerprint, err := probe.Fingerprint(manifest)
	if err != nil || manifest.Probe != config.probe.Identity() || fingerprint != *claim.ProbeFingerprint || manifest.Resource.ContentHash != binding.ContentHash || manifest.Resource.SizeBytes != binding.SizeBytes || manifest.Resource.AssetID != videoedit.ID(binding.AssetID) {
		return result, errors.Join(ErrResource, err)
	}
	streamIndex := -1
	for _, stream := range manifest.Streams {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if stream.Kind == probe.StreamKindVideo {
			if streamIndex != -1 {
				return result, ErrUnsupported
			}
			streamIndex = stream.Index
		}
	}
	if streamIndex < 0 {
		return result, ErrUnsupported
	}
	scan, err := config.probe.ScanVideo(ctx, request, manifest, streamIndex, config.scan)
	if err != nil {
		return result, err
	}
	source, err = config.decoder.Open(ctx, videodecode.OpenRequest{Path: snapshot.path, Manifest: manifest, Index: scan.Index, IndexFingerprint: scan.IndexFingerprint, Semantics: scan.Semantics, SemanticsFingerprint: scan.SemanticsFingerprint})
	if err != nil {
		return result, err
	}
	identity, err := source.Identity()
	if err != nil || identity.AdapterFingerprint != config.decoder.Fingerprint() || identity.ProbeFingerprint != fingerprint || identity.ResourceHash != claim.ContentHash || identity.StreamIndex != streamIndex || identity.IndexFingerprint != scan.IndexFingerprint || identity.SemanticsFingerprint != scan.SemanticsFingerprint {
		return result, errors.Join(ErrResource, err)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	return openedVideoResource{source: source, manifest: manifest, scan: scan}, nil
}
