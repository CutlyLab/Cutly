package documentv2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/CutlyLab/Cutly/core/videoedit"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

var ErrDocumentInvalid = errors.New("documentv2: document is invalid")

// Decode accepts exactly Document v2. Older documents must enter through MigrateV1JSON so a
// caller can never accidentally treat migration as ordinary decoding.
func Decode(raw []byte) (Document, error) {
	if err := validateJSONEncoding(raw); err != nil {
		return Document{}, fmt.Errorf("%w: decode: %v", ErrDocumentInvalid, err)
	}
	var document Document
	if err := wirejson.Decode(raw, &document); err != nil {
		return Document{}, fmt.Errorf("%w: decode: %v", ErrDocumentInvalid, err)
	}
	diagnostics := Validate(document)
	if HasErrorDiagnostics(diagnostics) {
		first := diagnostics[0]
		return Document{}, fmt.Errorf("%w: %s at %s: %s", ErrDocumentInvalid, first.Code, first.Path, first.Message)
	}
	return document, nil
}

func CanonicalJSON(document Document) ([]byte, error) {
	if diagnostics := Validate(document); HasErrorDiagnostics(diagnostics) {
		first := diagnostics[0]
		return nil, fmt.Errorf("%w: %s at %s: %s", ErrDocumentInvalid, first.Code, first.Path, first.Message)
	}
	return videoedit.CanonicalJSON(document)
}

// DocumentHash returns the content-addressed identity of the complete canonical v2 document.
func DocumentHash(document Document) (string, error) {
	canonical, err := CanonicalJSON(document)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// Hash is a concise alias retained for callers that do not need to disambiguate other artifact
// fingerprints in the same package.
func Hash(document Document) (string, error) { return DocumentHash(document) }
