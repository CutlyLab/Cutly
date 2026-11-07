package cas

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	cachekey "github.com/CutlyLab/Cutly/cache/key"
	"github.com/CutlyLab/Cutly/internal/wirejson"
)

const (
	EntryManifestSchemaVersion = 1
	EntryManifestProfileV1     = "cutly.cache.local-entry-manifest.v1"
	CompleteSentinelProfileV1  = "cutly.cache.local-entry-complete.v1"
	maxManifestBytes           = 16 << 10
)

const (
	payloadName     = "payload"
	manifestName    = "manifest.json"
	completeName    = "complete"
	publishedName   = "published"
	leasesName      = "leases"
	gcClaimName     = "gc.claim"
	reservationName = "reservation.json"
)

// Entry describes immutable bytes proven by a successful publish or read.
type Entry struct {
	Key             cachekey.Key `json:"stage_key"`
	PayloadHash     string       `json:"payload_hash"`
	PayloadSize     int64        `json:"payload_size"`
	CreatedUnixNano int64        `json:"created_unix_nano"`
	ChargeBytes     uint64       `json:"charge_bytes"`
}

type entryManifest struct {
	SchemaVersion   int          `json:"schema_version"`
	Profile         string       `json:"profile"`
	StageKey        cachekey.Key `json:"stage_key"`
	PayloadHash     string       `json:"payload_hash"`
	PayloadSize     int64        `json:"payload_size"`
	CreatedUnixNano int64        `json:"created_unix_nano"`
}

func newEntryManifest(value cachekey.Key, hash string, size, created int64) (entryManifest, error) {
	manifest := entryManifest{
		SchemaVersion:   EntryManifestSchemaVersion,
		Profile:         EntryManifestProfileV1,
		StageKey:        value,
		PayloadHash:     hash,
		PayloadSize:     size,
		CreatedUnixNano: created,
	}
	if err := validateEntryManifest(manifest); err != nil {
		return entryManifest{}, err
	}
	return manifest, nil
}

func validateEntryManifest(value entryManifest) error {
	if value.SchemaVersion != EntryManifestSchemaVersion || value.Profile != EntryManifestProfileV1 {
		return fmt.Errorf("%w: entry manifest schema/profile", ErrCorrupt)
	}
	if err := cachekey.ValidateKey(value.StageKey); err != nil {
		return fmt.Errorf("%w: stage key: %w", ErrCorrupt, err)
	}
	if !cachekey.CanonicalSHA256(value.PayloadHash) || value.PayloadSize < 0 || value.CreatedUnixNano <= 0 {
		return fmt.Errorf("%w: entry payload identity or creation time", ErrCorrupt)
	}
	return nil
}

func canonicalEntryManifest(value entryManifest) ([]byte, error) {
	if err := validateEntryManifest(value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode entry manifest: %w", ErrPublication, err)
	}
	if len(encoded) > maxManifestBytes {
		return nil, fmt.Errorf("%w: entry manifest exceeds %d bytes", ErrPublication, maxManifestBytes)
	}
	return encoded, nil
}

func decodeEntryManifest(raw []byte) (entryManifest, error) {
	if len(raw) == 0 || len(raw) > maxManifestBytes {
		return entryManifest{}, fmt.Errorf("%w: manifest size %d", ErrCorrupt, len(raw))
	}
	var value entryManifest
	if err := wirejson.Decode(raw, &value); err != nil {
		return entryManifest{}, fmt.Errorf("%w: strict manifest decode: %w", ErrCorrupt, err)
	}
	if err := validateEntryManifest(value); err != nil {
		return entryManifest{}, err
	}
	canonical, err := canonicalEntryManifest(value)
	if err != nil {
		return entryManifest{}, fmt.Errorf("%w: canonical manifest: %w", ErrCorrupt, err)
	}
	if !bytes.Equal(raw, canonical) {
		return entryManifest{}, fmt.Errorf("%w: manifest bytes are not canonical", ErrCorrupt)
	}
	return value, nil
}

func completeSentinel(manifestRaw []byte) []byte {
	digest := sha256.Sum256(manifestRaw)
	return []byte(CompleteSentinelProfileV1 + "\nsha256:" + hex.EncodeToString(digest[:]) + "\n")
}

func validateCompleteSentinel(raw, manifestRaw []byte) error {
	want := completeSentinel(manifestRaw)
	if !bytes.Equal(raw, want) {
		return fmt.Errorf("%w: completion sentinel does not bind canonical manifest", ErrCorrupt)
	}
	return nil
}

func publicEntry(value entryManifest, manifestBytes, sentinelBytes int) Entry {
	charge := uint64(value.PayloadSize)
	if uint64(manifestBytes) <= ^uint64(0)-charge {
		charge += uint64(manifestBytes)
	} else {
		charge = ^uint64(0)
	}
	if uint64(sentinelBytes) <= ^uint64(0)-charge {
		charge += uint64(sentinelBytes)
	} else {
		charge = ^uint64(0)
	}
	return Entry{
		Key:             value.StageKey,
		PayloadHash:     value.PayloadHash,
		PayloadSize:     value.PayloadSize,
		CreatedUnixNano: value.CreatedUnixNano,
		ChargeBytes:     charge,
	}
}
