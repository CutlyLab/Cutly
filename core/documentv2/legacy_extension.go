package documentv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/internal/wirejson"
)

const LegacyPropertyCapabilityV1 = "legacy.property.v1"

var legacyPropertyNamespacePattern = regexp.MustCompile(`^legacy\.property\.([0-9a-f]{64})\.v1$`)

func legacyPropertyNamespace(key string) string {
	digest := sha256.Sum256([]byte(key))
	return "legacy.property." + hex.EncodeToString(digest[:]) + ".v1"
}

// validateLegacyPropertyExtension binds the migration-only consumer capability to its owned
// namespace and strict envelope. Without this relation an arbitrary vendor extension could claim
// legacy.property.v1 and pass preflight merely because a backend can consume migrated v1 values.
func (validator *documentValidator) validateLegacyPropertyExtension(extension ExtensionProperty, path string) {
	match := legacyPropertyNamespacePattern.FindStringSubmatch(extension.Namespace)
	claimsNamespace := strings.HasPrefix(extension.Namespace, "legacy.property.")
	claimsCapability := extension.RequiredCapability == LegacyPropertyCapabilityV1
	if !claimsNamespace && !claimsCapability {
		return
	}
	if len(match) != 2 {
		validator.add(CodePropertyInvalid, path+"/namespace", "legacy property extensions require namespace legacy.property.<sha256(legacy_key)>.v1")
		return
	}
	if !claimsCapability {
		validator.add(CodePropertyInvalid, path+"/required_capability", fmt.Sprintf("legacy property namespace requires capability %q", LegacyPropertyCapabilityV1))
		return
	}
	if !extension.AffectsRendering {
		validator.add(CodePropertyInvalid, path+"/affects_rendering", "legacy property extensions are rendering-affecting")
	}
	if validateJSONEncoding(extension.Value) != nil || wirejson.Validate(extension.Value) != nil || bytes.Equal(bytes.TrimSpace(extension.Value), []byte("null")) {
		return // The generic extension validator emits the stable JSON diagnostic.
	}
	var envelope legacyPropertyEnvelope
	if err := wirejson.Decode(extension.Value, &envelope); err != nil {
		validator.add(CodePropertyInvalid, path+"/value", "legacy property value must be the strict legacy_key/value envelope")
		return
	}
	// v1 permits the empty property key, so the migration envelope must preserve it. Its SHA-256
	// namespace is still unique and capability-bound; only invalid UTF-8 is unrepresentable.
	if !utf8.ValidString(envelope.LegacyKey) {
		validator.add(CodePropertyInvalid, path+"/value/legacy_key", "legacy_key must be valid UTF-8")
		return
	}
	// null is also a valid v1 raw property value. The extension's outer Value remains a non-null
	// strict envelope, while this nested member preserves the complete v1 JSON value domain.
	if len(envelope.Value) == 0 || validateJSONEncoding(envelope.Value) != nil || wirejson.Validate(envelope.Value) != nil {
		validator.add(CodePropertyInvalid, path+"/value/value", "legacy property payload must be strict JSON")
		return
	}
	if extension.Namespace != legacyPropertyNamespace(envelope.LegacyKey) {
		validator.add(CodePropertyInvalid, path+"/value/legacy_key", "legacy property namespace digest does not match legacy_key")
	}
}
