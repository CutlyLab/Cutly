package audiograph_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/evaluator"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/render/audiograph"
	"github.com/CutlyLab/Cutly/render/pcm16le"
	"github.com/CutlyLab/Cutly/render/wavsource"
)

func domainRT(v, r int64) documentv2.RationalTime { return documentv2.RationalTime{Value: v, Rate: r} }
func TestDomainStrictWAVMixedFullIdentityOriginalIndexesAndPCM(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile("../wavsource/testdata/source-v1.vectors.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Cases []struct {
			PCM          []int16 `json:"pcm"`
			SampleRate   uint32  `json:"sample_rate"`
			Channels     uint16  `json:"channels"`
			ExpectedHash string  `json:"expected_encoded_hash"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	vector := vectors.Cases[0]
	encoded := make([]byte, 44+len(vector.PCM)*2)
	copy(encoded, "RIFF")
	binary.LittleEndian.PutUint32(encoded[4:], uint32(len(encoded)-8))
	copy(encoded[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(encoded[16:], 16)
	binary.LittleEndian.PutUint16(encoded[20:], 1)
	binary.LittleEndian.PutUint16(encoded[22:], vector.Channels)
	binary.LittleEndian.PutUint32(encoded[24:], vector.SampleRate)
	binary.LittleEndian.PutUint32(encoded[28:], vector.SampleRate*uint32(vector.Channels)*2)
	binary.LittleEndian.PutUint16(encoded[32:], vector.Channels*2)
	binary.LittleEndian.PutUint16(encoded[34:], 16)
	copy(encoded[36:], "data")
	binary.LittleEndian.PutUint32(encoded[40:], uint32(len(vector.PCM)*2))
	for i, v := range vector.PCM {
		binary.LittleEndian.PutUint16(encoded[44+i*2:], uint16(v))
	}
	hashBytes := sha256.Sum256(encoded)
	contentHash := "sha256:" + hex.EncodeToString(hashBytes[:])
	if contentHash != vector.ExpectedHash {
		t.Fatal("existing strict WAV vector bytes changed")
	}
	raw, err = os.ReadFile("../../conformance/fixtures/document-v2.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	d.AudioBuses = nil
	d.Timelines[0].Transitions = nil
	for i := range d.Timelines[0].Tracks {
		tr := &d.Timelines[0].Tracks[i]
		tr.AudioBusID = ""
		for j := range tr.Clips {
			tr.Clips[j].PropertySet = nil
			tr.Clips[j].KeyframeLanes = nil
		}
	}
	d.Assets[2].ContentHash = contentHash
	duration := domainRT(1, 1)
	d.Assets[2].Duration = &duration
	tr := &d.Timelines[0].Tracks[1]
	clip := tr.Clips[0]
	clip.TimelineRange = documentv2.TimeRange{Start: domainRT(2, 1), Duration: duration}
	clip.SourceRange = documentv2.TimeRange{Start: domainRT(0, 1), Duration: duration}
	before := clip
	before.ID = "outside-same-asset"
	before.TimelineRange.Start = domainRT(0, 1)
	before.PropertySet = &documentv2.PropertySet{Extensions: []documentv2.ExtensionProperty{{Namespace: "test.unselected.v1", AffectsRendering: true, RequiredCapability: "test.unselected.v1", Value: json.RawMessage(`{"preserve":true}`)}}}
	tr.Clips = []documentv2.Clip{before, clip}
	// Visual/text/vector and annotation tracks remain interleaved around original audio track 2.
	tracks := d.Timelines[0].Tracks
	d.Timelines[0].Tracks = []documentv2.Track{tracks[0], tracks[4], tracks[1], tracks[2], tracks[3]}
	hidden := clip
	hidden.ID = "hidden-audio"
	hidden.AssetID = "hidden-unbound"
	hiddenAsset := d.Assets[2]
	hiddenAsset.ID = hidden.AssetID
	d.Assets = append(d.Assets, hiddenAsset)
	d.Timelines = append([]documentv2.Timeline{{ID: "hidden-timeline", Tracks: []documentv2.Track{{ID: "hidden-track", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{hidden}}}}}, d.Timelines...)
	documentHash, err := documentv2.DocumentHash(d)
	if err != nil {
		t.Fatal(err)
	}
	offered := videoedit.BackendCapabilities{BackendID: "trusted-fixed-audio", TrackKinds: []videoedit.TrackKind{"audio"}, AssetKinds: []videoedit.AssetKind{"audio"}}
	scope := evaluator.DomainScope{Domain: evaluator.DomainAudio, Selection: documentv2.CapabilitySelection{TimelineID: "timeline-main", ClipIDs: []documentv2.ID{"clip-audio"}}, RequestedRange: documentv2.TimeRange{Start: domainRT(4, 2), Duration: duration}}
	pre, ds := audiograph.PreflightDomain(ctx, d, scope, offered, audiograph.Limits{})
	if documentv2.HasErrorDiagnostics(ds) || pre.DocumentHash != documentHash {
		t.Fatalf("preflight %+v", ds)
	}
	// Existing strict provider intentionally binds every enabled audio asset, even outside the graph
	// scope. The graph needs ONLY selected asset media evidence; do not weaken provider authority.
	provider, err := wavsource.Compile(ctx, d, []wavsource.Binding{{DocumentHash: documentHash, AssetID: "asset-audio", EncodedWAV: encoded}, {DocumentHash: documentHash, AssetID: "hidden-unbound", EncodedWAV: encoded}}, wavsource.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	media := provider.MediaBindings()
	var selected audiograph.MediaBinding
	for _, b := range media {
		if b.AssetID == "asset-audio" {
			selected = b
		}
	}
	p, ds := audiograph.CompileDomain(ctx, d, scope, audiograph.CompileOptions{Offered: offered, Bindings: []audiograph.MediaBinding{selected}})
	if p == nil || documentv2.HasErrorDiagnostics(ds) {
		t.Fatalf("compile %+v", ds)
	}
	if p.DocumentHash() != documentHash || len(p.Receipt().Sources) != 1 || p.Receipt().Sources[0].AssetIndex != 2 {
		t.Fatal("pruned identity or extra binding")
	}
	value, err := p.EvaluateAt(ctx, "timeline-main", domainRT(17, 8), provider)
	if err != nil {
		t.Fatal(err)
	}
	request := value.ActiveClips[0].Source.Request
	if request.DocumentHash != documentHash || request.TimelineIndex != 1 || request.TrackIndex != 2 || request.ClipIndex != 1 || request.AssetIndex != 2 || request.AssetID != "asset-audio" || request.ClipID != "clip-audio" || request.SourceTime != domainRT(1, 8) || value.Profile != audiograph.ProfileV1 || value.Output.Left.String() != "-1/2" {
		t.Fatalf("request/output %+v %+v", request, value.Output)
	}
	clock := videoedit.SampleClock{Origin: domainRT(0, 1), SamplesPerSecond: 4}
	block, err := p.EvaluateBlock(ctx, "timeline-main", clock, 8, 4, provider)
	if err != nil {
		t.Fatal(err)
	}
	pcm, err := pcm16le.Quantize(ctx, 4, 8, block.Samples, pcm16le.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	pcmBytes, err := pcm.PCM.Bytes()
	if err != nil || hex.EncodeToString(pcmBytes) != "0080008000000000ff7fff7f00400040" {
		t.Fatalf("PCM %x %v", pcmBytes, err)
	}
	if old, ds := audiograph.Compile(d, audiograph.CompileOptions{Offered: offered, Bindings: media}); old != nil || !documentv2.HasErrorDiagnostics(ds) {
		t.Fatal("legacy whole-document capability rejection relaxed")
	}
	// Original out-of-domain edits must change identity, not get silently dropped.
	d.Revision++
	changed, ds := audiograph.CompileDomain(ctx, d, scope, audiograph.CompileOptions{Offered: offered, Bindings: []audiograph.MediaBinding{selected}})
	if changed == nil || changed.DocumentHash() == documentHash {
		t.Fatalf("full identity %+v", ds)
	}
	if v, e := changed.EvaluateAt(ctx, "timeline-main", domainRT(2, 1), provider); e == nil || !reflect.DeepEqual(v, audiograph.Evaluation{}) {
		t.Fatal("old strict provider replay accepted")
	}
}
