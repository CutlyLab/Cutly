package videodecode

import "errors"

var (
	ErrInvalid          = errors.New("videodecode: invalid input")
	ErrIndexInvalid     = errors.New("videodecode: frame index is invalid")
	ErrIndexMismatch    = errors.New("videodecode: frame index does not match probe manifest")
	ErrFrameUnavailable = errors.New("videodecode: source time is not covered by a frame")
	ErrUnsupported      = errors.New("videodecode: media semantics are unsupported")
	ErrExecutable       = errors.New("videodecode: FFmpeg executable is invalid")
	ErrContentChanged   = errors.New("videodecode: content changed")
	ErrContentHash      = errors.New("videodecode: content hash mismatch")
	ErrResourceLimit    = errors.New("videodecode: resource limit exceeded")
	ErrCommand          = errors.New("videodecode: FFmpeg command failed")
	ErrOutput           = errors.New("videodecode: FFmpeg output is invalid")
	ErrClosed           = errors.New("videodecode: source is closed")
	ErrCleanup          = errors.New("videodecode: cleanup failed")
)
