//go:build darwin || linux || freebsd

package videodecode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type sourceSnapshot struct {
	file        *os.File
	contentHash string
	sizeBytes   int64
}

func materializeLocalInput(ctx context.Context, path, expectedHash string, expectedSize, maxBytes int64) (_ *sourceSnapshot, returnErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrInvalid)
	}
	if !fingerprintPattern.MatchString(expectedHash) || expectedSize <= 0 || expectedSize > maxBytes {
		return nil, fmt.Errorf("%w: invalid expected content identity", ErrInvalid)
	}
	resolved, err := secureLocalPath(path)
	if err != nil {
		return nil, err
	}
	before, err := os.Lstat(resolved)
	if err != nil || !before.Mode().IsRegular() || before.Size() != expectedSize {
		return nil, fmt.Errorf("%w: source size or type", ErrContentChanged)
	}
	source, opened, err := openRegularNoFollow(resolved)
	if err != nil {
		return nil, fmt.Errorf("%w: open input: %v", ErrInvalid, err)
	}
	defer source.Close()
	if !os.SameFile(before, opened) {
		return nil, fmt.Errorf("%w: source changed while opening", ErrContentChanged)
	}
	if _, err := secureLocalPath(resolved); err != nil {
		return nil, fmt.Errorf("%w: source path changed while opening: %v", ErrContentChanged, err)
	}
	current, err := os.Lstat(resolved)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(current, opened) {
		return nil, fmt.Errorf("%w: source path resolved to another file", ErrContentChanged)
	}

	directory, err := os.MkdirTemp("", ".cutly-video-decode-")
	if err != nil {
		return nil, fmt.Errorf("%w: private directory: %v", ErrInvalid, err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.Remove(directory)
		return nil, fmt.Errorf("%w: private directory permissions: %v", ErrInvalid, err)
	}
	name := filepath.Join(directory, "snapshot")
	writable, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.Remove(directory)
		return nil, fmt.Errorf("%w: private snapshot: %v", ErrInvalid, err)
	}
	cleanupPath := true
	defer func() {
		_ = writable.Close()
		if cleanupPath {
			_ = os.Remove(name)
			_ = os.Remove(directory)
		}
	}()

	hasher := sha256.New()
	buffer := make([]byte, 128<<10)
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			copied += int64(read)
			if copied > maxBytes || copied > expectedSize {
				return nil, fmt.Errorf("%w: input exceeds byte budget", ErrResourceLimit)
			}
			if _, err := hasher.Write(buffer[:read]); err != nil {
				return nil, fmt.Errorf("%w: hash input: %v", ErrInvalid, err)
			}
			if _, err := writable.Write(buffer[:read]); err != nil {
				return nil, fmt.Errorf("%w: copy input: %v", ErrInvalid, err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("%w: read input: %v", ErrInvalid, readErr)
		}
	}
	if copied != expectedSize {
		return nil, fmt.Errorf("%w: copied size %d, expected %d", ErrContentChanged, copied, expectedSize)
	}
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if digest != expectedHash {
		return nil, fmt.Errorf("%w: got %s, expected %s", ErrContentHash, digest, expectedHash)
	}
	after, err := source.Stat()
	if err != nil || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
		return nil, fmt.Errorf("%w: source metadata changed during copy", ErrContentChanged)
	}
	if err := writable.Sync(); err != nil {
		return nil, fmt.Errorf("%w: sync snapshot: %v", ErrInvalid, err)
	}
	if err := writable.Close(); err != nil {
		return nil, fmt.Errorf("%w: close writable snapshot: %v", ErrInvalid, err)
	}
	if err := os.Chmod(name, 0o400); err != nil {
		return nil, fmt.Errorf("%w: snapshot permissions: %v", ErrInvalid, err)
	}
	retained, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("%w: retain snapshot: %v", ErrInvalid, err)
	}
	if err := os.Remove(name); err != nil {
		_ = retained.Close()
		return nil, fmt.Errorf("%w: unlink snapshot: %v", ErrCleanup, err)
	}
	if err := os.Remove(directory); err != nil {
		_ = retained.Close()
		return nil, fmt.Errorf("%w: remove private directory: %v", ErrCleanup, err)
	}
	cleanupPath = false
	return &sourceSnapshot{file: retained, contentHash: digest, sizeBytes: copied}, nil
}

func secureLocalPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%w: path is required", ErrInvalid)
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "" && filepath.VolumeName(value) == "") {
		return "", fmt.Errorf("%w: URL/protocol inputs are forbidden", ErrInvalid)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("%w: absolute path: %v", ErrInvalid, err)
	}
	for current := filepath.Clean(absolute); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("%w: inspect path: %v", ErrInvalid, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: symlink path components are forbidden", ErrInvalid)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return absolute, nil
}

func (value *sourceSnapshot) inheritedBinding() (string, []*os.File, error) {
	if value == nil || value.file == nil {
		return "", nil, ErrClosed
	}
	if _, err := value.file.Seek(0, io.SeekStart); err != nil {
		return "", nil, fmt.Errorf("%w: rewind snapshot: %v", ErrInvalid, err)
	}
	return "fd:", []*os.File{value.file}, nil
}

func (value *sourceSnapshot) close() error {
	if value == nil || value.file == nil {
		return nil
	}
	err := value.file.Close()
	value.file = nil
	return err
}
