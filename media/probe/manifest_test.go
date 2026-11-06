package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestDecodeValidManifestAndFingerprint(t *testing.T) {
	raw := fixture(t, "probe-manifest-v1.valid.json")
	manifest, diagnostics, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if HasErrorDiagnostics(diagnostics) {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
	if manifest.SchemaVersion != SchemaVersion || len(manifest.Streams) != 2 {
		t.Fatalf("manifest = %+v", manifest)
	}
	if manifest.Streams[0].Video == nil || manifest.Streams[0].Video.FrameRateMode != FrameRateCFR {
		t.Fatalf("video stream = %+v", manifest.Streams[0])
	}
	if manifest.Streams[1].Audio == nil || manifest.Streams[1].Audio.EncoderDelaySamples != 1024 {
		t.Fatalf("audio stream = %+v", manifest.Streams[1])
	}

	first, err := Fingerprint(manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Fingerprint(manifest)
	if err != nil || first != second || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("fingerprints = %q %q, err=%v", first, second, err)
	}

	changed := manifest
	changed.Probe.OptionsFingerprint = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	changedFingerprint, err := Fingerprint(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedFingerprint == first {
		t.Fatal("probe options did not affect manifest fingerprint")
	}
}

func TestDecodeRejectsUnknownAndTrailingJSON(t *testing.T) {
	raw := bytes.TrimSpace(fixture(t, "probe-manifest-v1.valid.json"))
	withUnknown := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)
	if _, _, err := Decode(withUnknown); !errors.Is(err, ErrDecode) {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, _, err := Decode(append(raw, []byte(` {}`)...)); !errors.Is(err, ErrDecode) {
		t.Fatalf("trailing value error = %v", err)
	}
	duplicateRoot := append([]byte(`{"schema_version":999,`), raw[1:]...)
	if _, _, err := Decode(duplicateRoot); !errors.Is(err, ErrDecode) {
		t.Fatalf("duplicate field error = %v", err)
	}
	caseAlias := bytes.Replace(raw, []byte(`"schema_version"`), []byte(`"SCHEMA_VERSION"`), 1)
	if _, _, err := Decode(caseAlias); !errors.Is(err, ErrDecode) {
		t.Fatalf("case alias error = %v", err)
	}
	var missingRequired map[string]json.RawMessage
	if err := json.Unmarshal(raw, &missingRequired); err != nil {
		t.Fatal(err)
	}
	var container map[string]json.RawMessage
	if err := json.Unmarshal(missingRequired["container"], &container); err != nil {
		t.Fatal(err)
	}
	delete(container, "seekable")
	missingRequired["container"], _ = json.Marshal(container)
	missingRaw, _ := json.Marshal(missingRequired)
	if _, _, err := Decode(missingRaw); !errors.Is(err, ErrDecode) {
		t.Fatalf("missing required zero field error = %v", err)
	}
}

func TestInvalidFixtureProducesStableDiagnostics(t *testing.T) {
	_, diagnostics, err := Decode(fixture(t, "probe-manifest-v1.invalid.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantCodes := []string{
		CodeSchemaUnsupported,
		CodeResourceInvalid,
		CodeFingerprintInvalid,
		CodeContainerInvalid,
		CodeDurationInvalid,
		CodeStreamInvalid,
		CodeTimeBaseInvalid,
		CodePTSInvalid,
	}
	for _, code := range wantCodes {
		if !hasCode(diagnostics, code) {
			t.Errorf("missing %s: %+v", code, diagnostics)
		}
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Path == "" || diagnostic.Message == "" || diagnostic.Severity != SeverityError {
			t.Fatalf("non-actionable diagnostic: %+v", diagnostic)
		}
	}
}

func TestValidateVideoAndKeyframeContracts(t *testing.T) {
	manifest := validManifest(t)
	video := manifest.Streams[0].Video
	video.Width = 0
	video.RotationDegrees = 270
	video.SampleAspectRatio = &Ratio{Numerator: 0, Denominator: 1}
	video.PixelFormat = ""
	video.FrameRateMode = "sometimes"
	video.AverageFrameRate = rationalPointer(videoedit.RationalTime{Value: 0, Rate: 1})
	video.Keyframes = KeyframeSummary{Complete: true, Count: 2, FirstPTS: int64Pointer(10), LastPTS: int64Pointer(5)}
	video.Color.Range = "studio-ish"
	video.HDR = &HDRMetadata{Format: "", MaxContentLightLevel: -1, MaxFrameAverageLightLevel: -1}

	diagnostics := Validate(manifest)
	for _, code := range []string{CodeVideoInvalid, CodeKeyframesInvalid, CodeColorInvalid, CodeHDRInvalid} {
		if !hasCode(diagnostics, code) {
			t.Errorf("missing %s: %+v", code, diagnostics)
		}
	}
}

func TestValidateAudioShapeAndStreamOrdering(t *testing.T) {
	manifest := validManifest(t)
	manifest.Streams[1].Index = 0
	manifest.Streams[1].Video = manifest.Streams[0].Video
	audio := manifest.Streams[1].Audio
	audio.SampleRate = 0
	audio.Channels = 0
	audio.ChannelLayout = ""
	audio.SampleFormat = ""
	audio.BitsPerSample = -1
	audio.EncoderDelaySamples = -1
	audio.PaddingSamples = -1

	diagnostics := Validate(manifest)
	if !hasCode(diagnostics, CodeStreamOrderInvalid) || !hasCode(diagnostics, CodeStreamInvalid) {
		t.Fatalf("missing ordering/shape diagnostics: %+v", diagnostics)
	}

	manifest.Streams[1].Video = nil
	diagnostics = Validate(manifest)
	if !hasCode(diagnostics, CodeAudioInvalid) {
		t.Fatalf("missing audio diagnostics: %+v", diagnostics)
	}
}

func TestCompleteKeyframeScanRequiresIndexHash(t *testing.T) {
	manifest := validManifest(t)
	manifest.Streams[0].Video.Keyframes.IndexHash = ""
	diagnostics := Validate(manifest)
	if !hasCodeAt(diagnostics, CodeKeyframesInvalid, "/streams/0/video/keyframes/index_hash") {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
	if _, err := Fingerprint(manifest); err == nil {
		t.Fatal("invalid manifest produced a fingerprint")
	}
}

func TestCanonicalJSONRoundTripDoesNotChangeFingerprint(t *testing.T) {
	manifest := validManifest(t)
	before, err := Fingerprint(manifest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	decoded, diagnostics, err := Decode(raw)
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("decode diagnostics=%+v err=%v", diagnostics, err)
	}
	after, err := Fingerprint(decoded)
	if err != nil || before != after {
		t.Fatalf("fingerprints = %q %q, err=%v", before, after, err)
	}
}

func validManifest(t *testing.T) ProbeManifest {
	t.Helper()
	manifest, diagnostics, err := Decode(fixture(t, "probe-manifest-v1.valid.json"))
	if err != nil || HasErrorDiagnostics(diagnostics) {
		t.Fatalf("valid fixture diagnostics=%+v err=%v", diagnostics, err)
	}
	return manifest
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate testdata")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(source), "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func hasCode(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func hasCodeAt(diagnostics []Diagnostic, code, path string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code && diagnostic.Path == path {
			return true
		}
	}
	return false
}

func int64Pointer(value int64) *int64 { return &value }

func rationalPointer(value videoedit.RationalTime) *videoedit.RationalTime { return &value }
