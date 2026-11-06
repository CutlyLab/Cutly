package ffmpegbackend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// stagedInputs owns the only open handles to render input snapshots. Names exist only while the
// FFmpeg argv is compiled; detachPaths removes every media name before the child starts. The
// retained read-only handles are then verified again after the child exits.
type stagedInputs struct {
	root        *os.Root
	directory   string
	resources   []renderapi.MaterializedResource
	files       []*os.File
	names       []string
	removed     []bool
	detached    bool
	filesClosed bool
}

// materializeResources copies the exact verified bytes into a private render-owned directory.
// FFmpeg never reopens the mutable source locator, closing the hash/open/hash TOCTOU window.
func (backend *Backend) materializeResources(
	ctx context.Context,
	resources []renderapi.MaterializedResource,
	root *os.Root,
	directory string,
) (_ *stagedInputs, returnErr error) {
	staged := &stagedInputs{
		root: root, directory: directory,
		resources: append([]renderapi.MaterializedResource(nil), resources...),
		removed:   make([]bool, len(resources)),
	}
	completed := false
	defer func() {
		if !completed {
			returnErr = errors.Join(returnErr, staged.detachPaths(backend.outputHandle))
			returnErr = errors.Join(returnErr, staged.closeFiles())
		}
	}()
	for index, resource := range resources {
		authorized, err := backend.authorizeInput(resource.Locator)
		if err != nil {
			return nil, err
		}
		info, err := authorized.root.handle.Lstat(authorized.relative)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: resource %q is not a non-symlink regular file", ErrContent, resource.AssetID)
		}
		source, err := authorized.root.handle.Open(authorized.relative)
		if err != nil {
			return nil, fmt.Errorf("%w: open resource %q: %v", ErrContent, resource.AssetID, err)
		}
		openedInfo, statErr := source.Stat()
		if statErr != nil || !openedInfo.Mode().IsRegular() {
			_ = source.Close()
			return nil, fmt.Errorf("%w: opened resource %q is not regular", ErrContent, resource.AssetID)
		}
		destinationName := strconv.Itoa(index) + ".media"
		destination, createErr := root.OpenFile(destinationName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if createErr != nil {
			_ = source.Close()
			return nil, fmt.Errorf("%w: stage resource %q: %v", ErrContent, resource.AssetID, createErr)
		}
		hash := sha256.New()
		count, copyErr := copyWithContext(ctx, io.MultiWriter(destination, hash), source)
		closeSourceErr := source.Close()
		syncDestinationErr := destination.Sync()
		if copyErr != nil || closeSourceErr != nil || syncDestinationErr != nil {
			closeDestinationErr := destination.Close()
			_ = root.Remove(destinationName)
			return nil, fmt.Errorf("%w: copy resource %q: %w", ErrContent, resource.AssetID, errors.Join(copyErr, closeSourceErr, syncDestinationErr, closeDestinationErr))
		}
		contentHash := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if count != resource.SizeBytes || contentHash != resource.ContentHash {
			_ = destination.Close()
			_ = root.Remove(destinationName)
			return nil, fmt.Errorf("%w: resource %q got %s/%d, want %s/%d", ErrContent, resource.AssetID, contentHash, count, resource.ContentHash, resource.SizeBytes)
		}
		if err := root.Chmod(destinationName, 0o400); err != nil {
			_ = destination.Close()
			_ = root.Remove(destinationName)
			return nil, fmt.Errorf("%w: protect resource %q: %v", ErrContent, resource.AssetID, err)
		}
		writtenInfo, statErr := destination.Stat()
		closeDestinationErr := destination.Close()
		if statErr != nil || closeDestinationErr != nil {
			_ = root.Remove(destinationName)
			return nil, fmt.Errorf("%w: finalize resource %q: %v", ErrContent, resource.AssetID, errors.Join(statErr, closeDestinationErr))
		}
		retained, openErr := root.Open(destinationName)
		if openErr != nil {
			_ = root.Remove(destinationName)
			return nil, fmt.Errorf("%w: retain resource %q: %v", ErrContent, resource.AssetID, openErr)
		}
		retainedInfo, retainedStatErr := retained.Stat()
		if retainedStatErr != nil || !retainedInfo.Mode().IsRegular() || !os.SameFile(writtenInfo, retainedInfo) {
			_ = retained.Close()
			_ = root.Remove(destinationName)
			return nil, fmt.Errorf("%w: staged resource %q changed while retaining its handle", ErrContent, resource.AssetID)
		}
		staged.resources[index].Locator = destinationName
		staged.names = append(staged.names, destinationName)
		staged.files = append(staged.files, retained)
	}
	completed = true
	return staged, nil
}

// detachPaths removes all media names relative to the directory handle, then removes only the now
// empty directory entry. It deliberately never uses recursive deletion. Missing/replaced names are
// errors, but the retained media descriptors remain safe to close or execute.
func (staged *stagedInputs) detachPaths(output *os.Root) error {
	if staged == nil || staged.detached {
		return nil
	}
	var cleanupErrors []error
	for index, name := range staged.names {
		if staged.removed[index] {
			continue
		}
		if err := staged.root.Remove(name); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("unlink staged input %q: %w", name, err))
			continue
		}
		staged.removed[index] = true
	}
	if len(cleanupErrors) != 0 {
		return errors.Join(cleanupErrors...)
	}
	if staged.root != nil {
		if err := staged.root.Close(); err != nil {
			return fmt.Errorf("close staged input root: %w", err)
		}
		staged.root = nil
	}
	if err := output.Remove(staged.directory); err != nil {
		return fmt.Errorf("remove empty staged input directory: %w", err)
	}
	staged.detached = true
	return nil
}

func (staged *stagedInputs) verify(ctx context.Context) error {
	if staged == nil || len(staged.files) != len(staged.resources) {
		return fmt.Errorf("%w: staged input handle count drift", ErrContent)
	}
	for index, file := range staged.files {
		contentHash, size, err := hashOpenFile(ctx, file)
		if err != nil {
			return fmt.Errorf("%w: resource %q: %w", ErrContent, staged.resources[index].AssetID, err)
		}
		resource := staged.resources[index]
		if contentHash != resource.ContentHash || size != resource.SizeBytes {
			return fmt.Errorf("%w: resource %q got %s/%d, want %s/%d", ErrContent, resource.AssetID, contentHash, size, resource.ContentHash, resource.SizeBytes)
		}
	}
	return nil
}

func (staged *stagedInputs) closeFiles() error {
	if staged == nil || staged.filesClosed {
		return nil
	}
	var closeErrors []error
	for _, file := range staged.files {
		closeErrors = append(closeErrors, file.Close())
	}
	staged.filesClosed = true
	return errors.Join(closeErrors...)
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != count {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
