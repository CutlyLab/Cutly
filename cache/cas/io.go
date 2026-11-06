package cas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

const ioBufferBytes = 64 << 10

func writeDurableFile(filesystem FileSystem, path string, content []byte, permission fs.FileMode) (returnErr error) {
	file, err := filesystem.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permission)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	for len(content) > 0 {
		written, writeErr := file.Write(content)
		if writeErr != nil {
			return writeErr
		}
		if written <= 0 || written > len(content) {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return file.Sync()
}

func readSmallFile(filesystem FileSystem, path string, limit int64) (_ []byte, returnErr error) {
	info, err := filesystem.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > limit {
		return nil, fmt.Errorf("%w: %s is not a bounded regular file", ErrCorrupt, path)
	}
	file, err := filesystem.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.Join(fmt.Errorf("%w: %s changed while opening", ErrCorrupt, path), err)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit || int64(len(raw)) != opened.Size() {
		return nil, fmt.Errorf("%w: %s changed while reading", ErrCorrupt, path)
	}
	return raw, nil
}

func copyAndHash(ctx context.Context, destination File, source io.Reader, maximum int64) (string, int64, error) {
	if ctx == nil || destination == nil || source == nil {
		return "", 0, fmt.Errorf("%w: nil copy input", ErrInvalidRequest)
	}
	hash := sha256.New()
	buffer := make([]byte, ioBufferBytes)
	var count int64
	for {
		if err := ctx.Err(); err != nil {
			return "", count, err
		}
		read, readErr := source.Read(buffer)
		if read < 0 || read > len(buffer) {
			return "", count, fmt.Errorf("%w: payload reader returned invalid byte count %d", ErrInvalidRequest, read)
		}
		if read > 0 {
			if count > maximum-int64(read) {
				return "", count, fmt.Errorf("%w: payload exceeds expected size %d", ErrInvalidRequest, maximum)
			}
			chunk := buffer[:read]
			if err := writeAll(destination, chunk); err != nil {
				return "", count, err
			}
			_, _ = hash.Write(chunk)
			count += int64(read)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", count, readErr
		}
		if read == 0 {
			return "", count, io.ErrNoProgress
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), count, nil
}

func hashFile(ctx context.Context, file File, maximum int64) (string, int64, error) {
	if ctx == nil || file == nil {
		return "", 0, fmt.Errorf("%w: nil hash input", ErrInvalidRequest)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	buffer := make([]byte, ioBufferBytes)
	var count int64
	for {
		if err := ctx.Err(); err != nil {
			return "", count, err
		}
		read, readErr := file.Read(buffer)
		if read < 0 || read > len(buffer) {
			return "", count, fmt.Errorf("%w: payload file returned invalid byte count %d", ErrCorrupt, read)
		}
		if read > 0 {
			if count > maximum-int64(read) {
				return "", count, fmt.Errorf("%w: payload exceeds manifest size", ErrCorrupt)
			}
			_, _ = hash.Write(buffer[:read])
			count += int64(read)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", count, readErr
		}
		if read == 0 {
			return "", count, io.ErrNoProgress
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", count, err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), count, nil
}

func writeAll(destination io.Writer, content []byte) error {
	for len(content) > 0 {
		written, err := destination.Write(content)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(content) {
			return io.ErrShortWrite
		}
		content = content[written:]
	}
	return nil
}
