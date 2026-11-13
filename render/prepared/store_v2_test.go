package prepared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CutlyLab/Cutly/core/documentv2"
	"github.com/CutlyLab/Cutly/core/renderplanv2"
	"github.com/CutlyLab/Cutly/core/videoedit"
	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// environmentV2Fixture builds one complete, internally consistent S3 environment from the
// shared conformance Document v2 fixture: S1 resolve, S2 freeze, and the same draft shape the
// S2 admission tests use.
func environmentV2Fixture(t *testing.T) (documentv2.Document, renderplanv2.Plan, renderapi.ExecutionManifestV2) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "document-v2.valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := documentv2.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	requested := documentv2.TimeRange{Start: videoedit.RationalTime{Value: 4, Rate: 1}, Duration: videoedit.RationalTime{Value: 2, Rate: 1}}
	target := videoedit.RenderTarget{
		TimelineID: "timeline-main", Width: 1920, Height: 1080,
		FrameClock:  videoedit.FrameClock{Origin: videoedit.RationalTime{Value: 0, Rate: 1}, FramesPerSecond: videoedit.RationalTime{Value: 24, Rate: 1}},
		SampleClock: videoedit.SampleClock{Origin: videoedit.RationalTime{Value: 0, Rate: 1}, SamplesPerSecond: 48000},
		Channels:    2, PixelFormat: "rgba16", ColorSpace: "linear-bt709",
	}
	plan, diagnostics := renderplanv2.Resolve(context.Background(), document, requested, target)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("resolve diagnostics: %+v", diagnostics)
	}
	resources := make([]renderapi.MaterializedResourceV2, len(plan.Resources))
	for index, planned := range plan.Resources {
		contentHash := planned.ContentHash
		if contentHash == "" {
			contentHash = digest('a')
		}
		resources[index] = renderapi.MaterializedResourceV2{
			AssetID: planned.AssetID, Kind: planned.Kind, ContentHash: contentHash,
			SizeBytes: int64(index + 1), LocatorScheme: renderapi.LocatorCAS,
			Locator:                "cas://sha256/" + contentHash[len("sha256:"):],
			MaterializationProfile: "cutly.materialize.cas.v1",
		}
		if planned.Kind == documentv2.AssetKindVideo || planned.Kind == documentv2.AssetKindAudio {
			resources[index].ProbeFingerprint = digest('b')
		} else {
			resources[index].InspectionFingerprint = digest('c')
		}
	}
	draft := renderapi.ExecutionManifestV2{
		CompilerBuild:    digest('1'),
		EvaluatorBuild:   digest('2'),
		ProviderBuild:    digest('3'),
		PipelineProfile:  renderapi.PipelineChunkedRestartableV1,
		TransportProfile: renderapi.TransportCASSpoolV1,
		Resources:        resources,
		Backend: renderapi.BackendCapabilitiesV2{
			BackendID: "native-cpu", BuildFingerprint: digest('4'),
			TrackKinds: append([]documentv2.TrackKind(nil), plan.RequiredCapabilities.TrackKinds...),
			AssetKinds: append([]documentv2.AssetKind(nil), plan.RequiredCapabilities.AssetKinds...),
			Features:   append([]videoedit.CapabilityID(nil), plan.RequiredCapabilities.Features...),
			MaxWidth:   8192, MaxHeight: 8192,
		},
		MediaRuntime: renderapi.MediaRuntime{
			FFmpegBuild: digest('5'), DecoderProfile: "cutly.decode.native-media.v1",
			EncoderProfile: "cutly.encode.h264-aac.v1", TextShaperBuild: digest('6'),
			ColorProfile: "cutly.color.linear-bt709.v1",
		},
		Output: renderapi.OutputSpecV2{
			ReferenceProfile: "cutly.reference.document-v2.v1", Format: "mp4",
			Width: plan.Target.Width, Height: plan.Target.Height,
			PixelFormat: plan.Target.PixelFormat, ColorSpace: plan.Target.ColorSpace,
			VideoCodec: "h264", AudioCodec: "aac",
			SampleRate: plan.Target.SampleClock.SamplesPerSecond, Channels: plan.Target.Channels,
			Intermediate: renderapi.IntermediateProfilesV2{
				VideoSurfaceProfile: renderapi.IntermediateVideoSurfaceRGBA16V1,
				AudioBlockProfile:   renderapi.IntermediateAudioBlockPCM16LEV1,
			},
			DecodedVideoProfile: renderapi.DecodedVideoProfileRGBA8V1,
			DecodedAudioProfile: renderapi.DecodedAudioProfilePCM16LEV1,
		},
		Visual: renderapi.ProgramSelectionV2{
			Enabled: true, Profile: "cutly.program.visual.reference.v1",
			DescriptorFingerprint: digest('7'), Configuration: json.RawMessage(`{"z":2,"a":1}`),
			SourceManifestFingerprint: digest('8'),
		},
		Audio: renderapi.ProgramSelectionV2{
			Enabled: true, Profile: "cutly.program.audio.reference.v1",
			DescriptorFingerprint: digest('9'), Configuration: json.RawMessage(`{"gain":1}`),
			SourceManifestFingerprint: digest('a'),
		},
		DeterministicSeed: 42,
	}
	// S3 admits only manifests that are already the exact frozen artifact for this plan.
	bound, err := renderplanv2.VerifyAgainstDocument(context.Background(), plan, document)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := renderapi.FreezeExecutionManifestV2(context.Background(), bound, draft)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := verified.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return document, plan, frozen
}

func frozenV2Fingerprint(t *testing.T, document documentv2.Document, plan renderplanv2.Plan, draft renderapi.ExecutionManifestV2) string {
	t.Helper()
	bound, err := renderplanv2.VerifyAgainstDocument(context.Background(), plan, document)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := renderapi.FreezeExecutionManifestV2(context.Background(), bound, draft)
	if err != nil {
		t.Fatal(err)
	}
	return verified.Fingerprint()
}

func TestPutV2ResolveV2ReopenIdempotenceAndOwnership(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, plan, draft := environmentV2Fixture(t)
	manifestFingerprint := frozenV2Fingerprint(t, document, plan, draft)

	environment, created, err := store.PutV2(context.Background(), document, plan, draft)
	if err != nil || !created || environment.Plan.Fingerprint != plan.Fingerprint || environment.Manifest.Fingerprint != manifestFingerprint {
		t.Fatalf("put created=%v environment plan=%v manifest=%v err=%v", created, environment.Plan.Fingerprint, environment.Manifest.Fingerprint, err)
	}
	if environment.Document.ID != document.ID {
		t.Fatalf("returned document = %+v", environment.Document)
	}
	// Caller mutation of the returned environment must not reach storage.
	environment.Document.Timelines[0].Tracks[0].Clips[0].ID = "mutated"
	environment.Plan.DocumentID = "mutated"
	environment.Manifest.CompilerBuild = digest('f')

	resolved, err := store.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint)
	if err != nil || resolved.Document.Timelines[0].Tracks[0].Clips[0].ID == "mutated" ||
		resolved.Plan.DocumentID == "mutated" || resolved.Manifest.CompilerBuild == digest('f') {
		t.Fatalf("resolve ownership document/plan/manifest mutated err=%v", err)
	}
	// Mutation of a resolved value must not alias the next resolve.
	resolved.Document.Assets[0].ID = "mutated-again"
	again, err := store.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint)
	if err != nil || again.Document.Assets[0].ID == "mutated-again" {
		t.Fatalf("second resolve aliases first err=%v", err)
	}
	if _, created, err := store.PutV2(context.Background(), document, plan, draft); err != nil || created {
		t.Fatalf("idempotent put created=%v err=%v", created, err)
	}
	// Reopen simulates a daemon restart: the persisted environment must reconstruct.
	reopened, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	reconstructed, err := reopened.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint)
	if err != nil || reconstructed.Plan.Fingerprint != plan.Fingerprint || reconstructed.Manifest.Fingerprint != manifestFingerprint ||
		reconstructed.Plan.DocumentHash != reconstructed.Manifest.DocumentHash {
		t.Fatalf("reopen resolve plan=%v manifest=%v err=%v", reconstructed.Plan.Fingerprint, reconstructed.Manifest.Fingerprint, err)
	}
	if _, err := store.ResolveV2(context.Background(), digest('9'), digest('8')); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing resolve = %v", err)
	}
}

func TestPutV2RejectsUnboundAndMismatchedEnvironments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, plan, draft := environmentV2Fixture(t)

	// A plan resolved for a different range is not the S1 resolve of this document+request.
	otherRange := documentv2.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: videoedit.RationalTime{Value: 1, Rate: 1}}
	diverted, diagnostics := renderplanv2.Resolve(context.Background(), document, otherRange, plan.Target)
	if documentv2.HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diverted resolve diagnostics: %+v", diagnostics)
	}
	if _, _, err := store.PutV2(context.Background(), document, diverted, draft); err == nil {
		t.Fatal("plan from a different requested range was admitted")
	}

	// A manifest whose plan binding is wrong must be rejected.
	detached := draft
	detached.PlanFingerprint = digest('e')
	if _, _, err := store.PutV2(context.Background(), document, plan, detached); err == nil {
		t.Fatal("manifest bound to another plan was admitted")
	}

	// A plan resolved from a different document must be rejected.
	mutatedDocument := document
	mutatedDocument.Revision++
	if _, _, err := store.PutV2(context.Background(), mutatedDocument, plan, draft); err == nil {
		t.Fatal("plan from another document was admitted")
	}
}

func TestCrossStoreConcurrentIdenticalPutV2(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	first, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, plan, draft := environmentV2Fixture(t)
	stores := []*Store{first, second}
	start := make(chan struct{})
	results := make(chan bool, 32)
	var wait sync.WaitGroup
	for index := 0; index < cap(results); index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, created, err := stores[index%2].PutV2(context.Background(), document, plan, draft)
			if err != nil {
				t.Errorf("concurrent put: %v", err)
				return
			}
			results <- created
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	creators := 0
	for created := range results {
		if created {
			creators++
		}
	}
	if creators != 1 {
		t.Fatalf("concurrent identical puts created %d entries", creators)
	}
	manifestFingerprint := frozenV2Fingerprint(t, document, plan, draft)
	if _, err := first.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint); err != nil {
		t.Fatalf("resolve after concurrent puts: %v", err)
	}
}

func TestV2CorruptRecordsFailClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, plan, draft := environmentV2Fixture(t)
	if _, _, err := store.PutV2(context.Background(), document, plan, draft); err != nil {
		t.Fatal(err)
	}
	manifestFingerprint := frozenV2Fingerprint(t, document, plan, draft)
	name, err := keyName(Key{PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifestFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	entryPath := filepath.Join(store.entriesRootV2, name[:2], name)
	raw, err := os.ReadFile(filepath.Join(entryPath, recordName))
	if err != nil {
		t.Fatal(err)
	}

	// A canonical v1 record must not decode as v2 and vice versa: profiles are disjoint.
	v1Plan, v1Manifest := testEnvironment(t, 2)
	v1Environment, v1Key, v1Raw, err := freezeEnvironment(v1Plan, v1Manifest, store.maxRecordBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeRecordV2(context.Background(), v1Raw, store.maxRecordBytesV2); err == nil {
		t.Fatal("v1 record decoded as v2")
	}
	if _, _, err := decodeRecord(v1Raw, store.maxRecordBytes); err != nil || v1Environment.Plan.Fingerprint != v1Key.PlanFingerprint {
		t.Fatal("v1 self-check failed")
	}
	if _, _, err := decodeRecord(raw, store.maxRecordBytes); err == nil {
		t.Fatal("v2 record decoded as v1")
	}

	// Flip one hex digit of the stored plan fingerprint: identity corruption must fail closed.
	flipped := append([]byte(nil), raw...)
	if index := strings.Index(string(flipped), plan.Fingerprint); index < 0 {
		t.Fatal("plan fingerprint absent from record")
	} else {
		flipped[index] = '0'
	}
	corruptions := map[string][]byte{
		"truncated":          raw[:len(raw)-1],
		"non-canonical":      append(append([]byte(nil), raw...), ' '),
		"flipped plan print": flipped,
		"oversize":           append(append([]byte(nil), raw...), make([]byte, 1024)...),
	}
	for name, corrupted := range corruptions {
		if _, _, err := decodeRecordV2(context.Background(), corrupted, store.maxRecordBytesV2); err == nil {
			t.Fatalf("%s record decoded successfully", name)
		}
	}
	if _, _, err := decodeRecordV2(context.Background(), raw, 1024); err == nil {
		t.Fatal("record beyond limit decoded")
	}

	// A mismatched completion sentinel must fail closed. Published files are read-only, so the
	// substitution removes the sentinel first.
	if err := os.Remove(filepath.Join(entryPath, completeName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entryPath, completeName), []byte(completionV2+"\nsha256:"+strings.Repeat("0", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint); err == nil {
		t.Fatal("mismatched completion sentinel resolved")
	}
}

func TestV2SymlinkAndCancellationFailClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "prepared")
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	document, plan, draft := environmentV2Fixture(t)
	if _, _, err := store.PutV2(context.Background(), document, plan, draft); err != nil {
		t.Fatal(err)
	}
	manifestFingerprint := frozenV2Fingerprint(t, document, plan, draft)
	name, err := keyName(Key{PlanFingerprint: plan.Fingerprint, ExecutionManifestFingerprint: manifestFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	entryPath := filepath.Join(store.entriesRootV2, name[:2], name)

	// Symlink substitution of the published entry must fail closed.
	decoy := t.TempDir()
	if err := os.Rename(entryPath, filepath.Join(decoy, "entry")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(decoy, "entry"), entryPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint); err == nil {
		t.Fatal("symlinked entry resolved")
	}
	if err := os.Remove(entryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(decoy, "entry"), entryPath); err != nil {
		t.Fatal(err)
	}

	// Cancellation before and after admission must not publish or panic.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.PutV2(canceled, document, plan, draft); err == nil {
		t.Fatal("canceled put succeeded")
	}
	if _, err := store.ResolveV2(canceled, plan.Fingerprint, manifestFingerprint); err == nil {
		t.Fatal("canceled resolve succeeded")
	}
	if value, err := store.ResolveV2(context.Background(), plan.Fingerprint, manifestFingerprint); err != nil || value.Document.ID != document.ID {
		t.Fatalf("resolve after cancellation recovery = %+v err=%v", value, err)
	}
}

func TestV2CanonicalRecordFixture(t *testing.T) {
	document, plan, draft := environmentV2Fixture(t)
	_, key, raw, err := freezeEnvironmentV2(context.Background(), document, plan, draft, DefaultMaxRecordBytesV2)
	if err != nil {
		t.Fatal(err)
	}
	if key.PlanFingerprint != plan.Fingerprint {
		t.Fatalf("key plan fingerprint = %v", key.PlanFingerprint)
	}
	// The canonical fixture is committed with its pinned identity so any change to the v2
	// record encoding is a deliberate, reviewed act.
	fixturePath := filepath.Join("testdata", "prepared-environment-v2.json")
	// Pinned canonical fixture identity: any change to the v2 record encoding or to the shared
	// conformance inputs is a deliberate, reviewed act.
	const pinnedFixtureSHA256 = "97d820d1238432e86a2a0720954d6ba0c162eb2a03251f9222792313fd4d459e"
	computed := sha256.Sum256(raw)
	if hex.EncodeToString(computed[:]) != pinnedFixtureSHA256 {
		t.Fatalf("record sha256 %s does not match the pinned fixture %s", hex.EncodeToString(computed[:]), pinnedFixtureSHA256)
	}
	if os.Getenv("CUTLY_REGENERATE_PREPARED_V2_FIXTURE") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixturePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("regenerated fixture %s sha256:%s — pin this hash and rerun", fixturePath, hex.EncodeToString(computed[:]))
	}
	pinned, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("fixture missing (run with CUTLY_REGENERATE_PREPARED_V2_FIXTURE=1 once): %v", err)
	}
	if !strings.Contains(string(pinned), plan.Fingerprint) {
		t.Fatalf("fixture does not bind the current conformance document plan")
	}
	if _, _, err := decodeRecordV2(context.Background(), pinned, DefaultMaxRecordBytesV2); err != nil {
		t.Fatalf("pinned fixture no longer decodes: %v", err)
	}
}
