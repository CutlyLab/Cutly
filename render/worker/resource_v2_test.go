package worker

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/media/wavdecode"
	api "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/prepared"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
)

func TestProductionChildV2AuthorizedWAV(t *testing.T) {
	path := buildChildV2(t)
	ctx := context.Background()
	provider, err := measureAuthorizedV2(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	req, c := fixtureV2(t, provider)
	data := make([]byte, 44+4*2)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 4)
	binary.LittleEndian.PutUint32(data[28:], 8)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], 8)
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint16(data[44+i*2:], uint16(1000+i))
	}
	h := digestV2(data)
	duration := documentv2.RationalTime{Value: 1, Rate: 1}
	interval := documentv2.TimeRange{Start: documentv2.RationalTime{Value: 0, Rate: 1}, Duration: duration}
	d := req.Environment.Document
	d.Assets = []documentv2.Asset{{ID: "audio", Kind: documentv2.AssetKindAudio, URI: "cas://sha256/" + h[7:], ContentHash: h, Duration: &duration}}
	d.Timelines[0].Tracks = []documentv2.Track{{ID: "track", Kind: documentv2.TrackKindAudio, Clips: []documentv2.Clip{{ID: "clip", AssetID: "audio", TimelineRange: interval, SourceRange: interval}}}}
	target := req.Environment.Plan.Target
	target.SampleClock.SamplesPerSecond = 4
	p, ds := renderplanv2.Resolve(ctx, d, interval, target)
	if documentv2.HasErrorDiagnostics(ds) {
		t.Fatal(ds)
	}
	binding := resourceidentityv2.Binding{AssetID: "audio", Kind: documentv2.AssetKindAudio, ContentHash: h, SizeBytes: int64(len(data))}
	observed, err := resourceidentityv2.InspectAudio(ctx, binding, data, wavdecode.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	observedProbe, err := observed.Probe()
	if err != nil {
		t.Fatal(err)
	}
	fp, err := probe.Fingerprint(observedProbe)
	if err != nil {
		t.Fatal(err)
	}
	claim := api.MaterializedResourceV2{AssetID: "audio", Kind: documentv2.AssetKindAudio, ContentHash: h, SizeBytes: int64(len(data)), LocatorScheme: api.LocatorCAS, Locator: d.Assets[0].URI, MaterializationProfile: api.AVMaterializationProfileV2, ProbeFingerprint: &fp}
	m := req.Environment.Manifest
	m.Resources = []api.MaterializedResourceV2{claim}
	m.Output.SampleClock = target.SampleClock
	m.Output.SampleRate = 4
	m.Visual = api.ProgramSelectionV2{}
	m.Audio = api.ProgramSelectionV2{}
	m, err = api.FreezeExecutionManifestV2(ctx, p, m)
	if err != nil {
		t.Fatal(err)
	}
	req.Environment, err = prepared.NewEnvironmentV2(ctx, d, p, m)
	if err != nil {
		t.Fatal(err)
	}
	req.Chunk.Range = p.SampleRange
	resource := filepath.Join(t.TempDir(), "independent-host-selection.wav")
	if err = os.WriteFile(resource, data, 0600); err != nil {
		t.Fatal(err)
	}
	c.Resources = []HostResourceV2{{DocumentHash: p.DocumentHash, Claim: claim, Path: resource}}
	if err = AuthorizeLaunchEnvironmentV2(ctx, c, path, req.Environment); err != nil {
		t.Fatal("parent WAV verification", err)
	}
	runner, err := NewRunnerV2(ConfigV2{Process: Config{Executable: path, WorkingDirectory: filepath.Dir(path)}, LaunchConfig: launchFileV2(t, c), SpoolDirectory: t.TempDir(), MaxPayloadBytes: MaxStreamPayloadBytesV2})
	if err != nil {
		t.Fatal(err)
	}
	result, report, err := runner.Run(ctx, req)
	if err != nil || !result.OK {
		t.Fatalf("native WAV: result=%+v report=%+v err=%v", result, report, err)
	}
	if len(result.Payload) != 16 || binary.LittleEndian.Uint16(result.Payload) != 1000 {
		t.Fatal("actual WAV samples not rendered", result.Payload)
	}
	// Locator cannot select an arbitrary host path; the exact host allowlist is independent.
	denied := c
	denied.Resources = []HostResourceV2{}
	if err = AuthorizeLaunchEnvironmentV2(ctx, denied, path, req.Environment); err == nil {
		t.Fatal("unlisted resource authorized")
	}
	data[len(data)-1] ^= 1
	if err = os.WriteFile(resource, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = AuthorizeLaunchEnvironmentV2(ctx, c, path, req.Environment); err == nil {
		t.Fatal("tampered resource authorized")
	}
	result, _, err = runner.Run(ctx, req)
	if err == nil && result.OK {
		t.Fatal("child accepted resource drift")
	}
}
