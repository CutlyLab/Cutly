package sfntglyf

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
)

const contextStrideBytes = 32 << 10

func normalizeLimits(value Limits) (Limits, error) {
	defaults := DefaultLimits()
	if value.MaxEncodedBytes == 0 {
		value.MaxEncodedBytes = defaults.MaxEncodedBytes
	}
	if value.MaxTables == 0 {
		value.MaxTables = defaults.MaxTables
	}
	if value.MaxGlyphs == 0 {
		value.MaxGlyphs = defaults.MaxGlyphs
	}
	if value.MaxCmapRecords == 0 {
		value.MaxCmapRecords = defaults.MaxCmapRecords
	}
	if value.MaxCmapGroups == 0 {
		value.MaxCmapGroups = defaults.MaxCmapGroups
	}
	if value.MaxCmapMappings == 0 {
		value.MaxCmapMappings = defaults.MaxCmapMappings
	}
	if value.MaxWorkUnits == 0 {
		value.MaxWorkUnits = defaults.MaxWorkUnits
	}
	if value.MaxWorkingBytes == 0 {
		value.MaxWorkingBytes = defaults.MaxWorkingBytes
	}
	if value.MaxRetainedBytes == 0 {
		value.MaxRetainedBytes = defaults.MaxRetainedBytes
	}
	if value.MaxEncodedBytes > HardMaxEncodedBytes || value.MaxTables > HardMaxTables ||
		value.MaxGlyphs > HardMaxGlyphs || value.MaxCmapRecords > HardMaxCmapRecords ||
		value.MaxCmapGroups > HardMaxCmapGroups || value.MaxCmapMappings > HardMaxCmapMappings ||
		value.MaxWorkUnits > HardMaxWorkUnits || value.MaxWorkingBytes > HardMaxWorkingBytes ||
		value.MaxRetainedBytes > HardMaxRetainedBytes {
		return Limits{}, fontError(CodeLimitsInvalid, "limits", ErrLimitsInvalid)
	}
	return value, nil
}

func contextFailure(ctx context.Context, path string) error {
	if ctx == nil {
		return fontError(CodeContextInvalid, path, ErrContextInvalid)
	}
	if err := ctx.Err(); err != nil {
		return fontError(CodeCanceled, path, errors.Join(ErrCanceled, err))
	}
	return nil
}

func checkedAdd(left, right uint64) (uint64, bool) {
	if left > math.MaxUint64-right {
		return 0, false
	}
	return left + right, true
}

func checkedMultiply(left, right uint64) (uint64, bool) {
	if right != 0 && left > math.MaxUint64/right {
		return 0, false
	}
	return left * right, true
}

func align4(value uint64) (uint64, bool) {
	if value > math.MaxUint64-3 {
		return 0, false
	}
	return (value + 3) &^ 3, true
}

func maxInt() int { return int(^uint(0) >> 1) }

func hashBytes(ctx context.Context, value []byte, path string) (string, error) {
	digest := sha256.New()
	for offset := 0; offset < len(value); offset += contextStrideBytes {
		if err := contextFailure(ctx, path); err != nil {
			return "", err
		}
		end := offset + contextStrideBytes
		if end > len(value) {
			end = len(value)
		}
		_, _ = digest.Write(value[offset:end])
	}
	if err := contextFailure(ctx, path); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

// sfntChecksum sums big-endian uint32 words, conceptually padding value with zero bytes.
// When zeroStart >= 0, bytes [zeroStart,zeroStart+zeroLength) are treated as zero.
func sfntChecksum(ctx context.Context, value []byte, zeroStart, zeroLength int, path string) (uint32, error) {
	var sum uint32
	for offset := 0; offset < len(value); offset += 4 {
		if offset%contextStrideBytes == 0 {
			if err := contextFailure(ctx, path); err != nil {
				return 0, err
			}
		}
		var word [4]byte
		end := offset + 4
		if end > len(value) {
			end = len(value)
		}
		copy(word[:], value[offset:end])
		if zeroStart >= 0 && zeroLength > 0 {
			zeroEnd := zeroStart + zeroLength
			for index := 0; index < 4 && offset+index < len(value); index++ {
				position := offset + index
				if position >= zeroStart && position < zeroEnd {
					word[index] = 0
				}
			}
		}
		sum += binary.BigEndian.Uint32(word[:])
	}
	if err := contextFailure(ctx, path); err != nil {
		return 0, err
	}
	return sum, nil
}

func allocateBytes(length int, path string) (result []byte, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = fontError(CodeAllocationFailed, path, ErrAllocationFailed)
		}
	}()
	return make([]byte, length), nil
}

func allocateMappings(length uint64, path string) (result []cmapMapping, err error) {
	if length > uint64(maxInt()) {
		return nil, fontError(CodeResourceLimit, path, ErrResourceLimit)
	}
	defer func() {
		if recover() != nil {
			result = nil
			err = fontError(CodeAllocationFailed, path, ErrAllocationFailed)
		}
	}()
	return make([]cmapMapping, 0, int(length)), nil
}
