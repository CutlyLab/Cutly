package cutly

import (
	"context"
	"errors"

	"github.com/CutlyLab/Cutly/project/local"
)

// LocalProjectV1 is the public handle for Cutly's durable, file-backed v1
// project. The handle contains no ambient global state and may be reopened by
// another process after the current operation releases its filesystem lock.
type LocalProjectV1 struct {
	service *local.Service
}

// LocalProjectSnapshotV1 is the immutable project view returned by Inspect.
type LocalProjectSnapshotV1 = local.Snapshot

var (
	// ErrLocalProjectExists reports an attempt to initialize an existing project
	// with different canonical document content. Repeating initialization with
	// the exact same document is idempotent and succeeds.
	ErrLocalProjectExists = local.ErrProjectExists
	// ErrLocalProjectNotFound reports a project directory without a native Cutly
	// project document.
	ErrLocalProjectNotFound = local.ErrProjectNotFound
	// ErrLocalProjectUnavailable reports a nil public project handle.
	ErrLocalProjectUnavailable = errors.New("cutly sdk: local project is unavailable")
)

// CreateLocalProjectV1 initializes dir from a validated v1 document. The
// directory must already exist and must not itself be a symlink. Repeating the
// call with byte-equivalent canonical content is safe and completes recovery;
// different content returns ErrLocalProjectExists.
func CreateLocalProjectV1(ctx context.Context, dir string, document DocumentV1) (*LocalProjectV1, error) {
	service, err := local.Create(ctx, dir, document, local.Options{})
	if err != nil {
		return nil, err
	}
	return &LocalProjectV1{service: service}, nil
}

// OpenLocalProjectV1 opens an existing project root. The project document and
// recovery state are checked when Inspect or another operation begins.
func OpenLocalProjectV1(dir string) (*LocalProjectV1, error) {
	service, err := local.New(dir, local.Options{})
	if err != nil {
		return nil, err
	}
	return &LocalProjectV1{service: service}, nil
}

// Inspect returns an owned snapshot after completing any recoverable local
// commit intent.
func (project *LocalProjectV1) Inspect(ctx context.Context) (LocalProjectSnapshotV1, error) {
	if project == nil || project.service == nil {
		return LocalProjectSnapshotV1{}, ErrLocalProjectUnavailable
	}
	return project.service.Inspect(ctx)
}
