package evidence

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

// EvidenceUnit names the composition clock used by a decoded-domain evidence sequence.
type EvidenceUnit string

const (
	EvidenceFrames  EvidenceUnit = "frames"
	EvidenceSamples EvidenceUnit = "samples"
)

// DecodedChunk binds one contiguous composition index range to the SHA-256 of its canonical
// decoded bytes. The byte layout is selected by DecodedSequence.Domain, not by a media container.
type DecodedChunk struct {
	First       int64  `json:"first"`
	Count       int64  `json:"count"`
	ContentHash string `json:"content_hash"`
}

// DecodedSequence is streaming evidence for frames or PCM. Chunks must be ordered, contiguous and
// cover Range exactly. RootHash is a domain-separated hash over the domain, unit, range and ordered
// chunk records, so reordering, omissions and composition-index drift are detectable.
type DecodedSequence struct {
	Domain    string              `json:"domain"`
	Unit      EvidenceUnit        `json:"unit"`
	UnitBytes int64               `json:"unit_bytes"`
	Range     videoedit.TickRange `json:"range"`
	Chunks    []DecodedChunk      `json:"chunks"`
	RootHash  string              `json:"root_hash"`
}

// DigestDecodedBytes hashes one canonical decoded frame block or PCM block.
func DigestDecodedBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ComputeDecodedRoot computes Cutly's v1 binary root contract for an already ordered sequence.
// It does not hash encoded container bytes. Domain must fully identify the decoded byte layout,
// Render execution freezes the exact domain profile and unit byte size before production starts.
func ComputeDecodedRoot(sequence DecodedSequence) (string, error) {
	if err := validateDecodedSequenceShape(sequence); err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("cutly.decoded-sequence.v1\x00"))
	writeRootString(hash, sequence.Domain)
	writeRootString(hash, string(sequence.Unit))
	writeRootInt64(hash, sequence.UnitBytes)
	writeRootInt64(hash, sequence.Range.First)
	writeRootInt64(hash, sequence.Range.Count)
	for _, chunk := range sequence.Chunks {
		writeRootInt64(hash, chunk.First)
		writeRootInt64(hash, chunk.Count)
		raw, _ := hex.DecodeString(strings.TrimPrefix(chunk.ContentHash, "sha256:"))
		_, _ = hash.Write(raw)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func canonicalizeDecodedSequence(sequence *DecodedSequence) {
	if sequence == nil {
		return
	}
	sequence.Chunks = append([]DecodedChunk(nil), sequence.Chunks...)
	sort.Slice(sequence.Chunks, func(i, j int) bool {
		if sequence.Chunks[i].First == sequence.Chunks[j].First {
			if sequence.Chunks[i].Count == sequence.Chunks[j].Count {
				return sequence.Chunks[i].ContentHash < sequence.Chunks[j].ContentHash
			}
			return sequence.Chunks[i].Count < sequence.Chunks[j].Count
		}
		return sequence.Chunks[i].First < sequence.Chunks[j].First
	})
}

func validateDecodedSequence(sequence DecodedSequence) error {
	if err := validateDecodedSequenceShape(sequence); err != nil {
		return err
	}
	want, err := ComputeDecodedRoot(sequence)
	if err != nil {
		return err
	}
	if sequence.RootHash != want {
		return fmt.Errorf("%w: decoded %s root got %q, want %q", ErrEvidenceInvalid, sequence.Unit, sequence.RootHash, want)
	}
	return nil
}

func validateDecodedSequenceShape(sequence DecodedSequence) error {
	if strings.TrimSpace(sequence.Domain) == "" || len(sequence.Domain) > 256 {
		return fmt.Errorf("%w: decoded evidence domain must be 1..256 bytes", ErrEvidenceInvalid)
	}
	if sequence.Unit != EvidenceFrames && sequence.Unit != EvidenceSamples {
		return fmt.Errorf("%w: decoded evidence unit %q", ErrEvidenceInvalid, sequence.Unit)
	}
	if sequence.UnitBytes <= 0 {
		return fmt.Errorf("%w: decoded %s unit_bytes must be positive", ErrEvidenceInvalid, sequence.Unit)
	}
	if err := sequence.Range.Validate(); err != nil || sequence.Range.Count <= 0 {
		return fmt.Errorf("%w: decoded %s range must be valid and non-empty", ErrEvidenceInvalid, sequence.Unit)
	}
	if len(sequence.Chunks) == 0 {
		return fmt.Errorf("%w: decoded %s chunks are required", ErrEvidenceInvalid, sequence.Unit)
	}
	next := sequence.Range.First
	for index, chunk := range sequence.Chunks {
		if chunk.Count <= 0 || !validDigest(chunk.ContentHash) {
			return fmt.Errorf("%w: decoded %s chunk %d is incomplete", ErrEvidenceInvalid, sequence.Unit, index)
		}
		if chunk.First != next {
			return fmt.Errorf("%w: decoded %s chunk %d starts at %d, want %d", ErrEvidenceInvalid, sequence.Unit, index, chunk.First, next)
		}
		rangeValue := videoedit.TickRange{First: chunk.First, Count: chunk.Count}
		end, err := rangeValue.EndExclusive()
		if err != nil {
			return fmt.Errorf("%w: decoded %s chunk %d: %v", ErrEvidenceInvalid, sequence.Unit, index, err)
		}
		next = end
	}
	end, err := sequence.Range.EndExclusive()
	if err != nil {
		return fmt.Errorf("%w: decoded %s range: %v", ErrEvidenceInvalid, sequence.Unit, err)
	}
	if next != end {
		return fmt.Errorf("%w: decoded %s chunks end at %d, want %d", ErrEvidenceInvalid, sequence.Unit, next, end)
	}
	return nil
}

func writeRootString(hash interface{ Write([]byte) (int, error) }, value string) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	_, _ = hash.Write(size[:])
	_, _ = hash.Write([]byte(value))
}

func writeRootInt64(hash interface{ Write([]byte) (int, error) }, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	_, _ = hash.Write(encoded[:])
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
