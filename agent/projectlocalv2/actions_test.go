package projectlocalv2

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/agent/protocolv2"
	"github.com/CutlyLab/Cutly/agent/routerv2"
	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/project/localv2"
)

func TestRenderPlanUsesCurrentDurableDocumentV2(t *testing.T) {
	ctx := context.Background()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	document := documentv2.Document{
		SchemaVersion: 2,
		ID:            "document",
		Timelines:     []documentv2.Timeline{{ID: "main"}},
	}
	project, err := localv2.Create(ctx, directory, document, localv2.Options{})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(map[string]*localv2.Service{"project": project})
	if err != nil {
		t.Fatal(err)
	}
	actions, err := New(registry)
	if err != nil {
		t.Fatal(err)
	}
	router, err := routerv2.New(actions.Routes()...)
	if err != nil {
		t.Fatal(err)
	}
	renderRange := documentv2.TimeRange{
		Start:    documentv2.RationalTime{Value: 0, Rate: 1},
		Duration: documentv2.RationalTime{Value: 1, Rate: 1},
	}
	target := videoedit.RenderTarget{
		TimelineID:  "main",
		Width:       16,
		Height:      16,
		Channels:    2,
		PixelFormat: "rgba16",
		ColorSpace:  "linear-srgb",
		FrameClock: videoedit.FrameClock{
			Origin:          documentv2.RationalTime{Value: 0, Rate: 1},
			FramesPerSecond: documentv2.RationalTime{Value: 24, Rate: 1},
		},
		SampleClock: videoedit.SampleClock{
			Origin:           documentv2.RationalTime{Value: 0, Rate: 1},
			SamplesPerSecond: 48_000,
		},
	}
	payload, err := videoedit.CanonicalJSON(struct {
		Range  documentv2.TimeRange   `json:"range"`
		Target videoedit.RenderTarget `json:"target"`
	}{renderRange, target})
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.HandleAgentV2(ctx, protocolv2.Request{
		SchemaVersion:  protocolv2.SchemaVersion,
		Profile:        protocolv2.RequestProfile,
		RequestID:      "render-plan",
		Tool:           protocolv2.ToolRender,
		Action:         "plan",
		ProjectID:      "project",
		PayloadProfile: protocolv2.PayloadProfile(protocolv2.ToolRender, "plan"),
		Payload:        payload,
	})
	if err != nil || !result.OK || result.State != "planned" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var receipt struct {
		Profile string            `json:"profile"`
		Plan    renderplanv2.Plan `json:"plan"`
	}
	if err := json.Unmarshal(result.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Profile != "cutly.render-plan.document-v2.v2" || receipt.Plan.DocumentID != document.ID || receipt.Plan.TimelineID != "main" {
		t.Fatalf("receipt=%+v", receipt)
	}
	if err := renderplanv2.VerifyAgainstDocument(ctx, receipt.Plan, document); err != nil {
		t.Fatal(err)
	}
}

func TestRenderPlanRejectsMalformedPayload(t *testing.T) {
	service := &Service{}
	outcome, err := service.renderPlan(context.Background(), protocolv2.Request{Payload: json.RawMessage(`{"range":{}}`)})
	if err != nil || outcome.OK || outcome.State != "rejected" || len(outcome.Diagnostics) != 1 || outcome.Diagnostics[0].Code != CodePayloadInvalid {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}
