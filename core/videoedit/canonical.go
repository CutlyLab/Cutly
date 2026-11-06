package videoedit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/CutlyLab/Cutly/internal/wirejson"
)

// CanonicalJSON normalizes whitespace and object key ordering, including JSON stored in RawMessage
// extension fields. It intentionally keeps JSON number spelling; model-owned numeric values already
// use integer types, while extension schemas remain responsible for their own numeric semantics.
func CanonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("videoedit: marshal canonical JSON: %w", err)
	}
	if err := wirejson.Validate(raw); err != nil {
		return nil, fmt.Errorf("videoedit: validate canonical JSON: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("videoedit: decode canonical JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("videoedit: encode canonical JSON: %w", err)
	}
	return canonical, nil
}

// DocumentHash returns the content-addressed identity of the complete canonical document, including
// schema version and revision.
func DocumentHash(document Document) (string, error) {
	return canonicalHash(document)
}

func canonicalHash(value any) (string, error) {
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("videoedit: canonical JSON contains multiple values")
		}
		return fmt.Errorf("videoedit: trailing canonical JSON: %w", err)
	}
	return nil
}
