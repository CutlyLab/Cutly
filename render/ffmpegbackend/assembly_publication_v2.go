package ffmpegbackend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"

	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/evidence"
)

// AssembleChunks consumes only verified ordered CAS descriptors. All bytes are
// verified before encoding. Evidence is made durable first; the no-replace MP4
// link is the logical pair commit marker, NOT a two-file atomic filesystem op.
func (b *ChunkAssemblerV2) AssembleChunks(ctx context.Context, v evidence.VerifiedBindingV2, source ChunkSourceV2, sink api.EventSink) (artifact api.RenderArtifact, err error) {
	defer func() {
		if x := recover(); x != nil {
			err = errors.Join(err, fmt.Errorf("%w: assembly panic: %v", ErrCommand, x))
		}
		if err != nil {
			artifact = api.RenderArtifact{}
		}
	}()
	r, err := b.admit(ctx, v)
	if err != nil {
		return artifact, err
	}
	unlock, err := b.lockV2(ctx)
	if err != nil {
		return artifact, err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err = b.recoverJournalV2(ctx, v, r); err != nil {
		return artifact, err
	}
	if source == nil {
		return artifact, ErrPreflight
	}
	// A complete valid prior pair is reusable. Incomplete or foreign publications
	// remain integrity errors and are never deleted as speculative crash cleanup.
	if existing, found, e := b.inspectChunkAssemblyV2(ctx, v); e != nil {
		return artifact, e
	} else if found {
		return existing, nil
	}
	emitter := progressEmitter{sink: sink, jobID: r.JobID}
	if err = emitter.emit(ctx, api.StageEncode, api.JobRunning, nil); err != nil {
		return artifact, err
	}
	// Audio then video is the frozen global descriptor order.
	audio, err := b.stageDomainV2(ctx, r.Audio, source)
	if err != nil {
		return artifact, err
	}
	if audio != nil {
		defer func() { err = errors.Join(err, audio.Close()) }()
	}
	raw, err := b.stageDomainV2(ctx, r.Video, source)
	if err != nil {
		return artifact, err
	}
	if raw != nil {
		defer func() { err = errors.Join(err, raw.Close()) }()
	}
	video, err := b.rgba8V2(ctx, raw, r)
	if err != nil {
		return artifact, err
	}
	if video != nil {
		defer func() { err = errors.Join(err, video.Close()) }()
	}
	// Bound staging includes raw inputs plus converted RGBA8. Admission counts
	// original RGBA16 twice conservatively, so no whole-video memory is allocated.
	j, err := b.newJournalV2(ctx, r)
	if err != nil {
		return artifact, err
	}
	media, mn, side, sn := j.media, j.record.Media.Name, j.side, j.record.Evidence.Name
	defer func() { err = errors.Join(err, j.close()) }()
	defer func() {
		cleanup := errors.Join(b.removeOwnedV2(sn, side), b.removeOwnedV2(mn, media))
		if cleanup == nil {
			cleanup = b.finishJournalV2(context.Background(), j)
		}
		err = errors.Join(err, cleanup)
	}()
	b.checkpointV2("journal-durable")
	// A surviving orphan encoder after an abrupt daemon death can only write
	// this detached inode, never the journal-owned publication inode that restart
	// examines. Parent-death process reaping is a separate supervisor boundary.
	encoded, err := b.anonymousV2()
	if err != nil {
		return artifact, err
	}
	defer func() { err = errors.Join(err, encoded.Close()) }()
	if err = b.encodeV2(ctx, r, video, audio, encoded); err != nil {
		return artifact, err
	}
	encodedHash, encodedSize, err := hashOpenFile(ctx, encoded)
	if err != nil || encodedSize <= 0 {
		return artifact, errors.Join(err, ErrCommand)
	}
	copiedHash, copiedSize, err := copyPublishedSnapshot(ctx, media, encoded, encodedSize)
	if err != nil {
		return artifact, err
	}
	if copiedHash != encodedHash || copiedSize != encodedSize {
		return artifact, ErrContent
	}
	if err = media.Sync(); err != nil {
		return artifact, err
	}
	b.checkpointV2("encoded")
	if err = b.io.verifyRuntime(ctx); err != nil {
		return artifact, err
	}
	hash, size, err := hashOpenFile(ctx, media)
	if err != nil || size <= 0 {
		return artifact, errors.Join(err, fmt.Errorf("%w: empty encoded V2 artifact", ErrCommand))
	}
	decodedVideo, decodedAudio, err := b.decodedV2(ctx, media, r)
	if err != nil {
		return artifact, err
	}
	manifest, err := evidence.NewManifestV2(ctx, r, b.workerDigest, hash, size, decodedVideo, decodedAudio)
	if err != nil {
		return artifact, err
	}
	if _, err = evidence.VerifyManifestV2AgainstBinding(ctx, manifest, r); err != nil {
		return artifact, err
	}
	after, n, err := hashOpenFile(ctx, media)
	if err != nil {
		return artifact, err
	}
	if after != hash || n != size {
		return artifact, ErrContent
	}
	data, err := evidence.CanonicalManifestV2(manifest)
	if err != nil {
		return artifact, err
	}
	if int64(len(data)) > MaxPublishedEvidenceBytes {
		return artifact, ErrPublication
	}
	if err = writePublishedSnapshot(side, data); err != nil {
		return artifact, err
	}
	if err = side.Sync(); err != nil {
		return artifact, err
	}
	artifact, err = artifactV2(r, manifest)
	if err != nil {
		return artifact, err
	}
	if err = emitter.emit(ctx, api.StagePublish, api.JobRunning, nil); err != nil {
		return artifact, err
	}
	if err = ctx.Err(); err != nil {
		return artifact, err
	}
	if err = b.io.verifyOutputPathIdentity(); err != nil {
		return artifact, err
	}
	target := filepath.Base(mustArtifactPathV2(b, r.ArtifactURI))
	sideTarget := target + ".evidence.json"
	sideOwned, mediaOwned, committed := false, false, false
	defer func() {
		if !committed {
			if mediaOwned {
				err = errors.Join(err, b.removeOwnedV2(target, media))
			}
			if sideOwned {
				err = errors.Join(err, b.removeOwnedV2(sideTarget, side))
			}
			if sideOwned || mediaOwned {
				err = errors.Join(err, b.io.syncPublicationDirectory())
			}
		}
	}()
	// Set ownership immediately after each successful link, before callbacks that
	// can panic. A panic after evidence publication still rolls back owned leaves.
	if err = linkOpenFileNoReplace(b.io.outputHandle, sn, sideTarget, side); err != nil {
		return artifact, err
	}
	sideOwned = true
	b.checkpointV2("evidence-linked")
	if err = b.io.syncPublicationDirectory(); err != nil {
		return artifact, err
	}
	if err = linkOpenFileNoReplace(b.io.outputHandle, mn, target, media); err != nil {
		return artifact, err
	}
	mediaOwned = true
	b.checkpointV2("media-linked")
	if err = b.io.verifyPublishedName(target, media); err != nil {
		return artifact, err
	}
	if err = b.io.verifyPublishedName(sideTarget, side); err != nil {
		return artifact, err
	}
	if err = b.removeOwnedV2(sn, side); err != nil {
		return artifact, err
	}
	if err = b.removeOwnedV2(mn, media); err != nil {
		return artifact, err
	}
	if err = b.io.syncPublicationDirectory(); err != nil {
		return artifact, err
	}
	if err = emitter.emit(ctx, api.StagePublish, api.JobComplete, nil); err != nil {
		return artifact, err
	}
	b.checkpointV2("pair-synced")
	// A host callback can mutate names even when it reports success. Revalidate
	// retained publication identities after the last callback before returning.
	if err = b.io.verifyPublishedName(target, media); err != nil {
		return artifact, err
	}
	if err = b.io.verifyPublishedName(sideTarget, side); err != nil {
		return artifact, err
	}
	finalHash, finalSize, finalErr := hashOpenFile(ctx, media)
	if finalErr != nil {
		return artifact, finalErr
	}
	if finalHash != hash || finalSize != size {
		return artifact, ErrContent
	}
	finalSide, finalErr := readBoundedPublished(ctx, side, MaxPublishedEvidenceBytes)
	if finalErr != nil {
		return artifact, finalErr
	}
	if !bytes.Equal(finalSide, data) {
		return artifact, ErrContent
	}
	if err = b.io.verifyOutputPathIdentity(); err != nil {
		return artifact, err
	}
	committed = true
	return artifact, nil
}
func mustArtifactPathV2(b *ChunkAssemblerV2, uri string) string {
	p, _ := b.io.resolveArtifactPath(uri)
	return p
}

func (b *ChunkAssemblerV2) inspectChunkAssemblyV2(ctx context.Context, v evidence.VerifiedBindingV2) (a api.RenderArtifact, found bool, err error) {
	media, found, err := b.openChunkAssemblyV2(ctx, v)
	if err != nil || !found {
		return a, found, err
	}
	a = media.Artifact()
	err = media.Close()
	if err != nil {
		return api.RenderArtifact{}, false, err
	}
	return a, true, nil
}

// OpenChunkAssembly re-verifies identity and full bytes and returns an anonymous
// snapshot. Recovery never removes incomplete/foreign leaves or guesses ownership.
func (b *ChunkAssemblerV2) openChunkAssemblyV2(ctx context.Context, v evidence.VerifiedBindingV2) (out *PublishedMedia, found bool, err error) {
	defer func() {
		if x := recover(); x != nil {
			err = errors.Join(err, fmt.Errorf("%w: recovery panic: %v", ErrPublication, x))
		}
		if err != nil {
			out = nil
			found = false
		}
	}()
	r, err := b.admit(ctx, v)
	if err != nil {
		return nil, false, err
	}
	target := filepath.Base(mustArtifactPathV2(b, r.ArtifactURI))
	sn := target + ".evidence.json"
	haveMedia, me := b.io.publishedLeafExists(target)
	haveSide, se := b.io.publishedLeafExists(sn)
	if me != nil || se != nil {
		return nil, false, errors.Join(me, se)
	}
	if !haveMedia && !haveSide {
		return nil, false, nil
	}
	if !haveMedia || !haveSide {
		return nil, false, fmt.Errorf("%w: incomplete V2 logical pair", ErrPublication)
	}
	media, err := b.io.openPublishedRegular(target, "media")
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, media.Close()) }()
	side, err := b.io.openPublishedRegular(sn, "evidence")
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, side.Close()) }()
	data, err := readBoundedPublished(ctx, side, MaxPublishedEvidenceBytes)
	if err != nil {
		return nil, false, err
	}
	m, err := evidence.DecodeManifestV2(data)
	if err != nil {
		return nil, false, err
	}
	if _, err = evidence.VerifyManifestV2AgainstBinding(ctx, m, r); err != nil {
		return nil, false, err
	}
	if m.WorkerExecutableDigest != b.workerDigest {
		return nil, false, fmt.Errorf("%w: worker identity", ErrPublication)
	}
	snapshot, err := b.anonymousV2()
	if err != nil {
		return nil, false, err
	}
	owned := true
	defer func() {
		if owned {
			err = errors.Join(err, snapshot.Close())
		}
	}()
	hash, size, err := copyPublishedSnapshot(ctx, snapshot, media, m.Artifact.SizeBytes)
	if err != nil {
		return nil, false, err
	}
	if hash != m.Artifact.ContentHash || size != m.Artifact.SizeBytes || m.Artifact.URI != r.ArtifactURI {
		return nil, false, ErrContent
	}
	// Recompute final roots from authenticated snapshot: a self-consistent forged
	// evidence fingerprint cannot substitute made-up decoded roots during restart.
	dv, da, err := b.decodedV2(ctx, snapshot, r)
	if err != nil {
		return nil, false, err
	}
	actual, err := evidence.NewManifestV2(ctx, r, b.workerDigest, hash, size, dv, da)
	if err != nil {
		return nil, false, err
	}
	if actual.Fingerprint != m.Fingerprint {
		return nil, false, fmt.Errorf("%w: recovered decoded roots differ", ErrContent)
	}
	if err = b.io.verifyPublishedName(target, media); err != nil {
		return nil, false, err
	}
	if err = b.io.verifyPublishedName(sn, side); err != nil {
		return nil, false, err
	}
	a, err := artifactV2(r, m)
	if err != nil {
		return nil, false, err
	}
	if _, err = snapshot.Seek(0, 0); err != nil {
		return nil, false, err
	}
	if err = ctx.Err(); err != nil {
		return nil, false, err
	}
	owned = false
	return &PublishedMedia{artifact: a, state: &publishedMediaState{file: snapshot}}, true, nil
}

// OpenChunkAssembly serializes restart cleanup and inspection across processes.
func (b *ChunkAssemblerV2) OpenChunkAssembly(ctx context.Context, v evidence.VerifiedBindingV2) (_ *PublishedMedia, found bool, err error) {
	r, err := b.admit(ctx, v)
	if err != nil {
		return nil, false, err
	}
	unlock, err := b.lockV2(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err = b.recoverJournalV2(ctx, v, r); err != nil {
		return nil, false, err
	}
	return b.openChunkAssemblyV2(ctx, v)
}
func (b *ChunkAssemblerV2) InspectChunkAssembly(ctx context.Context, v evidence.VerifiedBindingV2) (a api.RenderArtifact, found bool, err error) {
	m, found, err := b.OpenChunkAssembly(ctx, v)
	if err != nil || !found {
		return a, found, err
	}
	a = m.Artifact()
	err = m.Close()
	if err != nil {
		return api.RenderArtifact{}, false, err
	}
	return a, true, nil
}
