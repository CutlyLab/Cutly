package probe

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

var ErrDecode = errors.New("probe: manifest decode failed")

// Decode strictly parses one JSON value. Semantic problems are returned as stable diagnostics so
// agents and preflight can branch on codes without parsing error text.
func Decode(raw []byte) (ProbeManifest, []Diagnostic, error) {
	var manifest ProbeManifest
	if err := wirejson.Decode(raw, &manifest); err != nil {
		return ProbeManifest{}, nil, fmt.Errorf("%w: %v", ErrDecode, err)
	}
	return manifest, Validate(manifest), nil
}

// Fingerprint returns the content identity of a valid canonical manifest. It includes resource,
// probe build/options, and all stream facts, so a parser upgrade cannot hit an old probe cache.
func Fingerprint(manifest ProbeManifest) (string, error) {
	if diagnostics := Validate(manifest); HasErrorDiagnostics(diagnostics) {
		return "", fmt.Errorf("probe: cannot fingerprint invalid manifest: %s at %s", diagnostics[0].Code, diagnostics[0].Path)
	}
	canonical, err := videoedit.CanonicalJSON(manifest)
	if err != nil {
		return "", fmt.Errorf("probe: canonical manifest: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
