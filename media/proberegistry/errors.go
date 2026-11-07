package proberegistry

import "errors"

var (
	ErrConfiguration       = errors.New("probe registry: invalid configuration")
	ErrInvalidManifest     = errors.New("probe registry: manifest is invalid")
	ErrNotFound            = errors.New("probe registry: manifest was not found")
	ErrCorrupt             = errors.New("probe registry: stored manifest is corrupt")
	ErrCollision           = errors.New("probe registry: immutable manifest collision")
	ErrLimit               = errors.New("probe registry: resource limit exceeded")
	ErrBusy                = errors.New("probe registry: lock is busy")
	ErrUnsupported         = errors.New("probe registry: required platform primitives are unsupported")
	ErrDurabilityUncertain = errors.New("probe registry: manifest is visible but durability is uncertain")
)
