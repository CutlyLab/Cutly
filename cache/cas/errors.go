package cas

import (
	"errors"
	"fmt"
	"syscall"
)

var (
	ErrConfiguration       = errors.New("cache: invalid configuration")
	ErrInvalidRequest      = errors.New("cache: invalid request")
	ErrMiss                = errors.New("cache: miss")
	ErrCorrupt             = errors.New("cache: entry corrupt")
	ErrRecomputeRequired   = errors.New("cache: recompute required")
	ErrCollision           = errors.New("cache: key collision")
	ErrPublication         = errors.New("cache: publication failed")
	ErrPublicationBusy     = errors.New("cache: cache slot has an active or unknown owner")
	ErrDurabilityUncertain = errors.New("cache: namespace mutation visible but durability sync failed")
	ErrInsufficientDisk    = errors.New("cache: insufficient disk")
	ErrLeaseLost           = errors.New("cache: reader lease lost")
	ErrQuotaUnsatisfied    = errors.New("cache: quota target blocked by active leases")
)

// LookupError differentiates a clean miss from a corrupt entry that was removed from the reader
// namespace. A corrupt lookup satisfies ErrMiss, ErrCorrupt, and ErrRecomputeRequired.
type LookupError struct {
	Key            string
	QuarantinePath string
	Corrupt        bool
	Cause          error
}

func (value *LookupError) Error() string {
	if value == nil {
		return "cache: lookup failed"
	}
	classification := "miss"
	if value.Corrupt {
		classification = "corrupt recompute miss"
	}
	if value.QuarantinePath != "" {
		classification += " quarantined=" + value.QuarantinePath
	}
	if value.Cause != nil {
		return fmt.Sprintf("cache: %s for %s: %v", classification, value.Key, value.Cause)
	}
	return fmt.Sprintf("cache: %s for %s", classification, value.Key)
}

func (value *LookupError) Unwrap() []error {
	if value == nil {
		return nil
	}
	result := []error{ErrMiss}
	if value.Corrupt {
		result = append(result, ErrCorrupt, ErrRecomputeRequired)
	}
	if value.Cause != nil {
		result = append(result, value.Cause)
	}
	return result
}

// DiskError exposes both the semantic preflight result and ENOSPC for callers already classifying
// platform storage errors.
type DiskError struct {
	Available uint64
	Required  uint64
	Cause     error
}

func (value *DiskError) Error() string {
	if value == nil {
		return "cache: disk preflight failed"
	}
	return fmt.Sprintf("cache: disk preflight available=%d required=%d: %v", value.Available, value.Required, value.Cause)
}

func (value *DiskError) Unwrap() []error {
	if value == nil {
		return nil
	}
	result := []error{ErrInsufficientDisk, syscall.ENOSPC}
	if value.Cause != nil {
		result = append(result, value.Cause)
	}
	return result
}

func joinErrors(values ...error) error {
	return errors.Join(values...)
}
