package reconstructv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	renderapi "github.com/CutlyLab/Cutly/render/api"
	"github.com/CutlyLab/Cutly/render/resourceidentityv2"
)

// diskSnapshot owns a private temporary directory, never the authorized source
// pathname. The decoder makes its own descriptor-bound snapshot before this one
// is closed. Neither object retains a duration-proportional in-memory copy.
type diskSnapshot struct {
	path   string
	root   string
	mu     sync.Mutex
	closed bool
}

func (snapshot *diskSnapshot) Close() error {
	if snapshot == nil {
		return nil
	}
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	if snapshot.closed {
		return nil
	}
	if err := os.RemoveAll(snapshot.root); err != nil {
		return err
	}
	snapshot.closed = true
	return nil
}

// snapshotResourceFile is called after aggregate disk admission, joint
// environment verification and scope admission. scratch is trusted host policy;
// no resource locator or receipt chooses an output path. The finite maximum is
// a logical disk charge, not a filesystem quota or a hard RSS guarantee.
func snapshotResourceFile(ctx context.Context, source ResourceSource, documentHash string, claim renderapi.MaterializedResourceV2, maximum uint64, scratch string) (binding resourceidentityv2.Binding, result *diskSnapshot, err error) {
	var reader io.ReadCloser
	var file *os.File
	var owned *diskSnapshot
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if reader != nil {
			err = errors.Join(err, closeResource(reader))
		}
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		if err != nil {
			if owned != nil {
				err = errors.Join(err, owned.Close())
			}
			binding, result = resourceidentityv2.Binding{}, nil
		}
	}()
	if ctx == nil {
		return binding, nil, ErrContext
	}
	if err = ctx.Err(); err != nil {
		return binding, nil, err
	}
	if nilResourceSource(source) || documentHash == "" || !filepath.IsAbs(scratch) {
		return binding, nil, ErrConfig
	}
	if claim.SizeBytes <= 0 || uint64(claim.SizeBytes) > maximum {
		return binding, nil, ErrLimit
	}
	claim = ownClaim(claim)
	expected := resourceidentityv2.Binding{AssetID: claim.AssetID, Kind: claim.Kind, ContentHash: claim.ContentHash, SizeBytes: claim.SizeBytes}
	binding, err = source.Authorize(ctx, documentHash, ownClaim(claim))
	if err != nil {
		return binding, nil, err
	}
	if err = ctx.Err(); err != nil {
		return binding, nil, err
	}
	if binding != expected {
		return binding, nil, ErrResource
	}
	root, e := os.MkdirTemp(scratch, "cutly-video-snapshot-*")
	if e != nil {
		return binding, nil, e
	}
	owned = &diskSnapshot{root: root, path: filepath.Join(root, "resource")}
	file, err = os.OpenFile(owned.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return binding, nil, err
	}
	reader, err = source.Open(ctx, documentHash, ownClaim(claim), binding)
	if err != nil {
		return binding, nil, err
	}
	if reader == nil {
		return binding, nil, ErrResource
	}
	hash := sha256.New()
	var buffer [32 << 10]byte
	remaining := claim.SizeBytes
	for remaining > 0 {
		if err = ctx.Err(); err != nil {
			return binding, nil, err
		}
		p := buffer[:min(int64(len(buffer)), remaining)]
		n, readErr := reader.Read(p)
		if n < 0 || n > len(p) {
			return binding, nil, ErrResource
		}
		if n > 0 {
			if _, err = file.Write(p[:n]); err != nil {
				return binding, nil, err
			}
			_, _ = hash.Write(p[:n])
			remaining -= int64(n)
		}
		if readErr != nil {
			if readErr == io.EOF && remaining == 0 {
				break
			}
			return binding, nil, errors.Join(ErrResource, readErr)
		}
		if n == 0 {
			return binding, nil, io.ErrNoProgress
		}
	}
	if err = ctx.Err(); err != nil {
		return binding, nil, err
	}
	var extra [1]byte
	if n, readErr := reader.Read(extra[:]); n != 0 || readErr != io.EOF {
		return binding, nil, ErrResource
	}
	closing := reader
	reader = nil
	if err = closeResource(closing); err != nil {
		return binding, nil, err
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != binding.ContentHash {
		return binding, nil, ErrResource
	}
	if err = file.Sync(); err != nil {
		return binding, nil, err
	}
	closingFile := file
	file = nil
	if err = closingFile.Close(); err != nil {
		return binding, nil, err
	}
	if err = os.Chmod(owned.path, 0400); err != nil {
		return binding, nil, err
	}
	if err = ctx.Err(); err != nil {
		return binding, nil, err
	}
	return binding, owned, nil
}
