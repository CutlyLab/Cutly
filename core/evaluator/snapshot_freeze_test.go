package evaluator

import (
	"context"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestFreezeSnapshotIsCapabilityNeutralAndMatchesCompileSnapshot(t *testing.T) {
	document := textV2SnapshotDocument(t)
	limits := SnapshotLimits{MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20}

	frozen, diagnostics := FreezeSnapshot(context.Background(), document, limits)
	if frozen == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("snapshot=%v diagnostics=%+v", frozen, diagnostics)
	}
	program, compiled, compileDiagnostics := CompileSnapshot(
		context.Background(), document, snapshotOffered(t, document), limits,
	)
	if program == nil || compiled == nil || documentv2.HasErrorDiagnostics(compileDiagnostics) {
		t.Fatalf("program=%v snapshot=%v diagnostics=%+v", program, compiled, compileDiagnostics)
	}
	if frozen.DocumentHash() != program.DocumentHash() || frozen.DocumentHash() != compiled.DocumentHash() ||
		frozen.CanonicalBytes() != compiled.CanonicalBytes() ||
		!reflect.DeepEqual(frozen.Assets(), compiled.Assets()) ||
		!reflect.DeepEqual(frozen.Timelines(), compiled.Timelines()) {
		t.Fatalf("freeze/compile identity drift: freeze=%s/%d compile=%s/%d program=%s",
			frozen.DocumentHash(), frozen.CanonicalBytes(), compiled.DocumentHash(), compiled.CanonicalBytes(), program.DocumentHash())
	}

	unsupported := videoedit.BackendCapabilities{BackendID: "no-capabilities"}
	if rejectedProgram, rejectedSnapshot, rejectedDiagnostics := CompileSnapshot(
		context.Background(), document, unsupported, limits,
	); rejectedProgram != nil || rejectedSnapshot != nil || !documentv2.HasErrorDiagnostics(rejectedDiagnostics) {
		t.Fatalf("capability-aware compile unexpectedly accepted document: program=%v snapshot=%v diagnostics=%+v",
			rejectedProgram, rejectedSnapshot, rejectedDiagnostics)
	}

	textIndex := frozenTextAssetIndex(t, document)
	assets := frozen.Assets()
	textAsset := assets[textIndex]
	textSource := document.Assets[textIndex].Text
	if textSource == nil || textAsset.TextFill != textSource.Fill || !snapshotDigest(textAsset.TextBindingHash) {
		t.Fatalf("text discovery metadata=%+v source=%+v", textAsset, textSource)
	}
	_, receipt, err := frozen.BindTextSource(
		context.Background(), textAsset.ID, textIndex, DefaultFrozenTextSourceLimits(),
	)
	if err != nil || receipt.BindingHash != textAsset.TextBindingHash || receipt.FillR != textAsset.TextFill.R ||
		receipt.FillG != textAsset.TextFill.G || receipt.FillB != textAsset.TextFill.B || receipt.FillA != textAsset.TextFill.A {
		t.Fatalf("binding metadata receipt=%+v asset=%+v err=%v", receipt, textAsset, err)
	}

	timeline, track, clip, found := findSnapshotClip(frozen.Timelines(), textAsset.ID)
	if !found {
		t.Fatal("text clip metadata missing")
	}
	wantClip := document.Timelines[timeline.Index].Tracks[track.Index].Clips[clip.Index]
	if clip.TimelineRange != wantClip.TimelineRange || clip.SourceRange != wantClip.SourceRange || !clip.HasTextFillLane {
		t.Fatalf("clip metadata=%+v want=%+v", clip, wantClip)
	}
}

func TestFreezeSnapshotOwnsAndDefensivelyCopiesNewMetadata(t *testing.T) {
	document := textV2SnapshotDocument(t)
	limits := SnapshotLimits{MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20}
	snapshot, diagnostics := FreezeSnapshot(context.Background(), document, limits)
	if snapshot == nil || documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("snapshot=%v diagnostics=%+v", snapshot, diagnostics)
	}
	textIndex := frozenTextAssetIndex(t, document)
	wantAsset := snapshot.Assets()[textIndex]
	wantTimelines := snapshot.Timelines()
	timeline, track, clip, found := findSnapshotClip(wantTimelines, wantAsset.ID)
	if !found {
		t.Fatal("text clip metadata missing")
	}

	document.Assets[textIndex].Text.Fill.R++
	document.Timelines[timeline.Index].Tracks[track.Index].Clips[clip.Index].TimelineRange.Start.Value++
	document.Timelines[timeline.Index].Tracks[track.Index].Clips[clip.Index].SourceRange.Start.Value++
	document.Timelines[timeline.Index].Tracks[track.Index].Clips[clip.Index].KeyframeLanes = nil
	if got := snapshot.Assets()[textIndex]; got != wantAsset {
		t.Fatalf("snapshot aliases source asset: got=%+v want=%+v", got, wantAsset)
	}
	if got := snapshot.Timelines(); !reflect.DeepEqual(got, wantTimelines) {
		t.Fatalf("snapshot aliases source timeline: got=%+v want=%+v", got, wantTimelines)
	}

	returnedAssets := snapshot.Assets()
	returnedTimelines := snapshot.Timelines()
	returnedAssets[textIndex].TextBindingHash = "mutated"
	returnedAssets[textIndex].TextFill.R++
	returnedTimelines[timeline.Index].Tracks[track.Index].Clips[clip.Index].TimelineRange.Start.Value++
	returnedTimelines[timeline.Index].Tracks[track.Index].Clips[clip.Index].SourceRange.Start.Value++
	returnedTimelines[timeline.Index].Tracks[track.Index].Clips[clip.Index].HasTextFillLane = false
	if got := snapshot.Assets()[textIndex]; got != wantAsset {
		t.Fatalf("Assets aliases retained metadata: got=%+v want=%+v", got, wantAsset)
	}
	if got := snapshot.Timelines(); !reflect.DeepEqual(got, wantTimelines) {
		t.Fatalf("Timelines aliases retained metadata: got=%+v want=%+v", got, wantTimelines)
	}
}

func TestFreezeSnapshotLimitsValidationCancellationAndZeroPublication(t *testing.T) {
	document := textV2SnapshotDocument(t)
	limits := SnapshotLimits{MaxCanonicalDocumentBytes: 1 << 20, MaxDocumentNodes: 1 << 20}

	invalid := textV2SnapshotDocument(t)
	invalid.Assets[4].Text.FontAssetID = "missing-font"
	if snapshot, diagnostics := FreezeSnapshot(context.Background(), invalid, limits); snapshot != nil ||
		!documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("invalid snapshot=%v diagnostics=%+v", snapshot, diagnostics)
	}

	tiny := SnapshotLimits{MaxCanonicalDocumentBytes: 1, MaxDocumentNodes: 1}
	if snapshot, diagnostics := FreezeSnapshot(context.Background(), document, tiny); snapshot != nil ||
		!documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("limited snapshot=%v diagnostics=%+v", snapshot, diagnostics)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if snapshot, diagnostics := FreezeSnapshot(canceled, document, limits); snapshot != nil ||
		!documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("canceled snapshot=%v diagnostics=%+v", snapshot, diagnostics)
	}
	if snapshot, diagnostics := FreezeSnapshot(nil, document, limits); snapshot != nil ||
		!documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("nil-context snapshot=%v diagnostics=%+v", snapshot, diagnostics)
	}
	if snapshot, diagnostics := FreezeSnapshot(context.Background(), document, SnapshotLimits{}); snapshot != nil ||
		!documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("zero-limit snapshot=%v diagnostics=%+v", snapshot, diagnostics)
	}
}

func findSnapshotClip(
	timelines []SnapshotTimeline,
	assetID documentv2.ID,
) (SnapshotTimeline, SnapshotTrack, SnapshotClip, bool) {
	for _, timeline := range timelines {
		for _, track := range timeline.Tracks {
			for _, clip := range track.Clips {
				if clip.AssetID == assetID {
					return timeline, track, clip, true
				}
			}
		}
	}
	return SnapshotTimeline{}, SnapshotTrack{}, SnapshotClip{}, false
}
