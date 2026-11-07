package backendstage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/chunkplan"
)

// ReceiptProfileV1 identifies the job-scoped artifact receipt committed in a
// publish-stage checkpoint.
const ReceiptProfileV1 = "cutly.render.backend-publish-receipt.v1"

type receipt struct {
	Profile  string                   `json:"profile"`
	Artifact renderapi.RenderArtifact `json:"artifact"`
}

type referencePublishReceipt struct {
	Profile   string                   `json:"profile"`
	Artifact  renderapi.RenderArtifact `json:"artifact"`
	Aggregate aggregateBinding         `json:"aggregate"`
}

func encodeReceipt(artifact renderapi.RenderArtifact) (json.RawMessage, error) {
	raw, err := canonicaljson.Marshal(receipt{Profile: ReceiptProfileV1, Artifact: artifact})
	if err != nil {
		return nil, wrapError(ErrArtifact, "encode receipt", err)
	}
	return raw, nil
}

func decodeReceipt(raw json.RawMessage) (renderapi.RenderArtifact, error) {
	if len(raw) == 0 {
		return renderapi.RenderArtifact{}, fmt.Errorf("%w: empty receipt", ErrArtifact)
	}
	canonical, err := canonicaljson.Canonicalize(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		return renderapi.RenderArtifact{}, errors.Join(fmt.Errorf("%w: receipt is not canonical", ErrArtifact), err)
	}
	var value receipt
	if err := wirejson.Decode(canonical, &value); err != nil {
		return renderapi.RenderArtifact{}, wrapError(ErrArtifact, "strict receipt decode", err)
	}
	if value.Profile != ReceiptProfileV1 {
		return renderapi.RenderArtifact{}, fmt.Errorf("%w: receipt profile %q", ErrArtifact, value.Profile)
	}
	reencoded, err := canonicaljson.Marshal(value)
	if err != nil || !bytes.Equal(reencoded, raw) {
		return renderapi.RenderArtifact{}, errors.Join(fmt.Errorf("%w: receipt canonical form drift", ErrArtifact), err)
	}
	return value.Artifact, nil
}

func encodeReferencePublishReceipt(artifact renderapi.RenderArtifact, binding aggregateBinding) (json.RawMessage, error) {
	if err := validateAggregateBinding(binding); err != nil {
		return nil, err
	}
	raw, err := canonicaljson.Marshal(referencePublishReceipt{
		Profile: ReferenceChunkPublishReceiptV1, Artifact: artifact, Aggregate: binding,
	})
	if err != nil {
		return nil, wrapError(ErrArtifact, "encode reference publish receipt", err)
	}
	return raw, nil
}

func decodeReferencePublishReceipt(raw json.RawMessage) (renderapi.RenderArtifact, aggregateBinding, error) {
	if len(raw) == 0 {
		return renderapi.RenderArtifact{}, aggregateBinding{}, fmt.Errorf("%w: empty reference publish receipt", ErrArtifact)
	}
	canonical, err := canonicaljson.Canonicalize(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		return renderapi.RenderArtifact{}, aggregateBinding{}, errors.Join(fmt.Errorf("%w: reference publish receipt is not canonical", ErrArtifact), err)
	}
	var value referencePublishReceipt
	if err := wirejson.Decode(canonical, &value); err != nil {
		return renderapi.RenderArtifact{}, aggregateBinding{}, wrapError(ErrArtifact, "strict reference publish receipt decode", err)
	}
	if value.Profile != ReferenceChunkPublishReceiptV1 {
		return renderapi.RenderArtifact{}, aggregateBinding{}, fmt.Errorf("%w: reference publish receipt profile %q", ErrArtifact, value.Profile)
	}
	if err := validateAggregateBinding(value.Aggregate); err != nil {
		return renderapi.RenderArtifact{}, aggregateBinding{}, err
	}
	reencoded, err := canonicaljson.Marshal(value)
	if err != nil || !bytes.Equal(reencoded, raw) {
		return renderapi.RenderArtifact{}, aggregateBinding{}, errors.Join(fmt.Errorf("%w: reference publish receipt canonical form drift", ErrArtifact), err)
	}
	return value.Artifact, value.Aggregate, nil
}

func validateAggregateBinding(value aggregateBinding) error {
	if value.Profile != ReferenceChunkAggregateBindingV1 || value.ResumeMode != ReferenceChunkResumeModeV1 ||
		(value.Video == nil && value.Audio == nil) {
		return fmt.Errorf("%w: invalid reference aggregate header", ErrArtifact)
	}
	for _, item := range []struct {
		name string
		root *aggregateRoot
	}{{name: "video", root: value.Video}, {name: "audio", root: value.Audio}} {
		name, root := item.name, item.root
		if root == nil {
			continue
		}
		if root.Domain == "" || root.UnitBytes <= 0 || root.Range.Count <= 0 || root.Range.Validate() != nil ||
			root.ChunkCount <= 0 || !cachekey.CanonicalSHA256(root.RootHash) {
			return fmt.Errorf("%w: invalid %s aggregate root", ErrArtifact, name)
		}
	}
	if value.Video != nil && value.Video.Domain != chunkplan.VideoOutputProfileV1 {
		return fmt.Errorf("%w: invalid video aggregate domain", ErrArtifact)
	}
	if value.Audio != nil && value.Audio.Domain != chunkplan.AudioOutputProfileV1 {
		return fmt.Errorf("%w: invalid audio aggregate domain", ErrArtifact)
	}
	return nil
}
