package evaluator

import (
	"context"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestCompileSnapshotCanonicalIdentityDefensiveMetadataAndNoDocumentAlias(t *testing.T) {
	document := validDocument(t)
	offered := snapshotOffered(t, document)
	wantHash, err := documentv2.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	limits := SnapshotLimits{MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20}
	program, snapshot, diagnostics := CompileSnapshot(context.Background(), document, offered, limits)
	if program == nil || snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("program=%v snapshot=%v diagnostics=%+v", program, snapshot, diagnostics)
	}
	if program.DocumentHash() != wantHash || snapshot.DocumentHash() != wantHash || snapshot.CanonicalBytes() == 0 {
		t.Fatalf("program hash=%s snapshot hash=%s want=%s bytes=%d", program.DocumentHash(), snapshot.DocumentHash(), wantHash, snapshot.CanonicalBytes())
	}

	before, err := program.EvaluateClip("timeline-main", "clip-video-a", rt(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	document.Timelines[0].Tracks[0].Clips[0].SourceRange.Start.Value = 999
	document.Timelines[0].Tracks[0].Clips[0].PropertySet.Opacity.Value.Numerator = 0
	after, err := program.EvaluateClip("timeline-main", "clip-video-a", rt(1, 1))
	if err != nil || after.SourceTime != before.SourceTime || after.Properties.Opacity.String() != before.Properties.Opacity.String() {
		t.Fatalf("input mutation changed program: before=%+v after=%+v err=%v", before, after, err)
	}

	assets := snapshot.Assets()
	timelines := snapshot.Timelines()
	assets[0].ContentHash = "mutated"
	timelines[0].Tracks[0].Clips[0].ID = "mutated"
	if snapshot.Assets()[0].ContentHash == "mutated" || snapshot.Timelines()[0].Tracks[0].Clips[0].ID == "mutated" {
		t.Fatal("snapshot accessor aliases retained metadata")
	}
}

func TestCompileSnapshotLimitsCancellationAndZeroPublication(t *testing.T) {
	document := validDocument(t)
	offered := snapshotOffered(t, document)
	tiny := SnapshotLimits{MaxCanonicalDocumentBytes: 1, MaxDocumentNodes: 1}
	if program, snapshot, diagnostics := CompileSnapshot(context.Background(), document, offered, tiny); program != nil || snapshot != nil ||
		!documentv2.HasErrorDiagnostics(diagnostics) || !strings.Contains(diagnostics[len(diagnostics)-1].Message, ErrCanonicalDocumentLimit.Error()) {
		t.Fatalf("limit program=%v snapshot=%v diagnostics=%+v", program, snapshot, diagnostics)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	limits := SnapshotLimits{MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20}
	if program, snapshot, diagnostics := CompileSnapshot(ctx, document, offered, limits); program != nil || snapshot != nil || !documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("cancel program=%v snapshot=%v diagnostics=%+v", program, snapshot, diagnostics)
	}
}

func snapshotOffered(t *testing.T, document documentv2.Document) videoedit.BackendCapabilities {
	t.Helper()
	required, diagnostics := documentv2.RequiredCapabilities(document)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("requirements=%+v", diagnostics)
	}
	result := videoedit.BackendCapabilities{BackendID: "snapshot-test", Features: required.Features}
	for _, kind := range required.TrackKinds {
		result.TrackKinds = append(result.TrackKinds, videoedit.TrackKind(kind))
	}
	for _, kind := range required.AssetKinds {
		result.AssetKinds = append(result.AssetKinds, videoedit.AssetKind(kind))
	}
	return result
}
