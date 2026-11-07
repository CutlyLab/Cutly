package key

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

var canonicalMagic = []byte("cutly-cache-stage-key\x00")

// New validates input, hashes its frozen binary preimage, and returns a self-describing key.
func New(input Input) (Key, error) {
	canonical, err := CanonicalBinary(input)
	if err != nil {
		return Key{}, err
	}
	digest := sha256.Sum256(canonical)
	return Key{
		SchemaVersion: SchemaVersion,
		Profile:       ProfileV1,
		Namespace:     input.Namespace,
		ArtifactKind:  input.ArtifactKind,
		Digest:        "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

// Verify recomputes a key from its complete preimage.
func Verify(input Input, value Key) error {
	if err := ValidateKey(value); err != nil {
		return err
	}
	want, err := New(input)
	if err != nil {
		return err
	}
	if value != want {
		return fmt.Errorf("%w: got %+v, want %+v", ErrDigestMismatch, value, want)
	}
	return nil
}

// CanonicalBinary returns the portable v1 hash preimage. It is independent of JSON whitespace,
// object ordering, Go map iteration, host endianness, JobID, and artifact location.
func CanonicalBinary(input Input) ([]byte, error) {
	if err := Validate(input); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.Grow(512 + len(input.Inputs)*128)
	output.Write(canonicalMagic)
	writeUint32(&output, uint32(SchemaVersion))
	if err := writeString(&output, ProfileV1); err != nil {
		return nil, err
	}
	for _, value := range []string{
		input.Namespace,
		input.ArtifactKind,
		input.PlanFingerprint,
		input.ExecutionManifestFingerprint,
		input.StageProfile,
	} {
		if err := writeString(&output, value); err != nil {
			return nil, err
		}
	}
	writeUint32(&output, uint32(len(input.Inputs)))
	for _, item := range input.Inputs {
		if err := writeString(&output, item.Role); err != nil {
			return nil, err
		}
		writeUint32(&output, item.Ordinal)
		if err := writeString(&output, item.ContentHash); err != nil {
			return nil, err
		}
	}
	writeRange(&output, input.FrameRange)
	writeRange(&output, input.SampleRange)
	if err := writeString(&output, input.OutputProfile); err != nil {
		return nil, err
	}
	if output.Len() > MaxCanonicalBytes {
		return nil, fmt.Errorf("%w: canonical bytes %d exceed %d", ErrInvalid, output.Len(), MaxCanonicalBytes)
	}
	return output.Bytes(), nil
}

func writeString(output *bytes.Buffer, value string) error {
	if uint64(len(value)) > uint64(^uint32(0)) {
		return fmt.Errorf("%w: canonical string is too large", ErrInvalid)
	}
	writeUint32(output, uint32(len(value)))
	output.WriteString(value)
	return nil
}

func writeRange(output *bytes.Buffer, value *IndexRange) {
	if value == nil {
		output.WriteByte(0)
		return
	}
	output.WriteByte(1)
	writeUint64(output, uint64(value.Start))
	writeUint64(output, uint64(value.Count))
}

func writeUint32(output *bytes.Buffer, value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	output.Write(encoded[:])
}

func writeUint64(output *bytes.Buffer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	output.Write(encoded[:])
}
