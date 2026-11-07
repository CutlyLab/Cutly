package prepared

import "errors"

var (
	ErrConfiguration       = errors.New("prepared: invalid configuration")
	ErrInvalidEnvironment  = errors.New("prepared: environment is invalid")
	ErrNotFound            = errors.New("prepared: environment was not found")
	ErrCorrupt             = errors.New("prepared: stored environment is corrupt")
	ErrCollision           = errors.New("prepared: immutable environment collision")
	ErrLimit               = errors.New("prepared: resource limit exceeded")
	ErrBusy                = errors.New("prepared: lock is busy")
	ErrUnsupported         = errors.New("prepared: required platform durability primitives are unsupported")
	ErrDurabilityUncertain = errors.New("prepared: record is visible but durability is uncertain")
)
