package backendstage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// ReceiptProfileV1 identifies the job-scoped artifact receipt committed in a
// publish-stage checkpoint.
const ReceiptProfileV1 = "cutly.render.backend-publish-receipt.v1"

type receipt struct {
	Profile  string                   `json:"profile"`
	Artifact renderapi.RenderArtifact `json:"artifact"`
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
