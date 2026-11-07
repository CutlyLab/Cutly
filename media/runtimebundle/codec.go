package runtimebundle

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/CutlyLab/Cutly/internal/wirejson"
	"github.com/CutlyLab/Cutly/protocol/canonicaljson"
)

// SignManifest is a release-tooling helper. The caller supplies only payload
// fields; pre-existing signing metadata is rejected so a stale fingerprint or
// signature cannot be accidentally preserved. Production resolution requires
// only the public key.
func SignManifest(payload Manifest, keyID string, privateKey ed25519.PrivateKey) (Manifest, error) {
	if payload.BundleFingerprint != "" || payload.KeyID != "" || payload.SignatureAlgorithm != "" || payload.Signature != "" ||
		!identityPattern.MatchString(keyID) || len(privateKey) != ed25519.PrivateKeySize {
		return Manifest{}, fmt.Errorf("%w: signing input", ErrConfiguration)
	}
	manifest := payload
	manifest.Files = cloneFiles(payload.Files)
	manifest.KeyID = keyID
	manifest.SignatureAlgorithm = SignatureAlgorithmV1
	manifest.BundleFingerprint = "sha256:" + strings.Repeat("0", sha256.Size*2)
	manifest.Signature = strings.Repeat("0", ed25519.SignatureSize*2)
	if err := validateManifestShape(manifest, DefaultMaxFiles, DefaultMaxTotalFileBytes); err != nil {
		return Manifest{}, err
	}
	fingerprint, err := payloadFingerprint(manifest)
	if err != nil {
		return Manifest{}, err
	}
	manifest.BundleFingerprint = fingerprint
	preimage, err := signaturePreimage(manifest)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Signature = hex.EncodeToString(ed25519.Sign(privateKey, preimage))
	return freezeManifest(manifest)
}

// DecodeManifest is a strict wire decoder. Cryptographic and filesystem
// verification belongs to Resolver.Resolve.
func DecodeManifest(raw []byte) (Manifest, error) {
	if len(raw) == 0 || int64(len(raw)) > DefaultMaxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: encoded size", ErrManifest)
	}
	var manifest Manifest
	if err := wirejson.Decode(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrManifest, err)
	}
	if err := validateManifestShape(manifest, DefaultMaxFiles, DefaultMaxTotalFileBytes); err != nil {
		return Manifest{}, err
	}
	canonical, err := canonicaljson.Marshal(manifest)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: canonical form: %v", ErrManifest, err)
	}
	if !bytes.Equal(raw, canonical) {
		return Manifest{}, fmt.Errorf("%w: manifest must use canonical JSON", ErrManifest)
	}
	var frozen Manifest
	if err := wirejson.Decode(canonical, &frozen); err != nil {
		return Manifest{}, fmt.Errorf("%w: freeze: %v", ErrManifest, err)
	}
	return frozen, nil
}

// CanonicalManifest validates and returns the unique wire representation.
func CanonicalManifest(manifest Manifest) ([]byte, error) {
	if err := validateManifestShape(manifest, DefaultMaxFiles, DefaultMaxTotalFileBytes); err != nil {
		return nil, err
	}
	canonical, err := canonicaljson.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical form: %v", ErrManifest, err)
	}
	return canonical, nil
}

func validateManifestShape(manifest Manifest, maxFiles int, maxTotalBytes int64) error {
	if manifest.SchemaVersion != SchemaVersion || manifest.Profile != ManifestProfileV1 ||
		manifest.CanonicalJSONProfile != canonicaljson.Profile ||
		!identityPattern.MatchString(manifest.Platform) || !identityPattern.MatchString(manifest.Architecture) ||
		!validRelativePath(manifest.FFmpegPath) || !validRelativePath(manifest.FFprobePath) ||
		manifest.FFmpegPath == manifest.FFprobePath || len(manifest.Files) < 2 || len(manifest.Files) > maxFiles ||
		!canonicalSHA256(manifest.BundleFingerprint) || !identityPattern.MatchString(manifest.KeyID) ||
		manifest.SignatureAlgorithm != SignatureAlgorithmV1 || !canonicalSignature(manifest.Signature) {
		return fmt.Errorf("%w: envelope", ErrManifest)
	}
	var total int64
	ffmpegFound := false
	ffprobeFound := false
	for index, file := range manifest.Files {
		if !validRelativePath(file.Path) || file.Path == ManifestFileName || file.SizeBytes <= 0 ||
			file.Mode > 0o777 || !canonicalSHA256(file.ContentHash) || len(file.Tools) == 0 || len(file.Tools) > 2 ||
			(index > 0 && file.Path <= manifest.Files[index-1].Path) {
			return fmt.Errorf("%w: file %d", ErrManifest, index)
		}
		if manifest.Platform != "windows" && file.Mode&0o022 != 0 {
			return fmt.Errorf("%w: writable file %d", ErrManifest, index)
		}
		if file.SizeBytes > maxTotalBytes-total {
			return fmt.Errorf("%w: declared file bytes", ErrLimit)
		}
		total += file.SizeBytes
		for toolIndex, tool := range file.Tools {
			if tool != ToolFFmpeg && tool != ToolFFprobe || toolIndex > 0 && tool <= file.Tools[toolIndex-1] {
				return fmt.Errorf("%w: file %d tools", ErrManifest, index)
			}
		}
		if file.Path == manifest.FFmpegPath {
			ffmpegFound = includesTool(file.Tools, ToolFFmpeg)
			if manifest.Platform != "windows" && file.Mode&0o111 == 0 {
				return fmt.Errorf("%w: ffmpeg is not executable", ErrManifest)
			}
		}
		if file.Path == manifest.FFprobePath {
			ffprobeFound = includesTool(file.Tools, ToolFFprobe)
			if manifest.Platform != "windows" && file.Mode&0o111 == 0 {
				return fmt.Errorf("%w: ffprobe is not executable", ErrManifest)
			}
		}
	}
	if !ffmpegFound || !ffprobeFound {
		return fmt.Errorf("%w: executable membership", ErrManifest)
	}
	if err := admitPathNodes(manifest.Files, ManifestFileName, DefaultMaxClosureNodes); err != nil {
		return err
	}
	return nil
}

func admitPathNodes(files []FileEntry, manifestName string, maximum int) error {
	directories := map[string]struct{}{".": {}}
	for _, file := range files {
		addParentDirectories(directories, file.Path)
		if len(files)+len(directories)+1 > maximum {
			return fmt.Errorf("%w: closure path nodes", ErrLimit)
		}
	}
	addParentDirectories(directories, manifestName)
	if len(files)+len(directories)+1 > maximum {
		return fmt.Errorf("%w: closure path nodes", ErrLimit)
	}
	return nil
}

func validRelativePath(value string) bool {
	if value == "" || len(value) > MaxRelativePathBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) ||
		strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || path.IsAbs(value) ||
		path.Clean(value) != value || value == "." {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '/' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-' || character == '+' {
			continue
		}
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func canonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	hexValue := strings.TrimPrefix(value, "sha256:")
	if hexValue != strings.ToLower(hexValue) {
		return false
	}
	_, err := hex.DecodeString(hexValue)
	return err == nil
}

func canonicalSignature(value string) bool {
	if len(value) != ed25519.SignatureSize*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func manifestPayloadOf(manifest Manifest) manifestPayload {
	return manifestPayload{
		SchemaVersion: manifest.SchemaVersion, Profile: manifest.Profile,
		CanonicalJSONProfile: manifest.CanonicalJSONProfile, Platform: manifest.Platform,
		Architecture: manifest.Architecture, FFmpegPath: manifest.FFmpegPath,
		FFprobePath: manifest.FFprobePath, Files: cloneFiles(manifest.Files),
	}
}

func unsignedManifestOf(manifest Manifest) unsignedManifest {
	return unsignedManifest{
		SchemaVersion: manifest.SchemaVersion, Profile: manifest.Profile,
		CanonicalJSONProfile: manifest.CanonicalJSONProfile, Platform: manifest.Platform,
		Architecture: manifest.Architecture, FFmpegPath: manifest.FFmpegPath,
		FFprobePath: manifest.FFprobePath, Files: cloneFiles(manifest.Files),
		BundleFingerprint: manifest.BundleFingerprint, KeyID: manifest.KeyID,
		SignatureAlgorithm: manifest.SignatureAlgorithm,
	}
}

func payloadFingerprint(manifest Manifest) (string, error) {
	canonical, err := canonicaljson.Marshal(manifestPayloadOf(manifest))
	if err != nil {
		return "", fmt.Errorf("%w: payload canonical form: %v", ErrManifest, err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func signaturePreimage(manifest Manifest) ([]byte, error) {
	canonical, err := canonicaljson.Marshal(unsignedManifestOf(manifest))
	if err != nil {
		return nil, fmt.Errorf("%w: signature canonical form: %v", ErrManifest, err)
	}
	preimage := make([]byte, 0, len(SignatureDomainV1)+1+len(canonical))
	preimage = append(preimage, SignatureDomainV1...)
	preimage = append(preimage, 0)
	preimage = append(preimage, canonical...)
	return preimage, nil
}

func closureFingerprint(manifest Manifest, tool string) (string, error) {
	files := make([]FileEntry, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		if includesTool(file.Tools, tool) {
			files = append(files, cloneFile(file))
		}
	}
	executable := manifest.FFmpegPath
	if tool == ToolFFprobe {
		executable = manifest.FFprobePath
	}
	identity := closureIdentity{
		SchemaVersion: SchemaVersion, Profile: ClosureProfileV1,
		CanonicalJSONProfile: canonicaljson.Profile, Platform: manifest.Platform,
		Architecture: manifest.Architecture, Tool: tool, ExecutablePath: executable, Files: files,
	}
	canonical, err := canonicaljson.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("%w: closure identity: %v", ErrManifest, err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func includesTool(tools []string, wanted string) bool {
	for _, tool := range tools {
		if tool == wanted {
			return true
		}
	}
	return false
}

func cloneFile(file FileEntry) FileEntry {
	file.Tools = append([]string(nil), file.Tools...)
	return file
}

func cloneFiles(files []FileEntry) []FileEntry {
	result := make([]FileEntry, len(files))
	for index, file := range files {
		result[index] = cloneFile(file)
	}
	return result
}
