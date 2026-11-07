package visualcompositor

import (
	"fmt"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/render/cpucompositor"
	"github.com/CutlyLab/Cutly/render/expressionlayer"
	"github.com/CutlyLab/Cutly/render/rastercompositor"
	"github.com/CutlyLab/Cutly/render/textlayer"
	"github.com/CutlyLab/Cutly/render/vectorraster"
)

const ResultSchemaVersion = 1

type SourceKind string

const (
	SourceKindNative     SourceKind = "native"
	SourceKindExpression SourceKind = "expression"
	SourceKindText       SourceKind = "text"
)

// ClipBinding is the exact evaluator input represented by one ordered source receipt. For a
// transition handle Visible may be false; the expression request still carries these same times.
type ClipBinding struct {
	TimelineID    documentv2.ID           `json:"timeline_id"`
	TimelineIndex int                     `json:"timeline_index"`
	TrackID       documentv2.ID           `json:"track_id"`
	TrackIndex    int                     `json:"track_index"`
	TrackKind     documentv2.TrackKind    `json:"track_kind"`
	ClipID        documentv2.ID           `json:"clip_id"`
	ClipIndex     int                     `json:"clip_index"`
	AssetID       documentv2.ID           `json:"asset_id"`
	AssetIndex    int                     `json:"asset_index"`
	AssetKind     documentv2.AssetKind    `json:"asset_kind"`
	TimelineTime  documentv2.RationalTime `json:"timeline_time"`
	LocalTime     documentv2.RationalTime `json:"local_time"`
	SourceTime    documentv2.RationalTime `json:"source_time"`
	Visible       bool                    `json:"visible"`
}

// TransitionBinding makes a transition handle self-describing without relying on receipt array
// position or re-evaluating the document. Clip is the exact outgoing/incoming evaluator handle.
type TransitionBinding struct {
	TransitionID      documentv2.ID                        `json:"transition_id"`
	TransitionIndex   int                                  `json:"transition_index"`
	TransitionProfile string                               `json:"transition_profile"`
	Role              expressionlayer.TransitionHandleRole `json:"role"`
	Clip              ClipBinding                          `json:"clip"`
}

// SourceEvaluation is a strict outer union. Both keys are always encoded and exactly one is
// non-null, so a visible transition handle can never be confused with an ordinary clip sample.
type SourceEvaluation struct {
	Clip       *ClipBinding       `json:"clip" wirejson:"nullable"`
	Transition *TransitionBinding `json:"transition" wirejson:"nullable"`
}

func (value SourceEvaluation) clipBinding() (ClipBinding, bool) {
	if value.Clip != nil && value.Transition == nil {
		return *value.Clip, true
	}
	if value.Clip == nil && value.Transition != nil {
		return value.Transition.Clip, true
	}
	return ClipBinding{}, false
}

type ExpressionFrameReceipt struct {
	ContractVersion          int                                      `json:"contract_version"`
	VisibleRequest           *expressionlayer.Request                 `json:"visible_request" wirejson:"nullable"`
	TransitionRequest        *expressionlayer.TransitionHandleRequest `json:"transition_request" wirejson:"nullable"`
	AssetSemanticHashProfile string                                   `json:"asset_semantic_hash_profile"`
	AssetSemanticHash        string                                   `json:"asset_semantic_hash"`
	RasterReceipt            vectorraster.Receipt                     `json:"raster_receipt"`
	SurfaceIdentity          expressionlayer.SurfaceIdentity          `json:"surface_identity"`
}

// SourceReceipt is a strict discriminated union. The two v1 branch keys remain always serialized;
// the additive Text key is omitted for v1 byte stability. Exactly one branch is non-null and it
// must agree with Kind.
type SourceReceipt struct {
	Kind       SourceKind                      `json:"kind"`
	Evaluation SourceEvaluation                `json:"evaluation"`
	Native     *rastercompositor.SourceReceipt `json:"native" wirejson:"nullable"`
	Expression *ExpressionFrameReceipt         `json:"expression" wirejson:"nullable"`
	Text       *textlayer.FrameReceipt         `json:"text,omitempty"`
}

func (value SourceReceipt) Validate() error {
	binding, validEvaluation := value.Evaluation.clipBinding()
	if !validEvaluation || binding.TimelineID == "" || binding.TimelineIndex < 0 || binding.TrackID == "" || binding.TrackIndex < 0 ||
		binding.ClipID == "" || binding.ClipIndex < 0 || binding.AssetID == "" || binding.AssetIndex < 0 {
		return fmt.Errorf("%w: evaluation branch", ErrSourceInvalid)
	}
	if transition := value.Evaluation.Transition; transition != nil {
		if transition.TransitionID == "" || transition.TransitionIndex < 0 || transition.TransitionProfile != documentv2.TransitionDissolveV1 ||
			(transition.Role != expressionlayer.TransitionHandleOutgoing && transition.Role != expressionlayer.TransitionHandleIncoming) {
			return fmt.Errorf("%w: transition binding", ErrSourceInvalid)
		}
	}
	switch value.Kind {
	case SourceKindNative:
		if value.Native == nil || value.Expression != nil || value.Text != nil {
			return fmt.Errorf("%w: native receipt branch", ErrSourceInvalid)
		}
		if !nativeRequestMatchesBinding(value.Native.Request, binding) {
			return fmt.Errorf("%w: native evaluation binding", ErrSourceInvalid)
		}
	case SourceKindExpression:
		if value.Native != nil || value.Expression == nil || value.Text != nil {
			return fmt.Errorf("%w: expression receipt branch", ErrSourceInvalid)
		}
		visible := value.Expression.VisibleRequest != nil
		transition := value.Expression.TransitionRequest != nil
		if visible == transition {
			return fmt.Errorf("%w: expression request branch", ErrSourceInvalid)
		}
		if value.Evaluation.Clip != nil {
			if !visible || !expressionRequestMatchesBinding(*value.Expression.VisibleRequest, binding) {
				return fmt.Errorf("%w: visible expression binding", ErrSourceInvalid)
			}
		} else {
			outer := value.Evaluation.Transition
			request := value.Expression.TransitionRequest
			if request == nil || !transitionRequestMatchesBinding(*request, *outer) {
				return fmt.Errorf("%w: transition expression binding", ErrSourceInvalid)
			}
		}
	case SourceKindText:
		if value.Native != nil || value.Expression != nil || value.Text == nil || value.Evaluation.Clip == nil ||
			value.Evaluation.Transition != nil || value.Text.ContractVersion != textlayer.ContractVersion ||
			!textRequestMatchesBinding(value.Text.Request, binding) {
			return fmt.Errorf("%w: text receipt branch", ErrSourceInvalid)
		}
	default:
		return fmt.Errorf("%w: source kind %q", ErrSourceInvalid, value.Kind)
	}
	return nil
}

func textRequestMatchesBinding(request textlayer.Request, binding ClipBinding) bool {
	return request.TimelineID == binding.TimelineID && request.TimelineIndex == binding.TimelineIndex &&
		request.TrackID == binding.TrackID && request.TrackIndex == binding.TrackIndex && request.TrackKind == binding.TrackKind &&
		request.ClipID == binding.ClipID && request.ClipIndex == binding.ClipIndex &&
		request.AssetID == binding.AssetID && request.AssetIndex == binding.AssetIndex && request.AssetKind == binding.AssetKind &&
		request.TimelineTime == binding.TimelineTime && request.LocalTime == binding.LocalTime && request.SourceTime == binding.SourceTime
}

func nativeRequestMatchesBinding(request rastercompositor.NativeSourceRequest, binding ClipBinding) bool {
	return request.TimelineID == binding.TimelineID && request.TimelineIndex == binding.TimelineIndex &&
		request.TrackID == binding.TrackID && request.TrackIndex == binding.TrackIndex &&
		request.ClipID == binding.ClipID && request.ClipIndex == binding.ClipIndex &&
		request.AssetID == binding.AssetID && request.AssetIndex == binding.AssetIndex && request.AssetKind == binding.AssetKind &&
		request.TimelineTime == binding.TimelineTime && request.LocalTime == binding.LocalTime && request.SourceTime == binding.SourceTime
}

func expressionRequestMatchesBinding(request expressionlayer.Request, binding ClipBinding) bool {
	return request.TimelineID == binding.TimelineID && request.TimelineIndex == binding.TimelineIndex &&
		request.TrackID == binding.TrackID && request.TrackIndex == binding.TrackIndex && request.TrackKind == binding.TrackKind &&
		request.ClipID == binding.ClipID && request.ClipIndex == binding.ClipIndex &&
		request.AssetID == binding.AssetID && request.AssetIndex == binding.AssetIndex && request.AssetKind == binding.AssetKind &&
		request.TimelineTime == binding.TimelineTime && request.LocalTime == binding.LocalTime && request.SourceTime == binding.SourceTime
}

func transitionRequestMatchesBinding(request expressionlayer.TransitionHandleRequest, binding TransitionBinding) bool {
	return request.TransitionID == binding.TransitionID && request.TransitionIndex == binding.TransitionIndex &&
		request.TransitionProfile == binding.TransitionProfile && request.Role == binding.Role &&
		expressionTransitionClipMatchesBinding(request, binding.Clip)
}

func expressionTransitionClipMatchesBinding(request expressionlayer.TransitionHandleRequest, binding ClipBinding) bool {
	return request.TimelineID == binding.TimelineID && request.TimelineIndex == binding.TimelineIndex &&
		request.TrackID == binding.TrackID && request.TrackIndex == binding.TrackIndex && request.TrackKind == binding.TrackKind &&
		request.ClipID == binding.ClipID && request.ClipIndex == binding.ClipIndex &&
		request.AssetID == binding.AssetID && request.AssetIndex == binding.AssetIndex && request.AssetKind == binding.AssetKind &&
		request.TimelineTime == binding.TimelineTime && request.LocalTime == binding.LocalTime && request.SourceTime == binding.SourceTime
}

type Providers struct {
	Native           rastercompositor.NativeSourceProvider
	Expression       expressionlayer.ExpressionLayerProvider
	ExpressionHandle expressionlayer.TransitionHandleProvider
	Text             textlayer.TextLayerProvider
}

// Result binds the complete target/profile identity and ordered source union. Surface is an
// immutable runtime value and is intentionally omitted from serialization.
type Result struct {
	SchemaVersion                   int                     `json:"schema_version"`
	Profile                         string                  `json:"profile"`
	TimelineID                      documentv2.ID           `json:"timeline_id"`
	TimelineTime                    documentv2.RationalTime `json:"timeline_time"`
	Width                           int                     `json:"width"`
	Height                          int                     `json:"height"`
	DocumentHash                    string                  `json:"document_hash"`
	BuildFingerprint                string                  `json:"build_fingerprint"`
	NativeSourceContractVersion     int                     `json:"native_source_contract_version"`
	ExpressionContractVersion       int                     `json:"expression_contract_version"`
	ExpressionHandleContractVersion int                     `json:"expression_handle_contract_version"`
	TextContractVersion             int                     `json:"text_contract_version,omitempty"`
	SurfaceProfile                  string                  `json:"surface_profile"`
	PixelHashProfile                string                  `json:"pixel_hash_profile"`
	RasterProfile                   string                  `json:"raster_profile"`
	KernelProfile                   string                  `json:"kernel_profile"`
	LayerOrder                      string                  `json:"layer_order"`
	Sources                         []SourceReceipt         `json:"sources"`
	PixelHash                       string                  `json:"pixel_hash"`
	Surface                         *cpucompositor.Surface  `json:"-"`
}

type FrameResult struct {
	CompositionFrameIndex int64  `json:"composition_frame_index"`
	Result                Result `json:"result"`
}
