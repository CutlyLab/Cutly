package proberegistry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"unicode/utf8"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/media/probe"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

func freezeManifest(value probe.ProbeManifest, maximum int) (probe.ProbeManifest, string, []byte, error) {
	if err := validateManifestBudget(value, maximum); err != nil {
		return probe.ProbeManifest{}, "", nil, err
	}
	// Probe fingerprints are defined by videoedit.CanonicalJSON. Keep the
	// persisted bytes in that exact profile instead of using the protocol JSON
	// profile, whose escaping rules intentionally differ for HTML-sensitive
	// runes.
	raw, err := videoedit.CanonicalJSON(value)
	if err != nil {
		return probe.ProbeManifest{}, "", nil, fmt.Errorf("%w: canonical encode: %v", ErrInvalidManifest, err)
	}
	if len(raw) == 0 || len(raw) > maximum {
		return probe.ProbeManifest{}, "", nil, fmt.Errorf("%w: record bytes %d exceed [1,%d]", ErrLimit, len(raw), maximum)
	}
	if err := validateCanonicalBudget(raw, maximum); err != nil {
		return probe.ProbeManifest{}, "", nil, fmt.Errorf("%w: canonical record budget: %v", ErrInvalidManifest, err)
	}
	frozen, diagnostics, err := probe.Decode(raw)
	if err != nil || probe.HasErrorDiagnostics(diagnostics) {
		return probe.ProbeManifest{}, "", nil, errors.Join(
			fmt.Errorf("%w: %s", ErrInvalidManifest, firstDiagnostic(diagnostics)), err,
		)
	}
	if len(frozen.Streams) > MaxStreams {
		return probe.ProbeManifest{}, "", nil, fmt.Errorf("%w: streams %d exceed %d", ErrLimit, len(frozen.Streams), MaxStreams)
	}
	fingerprint := fingerprintRaw(raw)
	official, err := probe.Fingerprint(frozen)
	if err != nil || official != fingerprint {
		return probe.ProbeManifest{}, "", nil, errors.Join(
			fmt.Errorf("%w: probe fingerprint profile differs from canonical record", ErrInvalidManifest), err,
		)
	}
	return frozen, fingerprint, raw, nil
}

func decodeManifest(raw []byte, expectedFingerprint string, maximum int) (probe.ProbeManifest, error) {
	if len(raw) == 0 || len(raw) > maximum {
		return probe.ProbeManifest{}, fmt.Errorf("%w: record bytes %d exceed [1,%d]", ErrCorrupt, len(raw), maximum)
	}
	if err := validateCanonicalBudget(raw, maximum); err != nil {
		return probe.ProbeManifest{}, errors.Join(ErrCorrupt, errors.New("record exceeds canonical JSON limits"), err)
	}
	manifest, diagnostics, err := probe.Decode(raw)
	if err != nil || probe.HasErrorDiagnostics(diagnostics) {
		return probe.ProbeManifest{}, errors.Join(
			fmt.Errorf("%w: %s", ErrCorrupt, firstDiagnostic(diagnostics)), err,
		)
	}
	if len(manifest.Streams) > MaxStreams {
		return probe.ProbeManifest{}, fmt.Errorf("%w: streams %d exceed %d", ErrCorrupt, len(manifest.Streams), MaxStreams)
	}
	actual := fingerprintRaw(raw)
	if expectedFingerprint != "" && actual != expectedFingerprint {
		return probe.ProbeManifest{}, fmt.Errorf("%w: path expects %s, record is %s", ErrCollision, expectedFingerprint, actual)
	}
	reencoded, err := videoedit.CanonicalJSON(manifest)
	if err != nil || !bytes.Equal(reencoded, raw) {
		return probe.ProbeManifest{}, errors.Join(ErrCorrupt, errors.New("record encoding drift"), err)
	}
	official, err := probe.Fingerprint(manifest)
	if err != nil || official != actual {
		return probe.ProbeManifest{}, errors.Join(ErrCorrupt, errors.New("probe fingerprint profile drift"), err)
	}
	return manifest, nil
}

func validateCanonicalBudget(raw []byte, maximum int) error {
	limits := canonicaljson.DefaultLimits
	limits.MaxInputBytes = maximum
	limits.MaxStringBytes = maximum
	limits.MaxDepth = 64
	limits.MaxValues = MaxCanonicalValues
	limits.MaxIntegerDigits = 32
	// CanonicalizeWithLimits supplies the bounded parser. Its output is not the
	// persisted profile: exact canonicality is checked below by re-encoding the
	// typed manifest with videoedit.CanonicalJSON.
	_, err := canonicaljson.CanonicalizeWithLimits(raw, limits)
	return err
}

func validateManifestBudget(value probe.ProbeManifest, maximum int) error {
	if len(value.Streams) > MaxStreams {
		return fmt.Errorf("%w: streams %d exceed %d", ErrLimit, len(value.Streams), MaxStreams)
	}
	// Reserve a conservative fixed structural budget before json.Marshal so a
	// manifest with thousands of empty/invalid streams cannot make configured
	// record bytes meaningless as a work bound.
	const baseBytes = 1024
	const bytesPerStream = 1024
	structural := baseBytes + len(value.Streams)*bytesPerStream
	if structural > maximum {
		return fmt.Errorf("%w: manifest structure exceeds %d bytes", ErrLimit, maximum)
	}
	remaining := maximum - structural
	consume := func(values ...string) error {
		for _, text := range values {
			if len(text) > remaining {
				return fmt.Errorf("%w: manifest strings exceed %d bytes", ErrLimit, maximum)
			}
			cost, valid := jsonStringWork(text)
			if !valid {
				return fmt.Errorf("%w: manifest contains invalid UTF-8", ErrInvalidManifest)
			}
			if cost > remaining {
				return fmt.Errorf("%w: manifest strings exceed %d bytes", ErrLimit, maximum)
			}
			remaining -= cost
		}
		return nil
	}
	if err := consume(
		string(value.Resource.AssetID), value.Resource.ContentHash,
		value.Probe.Implementation, value.Probe.BuildFingerprint,
		value.Probe.OptionsFingerprint, value.Container.FormatName,
	); err != nil {
		return err
	}
	for _, stream := range value.Streams {
		if err := consume(string(stream.Kind), stream.Codec); err != nil {
			return err
		}
		if stream.Video != nil {
			video := stream.Video
			if err := consume(
				video.PixelFormat, string(video.FrameRateMode), video.Keyframes.IndexHash,
				video.Color.Primaries, video.Color.Transfer, video.Color.Matrix,
				string(video.Color.Range), video.Color.ChromaLocation,
			); err != nil {
				return err
			}
			if video.HDR != nil {
				if err := consume(video.HDR.Format, video.HDR.MasteringDisplay); err != nil {
					return err
				}
			}
		}
		if stream.Audio != nil {
			if err := consume(stream.Audio.ChannelLayout, stream.Audio.SampleFormat); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonStringWork(value string) (int, bool) {
	if !utf8.ValidString(value) {
		return 0, false
	}
	cost := 0
	for _, character := range value {
		switch {
		case character < 0x20, character == '<', character == '>', character == '&', character == '\u2028', character == '\u2029':
			cost += 6
		case character == '"', character == '\\':
			cost += 2
		default:
			cost += utf8.RuneLen(character)
		}
	}
	return cost, true
}

func fingerprintRaw(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func fingerprintName(fingerprint string) (string, error) {
	if !cachekey.CanonicalSHA256(fingerprint) {
		return "", fmt.Errorf("%w: canonical SHA-256 fingerprint is required", ErrInvalidManifest)
	}
	return fingerprint[len("sha256:"):], nil
}

func firstDiagnostic(diagnostics []probe.Diagnostic) string {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == probe.SeverityError {
			return fmt.Sprintf("%s at %s", diagnostic.Code, diagnostic.Path)
		}
	}
	return "semantic validation failed"
}
