package local

import "errors"

var (
	ErrConfiguration       = errors.New("checkpoint: invalid configuration")
	ErrInvalidRequest      = errors.New("checkpoint: invalid request")
	ErrBusy                = errors.New("checkpoint: lock is busy")
	ErrUnsupported         = errors.New("checkpoint: platform locking is unsupported")
	ErrCorrupt             = errors.New("checkpoint: stored value is corrupt")
	ErrCollision           = errors.New("checkpoint: immutable value collision")
	ErrArtifactUnavailable = errors.New("checkpoint: referenced CAS artifact is unavailable")
	ErrArtifactMismatch    = errors.New("checkpoint: CAS artifact does not match checkpoint output")
	ErrDurabilityUncertain = errors.New("checkpoint: publication is visible but durability is uncertain")
	ErrLimit               = errors.New("checkpoint: resource limit exceeded")
)
